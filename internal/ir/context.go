package ir

import (
	"github.com/kamichidu/go-regexp-re/syntax"
)

type RuneClass uint8

const (
	RuneClassOther RuneClass = iota
	RuneClassWord
	RuneClassNL
	RuneClassStart
)

func GetRuneClass(r rune) RuneClass {
	if r < 0 {
		return RuneClassStart
	}
	if r == '\n' {
		return RuneClassNL
	}
	if IsWord(r) {
		return RuneClassWord
	}
	return RuneClassOther
}

func GetByteClass(b byte) RuneClass {
	if b == '\n' {
		return RuneClassNL
	}
	if b < 0x80 {
		if syntax.IsWordChar(rune(b)) {
			return RuneClassWord
		}
		return RuneClassOther
	}
	return RuneClassOther
}

func CalculateContextBetween(c1, c2 RuneClass) syntax.EmptyOp {
	var op syntax.EmptyOp
	if c1 == RuneClassStart {
		op |= syntax.EmptyBeginText | syntax.EmptyBeginLine
	}
	if c1 == RuneClassNL {
		op |= syntax.EmptyBeginLine
	}

	isWord1 := (c1 == RuneClassWord)
	isWord2 := (c2 == RuneClassWord)
	if isWord1 != isWord2 {
		op |= syntax.EmptyWordBoundary
	} else {
		op |= syntax.EmptyNoWordBoundary
	}
	return op
}

func CalculateContextByClass(c1, c2 RuneClass) syntax.EmptyOp {
	var op syntax.EmptyOp
	if c1 == RuneClassStart {
		op |= syntax.EmptyBeginText | syntax.EmptyBeginLine
	}
	if c1 == RuneClassNL {
		op |= syntax.EmptyBeginLine
	}

	isWord1 := (c1 == RuneClassWord)
	isWord2 := (c2 == RuneClassWord)
	if isWord1 != isWord2 {
		op |= syntax.EmptyWordBoundary
	} else {
		op |= syntax.EmptyNoWordBoundary
	}
	return op
}

// CalculateContext determines the empty-width assertions at junction i.
// Strictly Byte-Oriented: No rune decoding, no loops.
// Optimized: Only performs checks required by the 'req' bitmask.
func CalculateContext(b []byte, i int, req syntax.EmptyOp) syntax.EmptyOp {
	var op syntax.EmptyOp

	// Junction Left Analysis
	if (req & (syntax.EmptyBeginText | syntax.EmptyBeginLine | syntax.EmptyWordBoundary | syntax.EmptyNoWordBoundary)) != 0 {
		if i == 0 {
			op |= syntax.EmptyBeginText | syntax.EmptyBeginLine
		} else {
			prev := b[i-1]
			if (req&syntax.EmptyBeginLine) != 0 && prev == '\n' {
				op |= syntax.EmptyBeginLine
			}
		}
	}

	// Junction Right Analysis
	if (req & (syntax.EmptyEndText | syntax.EmptyEndLine | syntax.EmptyWordBoundary | syntax.EmptyNoWordBoundary)) != 0 {
		if i == len(b) {
			op |= syntax.EmptyEndText | syntax.EmptyEndLine
		} else {
			curr := b[i]
			if (req&syntax.EmptyEndLine) != 0 && curr == '\n' {
				op |= syntax.EmptyEndLine
			}
		}
	}

	// Word Boundary Analysis (only if requested)
	if (req & (syntax.EmptyWordBoundary | syntax.EmptyNoWordBoundary)) != 0 {
		var wordLeft, wordRight bool
		if i > 0 {
			prev := b[i-1]
			if prev < 0x80 && syntax.IsWordChar(rune(prev)) {
				wordLeft = true
			}
		}
		if i < len(b) {
			curr := b[i]
			if curr < 0x80 && syntax.IsWordChar(rune(curr)) {
				wordRight = true
			}
		}

		if wordLeft != wordRight {
			op |= syntax.EmptyWordBoundary
		} else {
			op |= syntax.EmptyNoWordBoundary
		}
	}
	return op
}

func IsWord(r rune) bool {
	if r < 0 || r >= 0x80 {
		return false
	}
	return syntax.IsWordChar(r)
}

func GetTrailingByteCount(lead byte) int {
	if lead < 0xC2 {
		return 0
	}
	if lead < 0xE0 {
		return 1
	}
	if lead < 0xF0 {
		return 2
	}
	if lead < 0xF5 {
		return 3
	}
	return 0
}
