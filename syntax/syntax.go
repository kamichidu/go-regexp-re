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

	if re.Op == OpAlternate {
		return optimizeAlternate(re)
	}

	return re
}

func optimizeAlternate(re *Regexp) *Regexp {
	if len(re.Sub) <= 1 {
		return re
	}

	// 1. Flatten nested alternates and collect subs
	var subs []*Regexp
	for _, sub := range re.Sub {
		if sub.Op == OpAlternate {
			subs = append(subs, sub.Sub...)
		} else {
			subs = append(subs, sub)
		}
	}

	// 2. Group by prefix while preserving first-appearance order
	type group struct {
		prefix []rune
		flags  Flags
		items  []*Regexp
	}
	type entry struct {
		isGroup bool
		group   *group
		other   *Regexp
	}

	var entries []*entry
	groupMap := make(map[string]*group)

	for _, sub := range subs {
		prefix, flags, rest := getLeadingLiteral(sub)
		if len(prefix) > 0 {
			key := string(prefix) + "|" + string(rune(flags))
			if g, ok := groupMap[key]; ok {
				g.items = append(g.items, rest)
			} else {
				g := &group{prefix, flags, []*Regexp{rest}}
				groupMap[key] = g
				entries = append(entries, &entry{isGroup: true, group: g})
			}
		} else {
			entries = append(entries, &entry{other: sub})
		}
	}

	// 3. Rebuild alternate with merged prefixes
	var newSubs []*Regexp
	for _, e := range entries {
		if e.isGroup {
			if len(e.group.items) == 1 {
				newSubs = append(newSubs, concatPrefix(e.group.prefix, e.group.flags, e.group.items[0]))
			} else {
				alt := &Regexp{Op: OpAlternate}
				alt.Sub = e.group.items
				newSubs = append(newSubs, concatPrefix(e.group.prefix, e.group.flags, Optimize(alt)))
			}
		} else {
			newSubs = append(newSubs, e.other)
		}
	}

	if len(newSubs) == 1 {
		return newSubs[0]
	}
	re.Sub = newSubs
	return re
}

func getLeadingLiteral(re *Regexp) (prefix []rune, flags Flags, rest *Regexp) {
	switch re.Op {
	case OpLiteral:
		if len(re.Rune) > 1 {
			return re.Rune[:1], re.Flags, &Regexp{Op: OpLiteral, Rune: re.Rune[1:], Flags: re.Flags}
		}
		return re.Rune, re.Flags, &Regexp{Op: OpEmptyMatch}
	case OpConcat:
		if len(re.Sub) > 0 {
			p, f, r := getLeadingLiteral(re.Sub[0])
			if len(p) > 0 {
				var newRest *Regexp
				if r.Op == OpEmptyMatch {
					if len(re.Sub) == 2 {
						newRest = re.Sub[1]
					} else {
						newRest = &Regexp{Op: OpConcat, Sub: re.Sub[1:]}
					}
				} else {
					newRest = &Regexp{Op: OpConcat}
					newRest.Sub = append([]*Regexp{r}, re.Sub[1:]...)
				}
				return p, f, newRest
			}
		}
	}
	return nil, 0, nil
}

func concatPrefix(prefix []rune, flags Flags, rest *Regexp) *Regexp {
	pre := &Regexp{Op: OpLiteral, Rune: prefix, Flags: flags}
	if rest.Op == OpEmptyMatch {
		return pre
	}
	res := &Regexp{Op: OpConcat}
	if rest.Op == OpConcat {
		res.Sub = append([]*Regexp{pre}, rest.Sub...)
	} else {
		res.Sub = []*Regexp{pre, rest}
	}
	return res
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
