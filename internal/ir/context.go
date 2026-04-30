package ir

import (
	"github.com/kamichidu/go-regexp-re/syntax"
)

// Input represents the search input with absolute coordinate context.
type Input struct {
	B           []byte // The virtual slice currently being scanned
	OriginalB   []byte // The full original input buffer
	AbsPos      int    // The absolute position of B[0] in OriginalB
	TotalBytes  int    // The total length of OriginalB
	SearchStart int    // Relative start position in B
	SearchEnd   int    // Relative end position in B
}

// VerifyBegin checks for ^ and \A anchors using absolute context.
func VerifyBegin(in Input, i int, req syntax.EmptyOp) bool {
	if (req & (syntax.EmptyBeginText | syntax.EmptyBeginLine)) == 0 {
		return true
	}
	absPos := in.AbsPos + i
	if absPos == 0 {
		return true
	}
	return (req&syntax.EmptyBeginLine) != 0 && absPos > 0 && in.OriginalB[absPos-1] == '\n'
}

// VerifyEnd checks for $ and \z anchors using absolute context.
func VerifyEnd(in Input, i int, req syntax.EmptyOp) bool {
	if (req & (syntax.EmptyEndText | syntax.EmptyEndLine)) == 0 {
		return true
	}
	absPos := in.AbsPos + i
	if absPos == in.TotalBytes {
		return true
	}
	return (req&syntax.EmptyEndLine) != 0 && absPos < in.TotalBytes && in.OriginalB[absPos] == '\n'
}

// VerifyWord checks for \b and \B anchors using absolute context.
func VerifyWord(in Input, i int, req syntax.EmptyOp) bool {
	if (req & (syntax.EmptyWordBoundary | syntax.EmptyNoWordBoundary)) == 0 {
		return true
	}
	absPos := in.AbsPos + i
	var wordLeft, wordRight bool
	if absPos > 0 && in.OriginalB[absPos-1] < 0x80 && syntax.IsWordChar(rune(in.OriginalB[absPos-1])) {
		wordLeft = true
	}
	if absPos < in.TotalBytes && in.OriginalB[absPos] < 0x80 && syntax.IsWordChar(rune(in.OriginalB[absPos])) {
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
