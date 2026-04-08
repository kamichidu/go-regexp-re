package ir

import (
	"github.com/kamichidu/go-regexp-re/syntax"
)

// nfaMatchPikeVM performs submatch extraction using an NFA (Pike VM).
// It's intended to be used as a second pass after the DFA has identified the match range [start, end].
func nfaMatchPikeVM(prog *syntax.Prog, b []byte, start, end int, numSubexp int) []int {
	numRegs := (numSubexp + 1) * 2

	curr := make([]thread, 0, 64)
	next := make([]thread, 0, 64)

	// visited keeps track of (PC, node) already added to the thread list for the current position.
	type visitedKey struct {
		pc   uint32
		node *utf8Node
	}
	visited := make(map[visitedKey]bool)

	var addThread func(q *[]thread, pc uint32, node *utf8Node, regs []int, pos int, context syntax.EmptyOp)
	addThread = func(q *[]thread, pc uint32, node *utf8Node, regs []int, pos int, context syntax.EmptyOp) {
		key := visitedKey{pc, node}
		if visited[key] {
			return
		}
		visited[key] = true

		if node != nil {
			*q = append(*q, thread{pc: pc, node: node, regs: regs})
			return
		}

		inst := prog.Inst[pc]
		switch inst.Op {
		case syntax.InstNop:
			addThread(q, inst.Out, nil, regs, pos, context)
		case syntax.InstAlt, syntax.InstAltMatch:
			addThread(q, inst.Out, nil, regs, pos, context)
			addThread(q, inst.Arg, nil, regs, pos, context)
		case syntax.InstCapture:
			if int(inst.Arg) < numRegs {
				newRegs := make([]int, numRegs)
				copy(newRegs, regs)
				newRegs[inst.Arg] = pos
				addThread(q, inst.Out, nil, newRegs, pos, context)
			} else {
				addThread(q, inst.Out, nil, regs, pos, context)
			}
		case syntax.InstEmptyWidth:
			if (syntax.EmptyOp(inst.Arg) & context) == syntax.EmptyOp(inst.Arg) {
				addThread(q, inst.Out, nil, regs, pos, context)
			}
		case syntax.InstFail:
			// do nothing
		case syntax.InstMatch:
			*q = append(*q, thread{pc: pc, node: nil, regs: regs})
		default:
			// Rune instructions: start with the trie roots
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
				*q = append(*q, thread{pc: pc, node: root, regs: regs})
			}
		}
	}

	initialRegs := make([]int, numRegs)
	for i := range initialRegs {
		initialRegs[i] = -1
	}

	ctx := CalculateContext(b, start)
	addThread(&curr, uint32(prog.Start), nil, initialRegs, start, ctx)

	for pos := start; ; {
		if len(curr) == 0 {
			break
		}

		if pos == end {
			for _, t := range curr {
				if prog.Inst[t.pc].Op == syntax.InstMatch && t.node == nil {
					if len(t.regs) >= 2 {
						t.regs[0] = start
						t.regs[1] = end
					}
					return t.regs
				}
			}
		}

		if pos < end {
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
						// Character completed
						addThread(&next, inst.Out, nil, t.regs, pos+1, nextCtx)
					} else {
						// Continue through the trie
						for _, child := range t.node.next {
							addThread(&next, t.pc, child, t.regs, pos+1, nextCtx)
						}
					}
				}
			}
			pos++
		} else {
			break
		}

		curr, next = next, curr
		next = next[:0]
	}

	return nil
}
