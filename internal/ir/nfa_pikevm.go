package ir

import (
	"github.com/kamichidu/go-regexp-re/syntax"
)

// nfaMatchPikeVM performs submatch extraction using an NFA (Pike VM).
// It's intended to be used as a second pass after the DFA has identified the match range [start, end].
func nfaMatchPikeVM(prog *syntax.Prog, trieRoots [][]*utf8Node, b []byte, start, end int, numSubexp int) []int {
	numRegs := (numSubexp + 1) * 2

	curr := make([]thread, 0, 64)
	next := make([]thread, 0, 64)

	maxPC := len(prog.Inst)
	// Track visited per PC and per trie node ID.
	// Since nodeID is small and node matching is infrequent compared to epsilon steps,
	// we use a simplified visited check.
	visited := make([]int, maxPC)
	visitedGen := 1

	type workItem struct {
		pc   uint32
		node *utf8Node
		regs []int
	}
	stack := make([]workItem, 0, 64)

	addThread := func(q *[]thread, pc uint32, node *utf8Node, regs []int, pos int, context syntax.EmptyOp) {
		stack = stack[:0]
		stack = append(stack, workItem{pc, node, regs})

		for len(stack) > 0 {
			item := stack[len(stack)-1]
			stack = stack[:len(stack)-1]

			if item.node == nil {
				if visited[item.pc] == visitedGen {
					continue
				}
				visited[item.pc] = visitedGen
			}

			if item.node != nil {
				*q = append(*q, thread{pc: item.pc, node: item.node, regs: item.regs})
				continue
			}

			inst := prog.Inst[item.pc]
			switch inst.Op {
			case syntax.InstNop:
				stack = append(stack, workItem{inst.Out, nil, item.regs})
			case syntax.InstAlt, syntax.InstAltMatch:
				stack = append(stack, workItem{inst.Arg, nil, item.regs})
				stack = append(stack, workItem{inst.Out, nil, item.regs})
			case syntax.InstCapture:
				if int(inst.Arg) < numRegs {
					newRegs := make([]int, numRegs)
					copy(newRegs, item.regs)
					newRegs[inst.Arg] = pos
					stack = append(stack, workItem{inst.Out, nil, newRegs})
				} else {
					stack = append(stack, workItem{inst.Out, nil, item.regs})
				}
			case syntax.InstEmptyWidth:
				if (syntax.EmptyOp(inst.Arg) & context) == syntax.EmptyOp(inst.Arg) {
					stack = append(stack, workItem{inst.Out, nil, item.regs})
				}
			case syntax.InstFail:
				// do nothing
			case syntax.InstMatch:
				*q = append(*q, thread{pc: item.pc, node: nil, regs: item.regs})
			default:
				roots := trieRoots[item.pc]
				for _, root := range roots {
					*q = append(*q, thread{pc: item.pc, node: root, regs: item.regs})
				}
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
			visitedGen++

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
