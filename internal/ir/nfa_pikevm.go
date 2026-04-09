package ir

import (
	"sync"

	"github.com/kamichidu/go-regexp-re/syntax"
)

type pikeVM struct {
	prog       *syntax.Prog
	trieRoots  [][]*utf8Node
	numRegs    int
	visited    []int
	visitedGen int
	stack      []workItem
	regsPool   *sync.Pool
}

type workItem struct {
	pc   uint32
	node *utf8Node
	regs *regState
}

func (m *pikeVM) allocRegs() *regState {
	rs := m.regsPool.Get().(*regState)
	for i := range rs.slots {
		rs.slots[i] = -1
	}
	rs.refs = 1
	return rs
}

func (m *pikeVM) copyRegs(old *regState) *regState {
	rs := m.regsPool.Get().(*regState)
	copy(rs.slots, old.slots)
	rs.refs = 1
	return rs
}

func (m *pikeVM) freeRegs(rs *regState) {
	if rs == nil {
		return
	}
	rs.refs--
	if rs.refs == 0 {
		m.regsPool.Put(rs)
	}
}

func (m *pikeVM) addThread(q *[]thread, pc uint32, node *utf8Node, regs *regState, pos int, context syntax.EmptyOp) {
	m.stack = m.stack[:0]
	m.stack = append(m.stack, workItem{pc, node, regs})
	regs.refs++

	for len(m.stack) > 0 {
		item := m.stack[len(m.stack)-1]
		m.stack = m.stack[:len(m.stack)-1]

		if item.node == nil {
			if m.visited[item.pc] == m.visitedGen {
				m.freeRegs(item.regs)
				continue
			}
			m.visited[item.pc] = m.visitedGen
		}

		if item.node != nil {
			*q = append(*q, thread{pc: item.pc, node: item.node, regs: item.regs})
			continue
		}

		inst := m.prog.Inst[item.pc]
		switch inst.Op {
		case syntax.InstNop:
			m.stack = append(m.stack, workItem{inst.Out, nil, item.regs})
		case syntax.InstAlt, syntax.InstAltMatch:
			item.regs.refs++
			m.stack = append(m.stack, workItem{inst.Arg, nil, item.regs})
			m.stack = append(m.stack, workItem{inst.Out, nil, item.regs})
		case syntax.InstCapture:
			if int(inst.Arg) < m.numRegs {
				var newRegs *regState
				if item.regs.refs == 1 {
					newRegs = item.regs
				} else {
					newRegs = m.copyRegs(item.regs)
					m.freeRegs(item.regs)
				}
				newRegs.slots[inst.Arg] = pos
				m.stack = append(m.stack, workItem{inst.Out, nil, newRegs})
			} else {
				m.stack = append(m.stack, workItem{inst.Out, nil, item.regs})
			}
		case syntax.InstEmptyWidth:
			if (syntax.EmptyOp(inst.Arg) & context) == syntax.EmptyOp(inst.Arg) {
				m.stack = append(m.stack, workItem{inst.Out, nil, item.regs})
			} else {
				m.freeRegs(item.regs)
			}
		case syntax.InstFail:
			m.freeRegs(item.regs)
		case syntax.InstMatch:
			*q = append(*q, thread{pc: item.pc, node: nil, regs: item.regs})
		default:
			roots := m.trieRoots[item.pc]
			for i, root := range roots {
				if i < len(roots)-1 {
					item.regs.refs++
				}
				*q = append(*q, thread{pc: item.pc, node: root, regs: item.regs})
			}
			if len(roots) == 0 {
				m.freeRegs(item.regs)
			}
		}
	}
}

func nfaMatchPikeVM(prog *syntax.Prog, trieRoots [][]*utf8Node, b []byte, start, end int, numSubexp int) []int {
	numRegs := (numSubexp + 1) * 2

	m := &pikeVM{
		prog:       prog,
		trieRoots:  trieRoots,
		numRegs:    numRegs,
		visited:    make([]int, len(prog.Inst)),
		visitedGen: 1,
		stack:      make([]workItem, 0, 64),
		regsPool: &sync.Pool{
			New: func() any {
				return &regState{slots: make([]int, numRegs)}
			},
		},
	}

	curr := make([]thread, 0, 64)
	next := make([]thread, 0, 64)

	initialRegs := m.allocRegs()

	ctx := CalculateContext(b, start)
	m.addThread(&curr, uint32(prog.Start), nil, initialRegs, start, ctx)
	m.freeRegs(initialRegs) // Handed over to addThread

	var result []int

	for pos := start; ; {
		if len(curr) == 0 {
			break
		}

		if pos == end {
			for _, t := range curr {
				if prog.Inst[t.pc].Op == syntax.InstMatch && t.node == nil {
					result = make([]int, numRegs)
					copy(result, t.regs.slots)
					result[0] = start
					result[1] = end
					goto found
				}
			}
		}

		if pos < end {
			c := b[pos]
			nextCtx := CalculateContext(b, pos+1)
			m.visitedGen++

			for i := range curr {
				t := &curr[i]
				if t.node == nil {
					m.freeRegs(t.regs)
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
						t.regs.refs++
						m.addThread(&next, inst.Out, nil, t.regs, pos+1, nextCtx)
					} else {
						// Continue through the trie
						for _, child := range t.node.next {
							t.regs.refs++
							m.addThread(&next, t.pc, child, t.regs, pos+1, nextCtx)
						}
					}
				}
				m.freeRegs(t.regs)
			}
			pos++
		} else {
			for i := range curr {
				m.freeRegs(curr[i].regs)
			}
			curr = curr[:0]
			break
		}

		curr = curr[:0]
		curr, next = next, curr
	}

found:
	for _, t := range curr {
		m.freeRegs(t.regs)
	}
	for _, t := range next {
		m.freeRegs(t.regs)
	}
	return result
}
