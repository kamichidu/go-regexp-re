package ir

import (
	"github.com/kamichidu/go-regexp-re/syntax"
	"unicode/utf8"
)

type thread struct {
	pc   uint32
	regs []int
}

// NFAMatch performs submatch extraction using an NFA (Pike VM).
// It's intended to be used as a second pass after the DFA has identified the match range [start, end].
func NFAMatch(prog *syntax.Prog, b []byte, start, end int, numSubexp int) []int {
	numRegs := (numSubexp + 1) * 2

	curr := make([]*thread, 0, 64)
	next := make([]*thread, 0, 64)

	// visited keeps track of PCs already added to the thread list for the current position.
	visited := make([]int, len(prog.Inst))
	for i := range visited {
		visited[i] = -1
	}

	var addThread func(q *[]*thread, pc uint32, regs []int, pos int, context syntax.EmptyOp)
	addThread = func(q *[]*thread, pc uint32, regs []int, pos int, context syntax.EmptyOp) {
		if visited[pc] == pos {
			return
		}
		visited[pc] = pos

		inst := prog.Inst[pc]
		switch inst.Op {
		case syntax.InstNop:
			addThread(q, inst.Out, regs, pos, context)
		case syntax.InstAlt, syntax.InstAltMatch:
			addThread(q, inst.Out, regs, pos, context)
			addThread(q, inst.Arg, regs, pos, context)
		case syntax.InstCapture:
			if int(inst.Arg) < numRegs {
				newRegs := make([]int, numRegs)
				copy(newRegs, regs)
				newRegs[inst.Arg] = pos
				addThread(q, inst.Out, newRegs, pos, context)
			} else {
				addThread(q, inst.Out, regs, pos, context)
			}
		case syntax.InstEmptyWidth:
			if (syntax.EmptyOp(inst.Arg) & context) == syntax.EmptyOp(inst.Arg) {
				addThread(q, inst.Out, regs, pos, context)
			}
		case syntax.InstFail:
			// do nothing
		default:
			*q = append(*q, &thread{pc: pc, regs: regs})
		}
	}

	initialRegs := make([]int, numRegs)
	for i := range initialRegs {
		initialRegs[i] = -1
	}

	ctx := calculateContext(b, start)
	addThread(&curr, uint32(prog.Start), initialRegs, start, ctx)

	for pos := start; ; {
		if len(curr) == 0 {
			break
		}

		// If we've reached a Match instruction at this position, and it's our target end,
		// the first one we see is the best (leftmost-first).
		if pos == end {
			for _, t := range curr {
				if prog.Inst[t.pc].Op == syntax.InstMatch {
					// Ensure capture 0 is set to the overall match range.
					// Sometimes gosyntax.Compile omits these from the instructions.
					if len(t.regs) >= 2 {
						t.regs[0] = start
						t.regs[1] = end
					}
					return t.regs
				}
			}
		}

		if pos < end {
			r, size := utf8.DecodeRune(b[pos:end])
			nextCtx := calculateContext(b, pos+size)
			for _, t := range curr {
				inst := prog.Inst[t.pc]
				if inst.MatchRune(r) {
					addThread(&next, inst.Out, t.regs, pos+size, nextCtx)
				}
			}
			pos += size
		} else {
			// pos == end but no Match instruction yet.
			break
		}

		curr, next = next, curr
		next = next[:0]
	}

	return nil
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
