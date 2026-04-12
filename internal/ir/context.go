package ir

import (
	"github.com/kamichidu/go-regexp-re/syntax"
)

// CalculateContext determines the empty-width assertions (anchors) that are true at position i in byte slice b.
func CalculateContext(b []byte, i int) syntax.EmptyOp {
	var r1, r2 rune = -1, -1
	if i == 0 {
		r1 = -1
	} else {
		r1 = rune(b[i-1])
	}
	if i == len(b) {
		r2 = -1
	} else {
		r2 = rune(b[i])
	}
	op := CalculateContextBetween(r1, r2)
	return op
}

// CalculateContextBetween returns the empty-width assertions that are true
// between two runes r1 and r2. Use -1 to represent the start or end of text.
func CalculateContextBetween(r1, r2 rune) syntax.EmptyOp {
	var op syntax.EmptyOp
	if r1 < 0 {
		op |= syntax.EmptyBeginText | syntax.EmptyBeginLine
	} else if r1 == '\n' {
		op |= syntax.EmptyBeginLine
	}
	if r2 < 0 {
		op |= syntax.EmptyEndText | syntax.EmptyEndLine
	} else if r2 == '\n' {
		op |= syntax.EmptyEndLine
	}
	if IsWord(r1) != IsWord(r2) {
		op |= syntax.EmptyWordBoundary
	} else {
		op |= syntax.EmptyNoWordBoundary
	}
	return op
}

// IsWordBoundary reports whether position i in byte slice b is a word boundary.
func IsWordBoundary(b []byte, i int) bool {
	var r1, r2 rune = -1, -1
	if i > 0 {
		r1 = rune(b[i-1])
	}
	if i < len(b) {
		r2 = rune(b[i])
	}
	return IsWord(r1) != IsWord(r2)
}

// IsWord reports whether rune r is considered a "word" character ([a-zA-Z0-9_]).
func IsWord(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
}
