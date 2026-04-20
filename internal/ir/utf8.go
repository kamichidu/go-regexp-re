package ir

import (
	"unicode/utf8"
)

type UTF8Node struct {
	ID     int
	Ranges []ByteRange
	Next   []*UTF8Node
}

func (n *UTF8Node) Match(b byte, fold bool) bool {
	for _, r := range n.Ranges {
		if b >= r.Lo && b <= r.Hi {
			return true
		}
	}
	return false
}

type ByteRange struct {
	Lo, Hi byte
}

func DecodeRune(b []byte) (rune, int) {
	return utf8.DecodeRune(b)
}

func DecodeLastRune(b []byte) (rune, int) {
	return utf8.DecodeLastRune(b)
}
