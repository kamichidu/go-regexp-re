package syntax

import (
	"strings"

	gosyntax "regexp/syntax"
)

// Regexp is a pointer to gosyntax.Regexp.
type Regexp = gosyntax.Regexp

// Flags is a bitmask of parsing flags.
type Flags = gosyntax.Flags

const (
	FoldCase      Flags = gosyntax.FoldCase
	Literal       Flags = gosyntax.Literal
	ClassNL       Flags = gosyntax.ClassNL
	DotNL         Flags = gosyntax.DotNL
	OneLine       Flags = gosyntax.OneLine
	NonGreedy     Flags = gosyntax.NonGreedy
	PerlX         Flags = gosyntax.PerlX
	UnicodeGroups Flags = gosyntax.UnicodeGroups
	WasDollar     Flags = gosyntax.WasDollar
	MatchNL       Flags = gosyntax.MatchNL
	Simple        Flags = gosyntax.Simple

	Perl  Flags = gosyntax.Perl
	POSIX Flags = gosyntax.POSIX
)

// Parse parses a regular expression string.
func Parse(s string, flags Flags) (*Regexp, error) {
	return gosyntax.Parse(s, flags)
}

// ParsePOSIX parses a regular expression string using POSIX syntax.
func ParsePOSIX(s string, flags Flags) (*Regexp, error) {
	return gosyntax.Parse(s, gosyntax.POSIX|flags)
}

// Simplify returns a simplified version of the regular expression.
func Simplify(re *Regexp) *Regexp {
	return re.Simplify()
}

// Optimize returns an optimized version of the regular expression.
// It merges common prefixes in alternations to reduce the number of DFA states.
func Optimize(re *Regexp) *Regexp {
	if re == nil {
		return nil
	}

	for i, sub := range re.Sub {
		re.Sub[i] = Optimize(sub)
	}

	switch re.Op {
	case OpAlternate:
		re = factorAlternate(re)
	case OpConcat:
		re = flattenConcat(re)
		re = aggregateLiterals(re)
		if len(re.Sub) == 1 {
			return re.Sub[0]
		}
	}

	return re
}

func flattenConcat(re *Regexp) *Regexp {
	if re.Op != OpConcat {
		return re
	}

	hasNested := false
	for _, sub := range re.Sub {
		if sub.Op == OpConcat {
			hasNested = true
			break
		}
	}
	if !hasNested {
		return re
	}

	var subs []*Regexp
	for _, sub := range re.Sub {
		if sub.Op == OpConcat {
			subs = append(subs, sub.Sub...)
		} else {
			subs = append(subs, sub)
		}
	}
	re.Sub = subs
	return re
}

func aggregateLiterals(re *Regexp) *Regexp {
	if re.Op != OpConcat || len(re.Sub) <= 1 {
		return re
	}

	var newSubs []*Regexp
	var lastLiteral *Regexp

	for _, sub := range re.Sub {
		if sub.Op == OpLiteral && sub.Flags&FoldCase == 0 {
			if lastLiteral != nil && lastLiteral.Flags&FoldCase == 0 {
				lastLiteral.Rune = append(lastLiteral.Rune, sub.Rune...)
			} else {
				lastLiteral = &Regexp{
					Op:    OpLiteral,
					Rune:  append([]rune(nil), sub.Rune...),
					Flags: sub.Flags,
				}
				newSubs = append(newSubs, lastLiteral)
			}
		} else {
			newSubs = append(newSubs, sub)
			lastLiteral = nil
		}
	}

	if len(newSubs) == len(re.Sub) {
		return re
	}
	re.Sub = newSubs
	return re
}

func factorAlternate(re *Regexp) *Regexp {
	if len(re.Sub) <= 1 {
		return re
	}

	// 1. Flatten nested alternates
	var subs []*Regexp
	for _, sub := range re.Sub {
		if sub.Op == OpAlternate {
			subs = append(subs, sub.Sub...)
		} else {
			subs = append(subs, sub)
		}
	}

	// 2. Prefix factoring
	subs = factorPrefix(subs)

	// 3. Suffix factoring
	subs = factorSuffix(subs)

	if len(subs) == 1 {
		return subs[0]
	}
	re.Sub = subs
	return re
}

func factorPrefix(subs []*Regexp) []*Regexp {
	if len(subs) <= 1 {
		return subs
	}

	type group struct {
		head  *Regexp
		items []*Regexp
	}
	var groups []*group
	hasCommon := false
	for _, sub := range subs {
		head, rest := splitHead(sub)
		found := false
		for _, g := range groups {
			if head.Op != OpEmptyMatch && equal(g.head, head) {
				g.items = append(g.items, rest)
				found = true
				hasCommon = true
				break
			}
		}
		if !found {
			groups = append(groups, &group{head, []*Regexp{rest}})
		}
	}

	if !hasCommon {
		return subs
	}

	var newSubs []*Regexp
	for _, g := range groups {
		if len(g.items) == 1 {
			newSubs = append(newSubs, combineHead(g.head, g.items[0]))
		} else {
			alt := &Regexp{Op: OpAlternate, Sub: g.items}
			newSubs = append(newSubs, combineHead(g.head, Optimize(alt)))
		}
	}
	return newSubs
}

func factorSuffix(subs []*Regexp) []*Regexp {
	if len(subs) <= 1 {
		return subs
	}

	type group struct {
		tail  *Regexp
		items []*Regexp
	}
	var groups []*group
	hasCommon := false
	for _, sub := range subs {
		rest, tail := splitTail(sub)
		found := false
		for _, g := range groups {
			if tail.Op != OpEmptyMatch && equal(g.tail, tail) {
				g.items = append(g.items, rest)
				found = true
				hasCommon = true
				break
			}
		}
		if !found {
			groups = append(groups, &group{tail, []*Regexp{rest}})
		}
	}

	if !hasCommon {
		return subs
	}

	var newSubs []*Regexp
	for _, g := range groups {
		if len(g.items) == 1 {
			newSubs = append(newSubs, combineTail(g.items[0], g.tail))
		} else {
			alt := &Regexp{Op: OpAlternate, Sub: g.items}
			newSubs = append(newSubs, combineTail(Optimize(alt), g.tail))
		}
	}
	return newSubs
}

func splitHead(re *Regexp) (head, rest *Regexp) {
	if re.Op == OpConcat && len(re.Sub) > 0 {
		head = re.Sub[0]
		if len(re.Sub) == 2 {
			rest = re.Sub[1]
		} else {
			rest = &Regexp{Op: OpConcat, Sub: re.Sub[1:]}
		}
		return head, rest
	}
	return re, &Regexp{Op: OpEmptyMatch}
}

func combineHead(head, rest *Regexp) *Regexp {
	if rest.Op == OpEmptyMatch {
		return head
	}
	res := &Regexp{Op: OpConcat}
	if rest.Op == OpConcat {
		res.Sub = append([]*Regexp{head}, rest.Sub...)
	} else {
		res.Sub = []*Regexp{head, rest}
	}
	return res
}

func splitTail(re *Regexp) (rest, tail *Regexp) {
	if re.Op == OpConcat && len(re.Sub) > 0 {
		tail = re.Sub[len(re.Sub)-1]
		if len(re.Sub) == 2 {
			rest = re.Sub[0]
		} else {
			rest = &Regexp{Op: OpConcat, Sub: re.Sub[:len(re.Sub)-1]}
		}
		return rest, tail
	}
	return re, &Regexp{Op: OpEmptyMatch}
}

func combineTail(rest, tail *Regexp) *Regexp {
	if rest.Op == OpEmptyMatch {
		return tail
	}
	res := &Regexp{Op: OpConcat}
	if rest.Op == OpConcat {
		res.Sub = append(rest.Sub, tail)
	} else {
		res.Sub = []*Regexp{rest, tail}
	}
	return res
}

func equal(a, b *Regexp) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if a.Op != b.Op || a.Flags != b.Flags {
		return false
	}
	if len(a.Rune) != len(b.Rune) {
		return false
	}
	for i := range a.Rune {
		if a.Rune[i] != b.Rune[i] {
			return false
		}
	}
	if a.Min != b.Min || a.Max != b.Max || a.Cap != b.Cap || a.Name != b.Name {
		return false
	}
	if len(a.Sub) != len(b.Sub) {
		return false
	}
	for i := range a.Sub {
		if !equal(a.Sub[i], b.Sub[i]) {
			return false
		}
	}
	return true
}

// Compile compiles the regular expression to a program.
func Compile(re *Regexp) (*Prog, error) {
	return gosyntax.Compile(re)
}

// Prefix returns the constant prefix of the regular expression.
func Prefix(re *Regexp) (string, bool) {
	if re.Flags&gosyntax.FoldCase != 0 {
		return "", false
	}
	switch re.Op {
	case gosyntax.OpLiteral:
		return string(re.Rune), true
	case gosyntax.OpCharClass:
		if len(re.Rune) == 2 && re.Rune[0] == re.Rune[1] {
			return string(re.Rune[0]), true
		}
	case gosyntax.OpCapture:
		return Prefix(re.Sub[0])
	case gosyntax.OpConcat:
		var sb strings.Builder
		allComplete := true
		for i, sub := range re.Sub {
			if i == 0 && (sub.Op == gosyntax.OpBeginText || sub.Op == gosyntax.OpBeginLine) {
				allComplete = false
				continue
			}
			p, complete := Prefix(sub)
			sb.WriteString(p)
			if !complete {
				return sb.String(), false
			}
		}
		return sb.String(), allComplete
	}
	return "", false
}

// An Error describes a failure to parse a regular expression
// and gives the offending expression.
type Error = gosyntax.Error

// An ErrorCode describes a category of parsing error.
type ErrorCode = gosyntax.ErrorCode

const (
	ErrInternalError ErrorCode = gosyntax.ErrInternalError

	// Parse errors
	ErrInvalidRepeatOp       ErrorCode = gosyntax.ErrInvalidRepeatOp
	ErrInvalidRepeatSize     ErrorCode = gosyntax.ErrInvalidRepeatSize
	ErrInvalidEscape         ErrorCode = gosyntax.ErrInvalidEscape
	ErrInvalidNamedCapture   ErrorCode = gosyntax.ErrInvalidNamedCapture
	ErrInvalidCharClass      ErrorCode = gosyntax.ErrInvalidCharClass
	ErrInvalidCharRange      ErrorCode = gosyntax.ErrInvalidCharRange
	ErrMissingBracket        ErrorCode = gosyntax.ErrMissingBracket
	ErrMissingParen          ErrorCode = gosyntax.ErrMissingParen
	ErrMissingRepeatArgument ErrorCode = gosyntax.ErrMissingRepeatArgument
	ErrTrailingBackslash     ErrorCode = gosyntax.ErrTrailingBackslash
	ErrUnexpectedParen       ErrorCode = gosyntax.ErrUnexpectedParen
)

// IsWordChar reports whether r is considered a “word” character
// during the evaluation of the \b and \B zero-width assertions.
// These characters are [A-Za-z0-9_].
func IsWordChar(r rune) bool {
	return gosyntax.IsWordChar(r)
}

// Inst is a single instruction in a regular expression program.
type Inst = gosyntax.Inst

// Prog is a compiled regular expression program.
type Prog = gosyntax.Prog

// Op is a regular expression operator.
type Op = gosyntax.Op

const (
	OpNoMatch        Op = gosyntax.OpNoMatch
	OpEmptyMatch     Op = gosyntax.OpEmptyMatch
	OpLiteral        Op = gosyntax.OpLiteral
	OpCharClass      Op = gosyntax.OpCharClass
	OpAnyCharNotNL   Op = gosyntax.OpAnyCharNotNL
	OpAnyChar        Op = gosyntax.OpAnyChar
	OpBeginLine      Op = gosyntax.OpBeginLine
	OpEndLine        Op = gosyntax.OpEndLine
	OpBeginText      Op = gosyntax.OpBeginText
	OpEndText        Op = gosyntax.OpEndText
	OpWordBoundary   Op = gosyntax.OpWordBoundary
	OpNoWordBoundary Op = gosyntax.OpNoWordBoundary
	OpCapture        Op = gosyntax.OpCapture
	OpStar           Op = gosyntax.OpStar
	OpPlus           Op = gosyntax.OpPlus
	OpQuest          Op = gosyntax.OpQuest
	OpRepeat         Op = gosyntax.OpRepeat
	OpConcat         Op = gosyntax.OpConcat
	OpAlternate      Op = gosyntax.OpAlternate
)

// InstOp is an instruction opcode.
type InstOp = gosyntax.InstOp

const (
	InstAlt          InstOp = gosyntax.InstAlt
	InstAltMatch     InstOp = gosyntax.InstAltMatch
	InstCapture      InstOp = gosyntax.InstCapture
	InstEmptyWidth   InstOp = gosyntax.InstEmptyWidth
	InstMatch        InstOp = gosyntax.InstMatch
	InstFail         InstOp = gosyntax.InstFail
	InstNop          InstOp = gosyntax.InstNop
	InstRune         InstOp = gosyntax.InstRune
	InstRune1        InstOp = gosyntax.InstRune1
	InstRuneAny      InstOp = gosyntax.InstRuneAny
	InstRuneAnyNotNL InstOp = gosyntax.InstRuneAnyNotNL
)

// EmptyOp is a bitmask of empty-width assertions.
type EmptyOp = gosyntax.EmptyOp

const (
	EmptyBeginLine      EmptyOp = gosyntax.EmptyBeginLine
	EmptyEndLine        EmptyOp = gosyntax.EmptyEndLine
	EmptyBeginText      EmptyOp = gosyntax.EmptyBeginText
	EmptyEndText        EmptyOp = gosyntax.EmptyEndText
	EmptyWordBoundary   EmptyOp = gosyntax.EmptyWordBoundary
	EmptyNoWordBoundary EmptyOp = gosyntax.EmptyNoWordBoundary
)

// EmptyOpContext returns the EmptyOp at the position between r1 and r2.
func EmptyOpContext(r1, r2 rune) EmptyOp {
	return gosyntax.EmptyOpContext(r1, r2)
}
