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
	visited := make([]int, maxPC)
	visitedGen := 1

	type workItem struct {
		pc       uint32
		node     *utf8Node
		regs     []int
		priority int
	}
	stack := make([]workItem, 0, 64)

	addThread := func(q *[]thread, pc uint32, node *utf8Node, regs []int, priority int, pos int, context syntax.EmptyOp) {
		stack = stack[:0]
		stack = append(stack, workItem{pc, node, regs, priority})

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
				*q = append(*q, thread{pc: item.pc, node: item.node, regs: item.regs, priority: item.priority})
				continue
			}

			inst := prog.Inst[item.pc]
			switch inst.Op {
			case syntax.InstNop:
				stack = append(stack, workItem{inst.Out, nil, item.regs, item.priority})
			case syntax.InstAlt, syntax.InstAltMatch:
				stack = append(stack, workItem{inst.Arg, nil, item.regs, item.priority + 1})
				stack = append(stack, workItem{inst.Out, nil, item.regs, item.priority})
			case syntax.InstCapture:
				if int(inst.Arg) < numRegs {
					newRegs := make([]int, numRegs)
					copy(newRegs, item.regs)
					newRegs[inst.Arg] = pos
					stack = append(stack, workItem{inst.Out, nil, newRegs, item.priority})
				} else {
					stack = append(stack, workItem{inst.Out, nil, item.regs, item.priority})
				}
			case syntax.InstEmptyWidth:
				if (syntax.EmptyOp(inst.Arg) & context) == syntax.EmptyOp(inst.Arg) {
					stack = append(stack, workItem{inst.Out, nil, item.regs, item.priority})
				}
			case syntax.InstFail:
				// do nothing
			case syntax.InstMatch:
				*q = append(*q, thread{pc: item.pc, node: nil, regs: item.regs, priority: item.priority})
			default:
				roots := trieRoots[item.pc]
				for _, root := range roots {
					*q = append(*q, thread{pc: item.pc, node: root, regs: item.regs, priority: item.priority})
				}
			}
		}
	}

	initialRegs := make([]int, numRegs)
	for i := range initialRegs {
		initialRegs[i] = -1
	}

	bestMatchRegs := make([]int, numRegs)
	for i := range bestMatchRegs {
		bestMatchRegs[i] = -1
	}
	bestPriority := 1 << 30
	foundMatch := false

	for pos := start; ; {
		// In Search mode, we allow a new match to start at every position.
		// We set regs[0] to the current position to track the start of the match.
		searchRegs := make([]int, numRegs)
		for i := range searchRegs {
			searchRegs[i] = -1
		}
		searchRegs[0] = pos
		// Priority ensures that matches starting earlier are preferred.
		addThread(&curr, uint32(prog.Start), nil, searchRegs, (pos-start)*SearchRestartPenalty, pos, CalculateContext(b, pos))

		if len(curr) == 0 && len(next) == 0 {
			break
		}

		// Check for matches at current position
		for _, t := range curr {
			if prog.Inst[t.pc].Op == syntax.InstMatch && t.node == nil {
				if t.priority <= bestPriority {
					bestPriority = t.priority
					foundMatch = true
					copy(bestMatchRegs, t.regs)
					if len(bestMatchRegs) >= 2 {
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
