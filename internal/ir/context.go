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

func CalculateContext(b []byte, i int) syntax.EmptyOp {
	var r1, r2 rune = -1, -1
	if i > 0 {
		j := i - 1
		for j > 0 && b[j]&0xC0 == 0x80 {
			j--
		}
		r1 = decodeRune(b[j:i])
	}
	if i < len(b) {
		r2 = decodeRune(b[i:])
	}

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

func IsWord(r rune) bool {
	if r < 0 {
		return false
	}
	if r <= 0x7F {
		return syntax.IsWordChar(r)
	}
	return false
}

func decodeRune(b []byte) rune {
	if len(b) == 0 {
		return -1
	}
	if b[0] < 0x80 {
		return rune(b[0])
	}
	r, _ := DecodeRune(b)
	return r
}
