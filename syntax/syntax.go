package syntax

import (
	"strings"

	gosyntax "regexp/syntax"
)

type Regexp = gosyntax.Regexp
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
	Simple        Flags = gosyntax.Simple

	MatchNL = gosyntax.MatchNL

	Perl  = gosyntax.Perl
	POSIX = gosyntax.POSIX
)

type UnsupportedError struct {
	Op string
}

func (e *UnsupportedError) Error() string {
	return "regexp: unsupported operator: " + e.Op
}

func Parse(s string, flags Flags) (*Regexp, error) {
	return gosyntax.Parse(s, flags)
}

func ParsePOSIX(s string, flags Flags) (*Regexp, error) {
	return gosyntax.Parse(s, gosyntax.POSIX|flags)
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
