package ir

import (
	"github.com/kamichidu/go-regexp-re/syntax"
)

type Input struct {
	B           []byte // スキャン対象スライス
	AbsPos      int    // スライス先頭の絶対位置
	TotalBytes  int    // 入力全体の長さ
	SearchStart int    // B内での相対開始位置
	SearchEnd   int    // B内での相対終了位置
}

func VerifyBegin(in Input, i int, req syntax.EmptyOp) bool {
	if (req & (syntax.EmptyBeginText | syntax.EmptyBeginLine)) == 0 {
		return true
	}
	if (in.AbsPos + i) == 0 {
		return true
	}
	return (req&syntax.EmptyBeginLine) != 0 && i > 0 && in.B[i-1] == '\n'
}

func VerifyEnd(in Input, i int, req syntax.EmptyOp) bool {
	if (req & (syntax.EmptyEndText | syntax.EmptyEndLine)) == 0 {
		return true
	}
	if (in.AbsPos + i) == in.TotalBytes {
		return true
	}
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
