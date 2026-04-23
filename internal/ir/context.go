package ir

import (
	"github.com/kamichidu/go-regexp-re/syntax"
)

// Tiny, inlineable anchor verification functions.
// These use exact syntax.EmptyOp bits to stay efficient and correct.

func VerifyBegin(b []byte, i int, req syntax.EmptyOp) bool {
	// EmptyBeginText(4) | EmptyBeginLine(1)
	if (req & (syntax.EmptyBeginText | syntax.EmptyBeginLine)) == 0 {
		return true
	}
	if i == 0 {
		// Text start satisfies both BeginText and BeginLine
		return true
	}
	// Line start only satisfies BeginLine
	return (req&syntax.EmptyBeginLine) != 0 && b[i-1] == '\n'
}

func VerifyEnd(b []byte, i int, numBytes int, req syntax.EmptyOp) bool {
	// EmptyEndText(8) | EmptyEndLine(2)
	if (req & (syntax.EmptyEndText | syntax.EmptyEndLine)) == 0 {
		return true
	}
	if i == numBytes {
		// Text end satisfies both EndText and EndLine
		return true
	}
	// Line end only satisfies EndLine
	return (req&syntax.EmptyEndLine) != 0 && b[i] == '\n'
}

func VerifyWord(b []byte, i int, numBytes int, req syntax.EmptyOp) bool {
	if (req & (syntax.EmptyWordBoundary | syntax.EmptyNoWordBoundary)) == 0 {
		return true
	}
	var wordLeft, wordRight bool
	if i > 0 && b[i-1] < 0x80 && syntax.IsWordChar(rune(b[i-1])) {
		wordLeft = true
	}
	if i < numBytes && b[i] < 0x80 && syntax.IsWordChar(rune(b[i])) {
		wordRight = true
	}
	if wordLeft != wordRight {
		return (req & syntax.EmptyWordBoundary) != 0
	}
	return (req & syntax.EmptyNoWordBoundary) != 0
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
