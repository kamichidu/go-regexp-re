package ir

import (
	"github.com/kamichidu/go-regexp-re/syntax"
	"unicode/utf8"
)

// nfaMatchBitParallel performs submatch extraction using an NFA that leverages
// bitsets for fast membership tests. It maintains Pike VM's leftmost-first
// priority by processing threads in order.
func nfaMatchBitParallel(prog *syntax.Prog, b []byte, start, end int, numSubexp int) []int {
	numRegs := (numSubexp + 1) * 2
	numInst := len(prog.Inst)
	if numInst > 64 {
		return nil
	}

	type thread struct {
		pc   uint32
		regs []int
	}

	curr := make([]thread, 0, numInst)
	next := make([]thread, 0, numInst)

	var visited uint64

	var addThread func(q *[]thread, pc uint32, regs []int, pos int, context syntax.EmptyOp)
	addThread = func(q *[]thread, pc uint32, regs []int, pos int, context syntax.EmptyOp) {
		if (visited & (1 << pc)) != 0 {
			return
		}
		visited |= (1 << pc)

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
			*q = append(*q, thread{pc: pc, regs: regs})
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

		if pos == end {
			for _, t := range curr {
				if prog.Inst[t.pc].Op == syntax.InstMatch {
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

			visited = 0
			for _, t := range curr {
				inst := prog.Inst[t.pc]
				if inst.MatchRune(r) {
					addThread(&next, inst.Out, t.regs, pos+size, nextCtx)
				}
			}
			pos += size
		} else {
			break
		}

		curr, next = next, curr
		next = next[:0]
	}

	return nil
}
