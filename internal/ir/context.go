package ir

import (
	"github.com/kamichidu/go-regexp-re/syntax"
)

// Input represents the search input with absolute coordinate context.
type Input struct {
	B           []byte // The byte slice being scanned
	AbsPos      int    // The absolute position of B[0] in the original input
	TotalBytes  int    // The total length of the original input
	SearchStart int    // Relative start position in B
	SearchEnd   int    // Relative end position in B
}

// Tiny, inlineable anchor verification functions.
// These use exact syntax.EmptyOp bits to stay efficient and correct.

func VerifyBegin(in Input, i int, req syntax.EmptyOp) bool {
	// EmptyBeginText(4) | EmptyBeginLine(1)
	if (req & (syntax.EmptyBeginText | syntax.EmptyBeginLine)) == 0 {
		return true
	}
	// Text start satisfies both BeginText and BeginLine
	if (in.AbsPos + i) == 0 {
		return true
	}
	// Line start only satisfies BeginLine
	return (req&syntax.EmptyBeginLine) != 0 && i > 0 && in.B[i-1] == '\n'
}

func VerifyEnd(in Input, i int, req syntax.EmptyOp) bool {
	// EmptyEndText(8) | EmptyEndLine(2)
	if (req & (syntax.EmptyEndText | syntax.EmptyEndLine)) == 0 {
		return true
	}
	// Text end satisfies both EndText and EndLine
	if (in.AbsPos + i) == in.TotalBytes {
		return true
	}
	// Line end only satisfies EndLine
	return (req&syntax.EmptyEndLine) != 0 && i < len(in.B) && in.B[i] == '\n'
}

func VerifyWord(in Input, i int, req syntax.EmptyOp) bool {
	if (req & (syntax.EmptyWordBoundary | syntax.EmptyNoWordBoundary)) == 0 {
		return true
	}
	numBytes := len(in.B)
	var wordLeft, wordRight bool
	if i > 0 && in.B[i-1] < 0x80 && syntax.IsWordChar(rune(in.B[i-1])) {
		wordLeft = true
	}
	if i < numBytes && in.B[i] < 0x80 && syntax.IsWordChar(rune(in.B[i])) {
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
