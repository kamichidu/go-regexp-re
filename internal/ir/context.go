package ir

import (
	"github.com/kamichidu/go-regexp-re/syntax"
)

// Tiny, inlineable anchor verification functions.
// These use exact syntax.EmptyOp bits to stay efficient and correct.

func VerifyBegin(b []byte, i int, req syntax.EmptyOp) bool {
	// If BeginText is requested but we are not at 0, fail.
	if (req&syntax.EmptyBeginText) != 0 && i != 0 {
		return false
	}
	// If BeginLine is requested but we are not at 0 and not after \n, fail.
	if (req & syntax.EmptyBeginLine) != 0 {
		if i != 0 && b[i-1] != '\n' {
			return false
		}
	}
	return true
}

func VerifyEnd(b []byte, i int, numBytes int, req syntax.EmptyOp) bool {
	// If EndText is requested but we are not at numBytes, fail.
	if (req&syntax.EmptyEndText) != 0 && i != numBytes {
		return false
	}
	// If EndLine is requested but we are not at numBytes and not before \n, fail.
	if (req & syntax.EmptyEndLine) != 0 {
		if i != numBytes && b[i] != '\n' {
			return false
		}
	}
	return true
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
