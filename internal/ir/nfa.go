package ir

import (
	"github.com/kamichidu/go-regexp-re/syntax"
)

// NFAMatch performs submatch extraction using an NFA.
// It selects the most efficient implementation based on the program characteristics.
func NFAMatch(prog *syntax.Prog, b []byte, start, end int, numSubexp int) []int {
	if len(prog.Inst) <= 64 {
		if res := nfaMatchBitParallel(prog, b, start, end, numSubexp); res != nil {
			return res
		}
	}
	return nfaMatchPikeVM(prog, b, start, end, numSubexp)
}

func calculateContext(b []byte, i int) syntax.EmptyOp {
	var op syntax.EmptyOp
	if i == 0 {
		op |= syntax.EmptyBeginText | syntax.EmptyBeginLine
	} else if i > 0 && b[i-1] == '\n' {
		op |= syntax.EmptyBeginLine
	}
	if i == len(b) {
		op |= syntax.EmptyEndText | syntax.EmptyEndLine
	} else if i < len(b) && b[i] == '\n' {
		op |= syntax.EmptyEndLine
	}
	if isWordBoundary(b, i) {
		op |= syntax.EmptyWordBoundary
	} else {
		op |= syntax.EmptyNoWordBoundary
	}
	return op
}

func isWordBoundary(b []byte, i int) bool {
	var r1, r2 rune = -1, -1
	if i > 0 {
		r1 = rune(b[i-1])
	}
	if i < len(b) {
		r2 = rune(b[i])
	}
	return isWord(r1) != isWord(r2)
}

func isWord(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
}
