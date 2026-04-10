package ir

import (
	"github.com/kamichidu/go-regexp-re/syntax"
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

	curr := make([]thread, 0, numInst)
	next := make([]thread, 0, numInst)

	type visitedKey struct {
		pc   uint32
		node *utf8Node
	}
	visited := make(map[visitedKey]bool)

	var addThread func(q *[]thread, pc uint32, node *utf8Node, regs []int, priority int, pos int, context syntax.EmptyOp)
	addThread = func(q *[]thread, pc uint32, node *utf8Node, regs []int, priority int, pos int, context syntax.EmptyOp) {
		key := visitedKey{pc, node}
		if visited[key] {
			return
		}
		visited[key] = true

		if node != nil {
			*q = append(*q, thread{pc: pc, node: node, regs: regs, priority: priority})
			return
		}

		inst := prog.Inst[pc]
		switch inst.Op {
		case syntax.InstNop:
			addThread(q, inst.Out, nil, regs, priority, pos, context)
		case syntax.InstAlt, syntax.InstAltMatch:
			addThread(q, inst.Out, nil, regs, priority*2, pos, context)
			addThread(q, inst.Arg, nil, regs, priority*2+1, pos, context)
		case syntax.InstCapture:
			if int(inst.Arg) < numRegs {
				newRegs := make([]int, numRegs)
				copy(newRegs, regs)
				newRegs[inst.Arg] = pos
				addThread(q, inst.Out, nil, newRegs, priority, pos, context)
			} else {
				addThread(q, inst.Out, nil, regs, priority, pos, context)
			}
		case syntax.InstEmptyWidth:
			if (syntax.EmptyOp(inst.Arg) & context) == syntax.EmptyOp(inst.Arg) {
				addThread(q, inst.Out, nil, regs, priority, pos, context)
			}
		case syntax.InstFail:
			// do nothing
		case syntax.InstMatch:
			*q = append(*q, thread{pc: pc, node: nil, regs: regs, priority: priority})
		default:
			var roots []*utf8Node
			switch inst.Op {
			case syntax.InstRune, syntax.InstRune1:
				foldCase := inst.Op == syntax.InstRune && (inst.Arg&uint32(syntax.FoldCase) != 0)
				roots = runeRangesToUTF8Trie(inst.Rune, foldCase)
			case syntax.InstRuneAny:
				roots = anyRuneTrie(true)
			case syntax.InstRuneAnyNotNL:
				roots = anyRuneTrie(false)
			}
			for _, root := range roots {
				addThread(q, pc, root, regs, priority, pos, context)
			}
		}
	}

	initialRegs := make([]int, numRegs)
	for i := range initialRegs {
		initialRegs[i] = -1
	}

	ctx := CalculateContext(b, start)
	addThread(&curr, uint32(prog.Start), nil, initialRegs, 0, start, ctx)

	bestMatchRegs := make([]int, numRegs)
	for i := range bestMatchRegs {
		bestMatchRegs[i] = -1
	}
	bestPriority := 1 << 30
	foundMatch := false

	for pos := start; ; {
		if len(curr) == 0 && len(next) == 0 {
			break
		}

		for _, t := range curr {
			if prog.Inst[t.pc].Op == syntax.InstMatch && t.node == nil {
				if t.priority <= bestPriority {
					bestPriority = t.priority
					foundMatch = true
					copy(bestMatchRegs, t.regs)
					if len(bestMatchRegs) >= 2 {
						bestMatchRegs[0] = start
						bestMatchRegs[1] = pos
					}
				}
			}
		}

		if pos >= end {
			break
		}

		c := b[pos]
		nextCtx := CalculateContext(b, pos+1)
		clear(visited)

		for _, t := range curr {
			if t.node == nil {
				continue
			}

			inst := prog.Inst[t.pc]
			fold := inst.Op == syntax.InstRune && (inst.Arg&uint32(syntax.FoldCase) != 0)
			match := matchesByte
			if fold {
				match = matchesByteFold
			}

			if match(t.node, c) {
				if t.node.next == nil {
					addThread(&next, inst.Out, nil, t.regs, t.priority, pos+1, nextCtx)
				} else {
					for _, child := range t.node.next {
						addThread(&next, t.pc, child, t.regs, t.priority, pos+1, nextCtx)
					}
				}
			}
		}
		pos++

		curr, next = next, curr
		next = next[:0]
	}

	if !foundMatch {
		return nil
	}
	return bestMatchRegs
}
