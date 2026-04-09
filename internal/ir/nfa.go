package ir

import (
	"github.com/kamichidu/go-regexp-re/syntax"
)

// regState represents the capture registers with a reference count for efficient reuse.
type regState struct {
	slots []int
	refs  int
}

// NFAMatch performs submatch extraction using an NFA.
type thread struct {
	pc   uint32
	node *utf8Node // Current position in the UTF-8 byte trie for this instruction
	regs *regState
}

// NFAMatch performs submatch extraction using an NFA.
// It selects the most efficient implementation based on the program characteristics.
func NFAMatch(prog *syntax.Prog, trieRoots [][]*utf8Node, b []byte, start, end int, numSubexp int) []int {
	if len(prog.Inst) <= 64 {
		if res := nfaMatchBitParallel(prog, b, start, end, numSubexp); res != nil {
			return res
		}
	}
	return nfaMatchPikeVM(prog, trieRoots, b, start, end, numSubexp)
}
