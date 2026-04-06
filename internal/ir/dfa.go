package ir

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/kamichidu/go-regexp-re/syntax"
)

// StateID represents a unique identifier for a DFA state.
type StateID int32

const (
	// InvalidState represents a non-existent or error state.
	InvalidState StateID = -1
	// StartStateID is the ID of the initial state.
	StartStateID StateID = 0
)

// nfaState represents an NFA instruction and its current matching progress (Trie node).
type nfaState struct {
	id   uint32
	node *utf8Node // if nil, the instruction hasn't started yet.
}

// DFA represents a Deterministic Finite Automaton.
type DFA struct {
	transitions []StateID // Flattened table for better cache locality: table[state * stride + byte]
	stride      int       // 256 or 257
	numStates   int
	startState  StateID
	accepting   []bool
	hasAnchors  bool
	dfaToNfa    [][]nfaState // For post-processing context checks
}

const (
	// Virtual bytes for different anchor types.
	VirtualBeginLine = 256 + iota
	VirtualEndLine
	VirtualBeginText
	VirtualEndText
	VirtualWordBoundary
	VirtualNoWordBoundary
	numVirtualBytes = 6
)

func NewDFA(prog *syntax.Prog) (*DFA, error) {
	d := &DFA{}
	if err := d.build(prog); err != nil {
		return nil, fmt.Errorf("failed to build DFA: %w", err)
	}
	return d, nil
}

func (d *DFA) Next(current StateID, b int) StateID {
	if current < 0 || int(current) >= d.numStates || b < 0 || b >= d.stride {
		return InvalidState
	}
	return d.transitions[int(current)*d.stride+b]
}

func (d *DFA) IsAccepting(s StateID) bool {
	if s < 0 || int(s) >= d.numStates {
		return false
	}
	return d.accepting[s]
}

// StartState returns the state ID to use for the initial position.
func (d *DFA) StartState() StateID {
	return d.startState
}

// HasAnchors reports whether the DFA contains any anchor transitions.
func (d *DFA) HasAnchors() bool {
	return d.hasAnchors
}

// TotalStates returns the number of states in the DFA.
func (d *DFA) TotalStates() int {
	return d.numStates
}

func (d *DFA) build(prog *syntax.Prog) error {
	// Detect anchors
	d.hasAnchors = false
	for _, inst := range prog.Inst {
		if inst.Op == syntax.InstEmptyWidth {
			d.hasAnchors = true
			break
		}
	}

	if d.hasAnchors {
		d.stride = 256 + numVirtualBytes
	} else {
		d.stride = 256
	}

	trieCache := make(map[uint32][]*utf8Node)
	getTrie := func(id uint32) []*utf8Node {
		if roots, ok := trieCache[id]; ok {
			return roots
		}
		inst := prog.Inst[id]
		var roots []*utf8Node
		switch inst.Op {
		case syntax.InstRune, syntax.InstRune1:
			foldCase := inst.Op == syntax.InstRune && (inst.Arg&uint32(syntax.FoldCase) != 0)
			roots = runeRangesToUTF8Trie(inst.Rune, foldCase)
		case syntax.InstRuneAny:
			roots = runeRangesToUTF8Trie([]rune{0, 0x10FFFF}, false)
		case syntax.InstRuneAnyNotNL:
			roots = runeRangesToUTF8Trie([]rune{0, '\n' - 1, '\n' + 1, 0x10FFFF}, false)
		}
		trieCache[id] = roots
		return roots
	}

	nfaToDfa := make(map[string]StateID)
	dfaToNfa := make([][]nfaState, 0)

	addDfaState := func(set []nfaState) StateID {
		key := serializeSet(set)
		if id, ok := nfaToDfa[key]; ok {
			return id
		}

		id := StateID(len(dfaToNfa))
		nfaToDfa[key] = id
		dfaToNfa = append(dfaToNfa, set)
		d.dfaToNfa = append(d.dfaToNfa, set)

		// Determine if this state is an accepting state WITHOUT any extra context.
		isAccepting := false
		closure := epsilonClosure(set, prog, 0)
		for _, s := range closure {
			if prog.Inst[s.id].Op == syntax.InstMatch && s.node == nil {
				isAccepting = true
				break
			}
		}
		d.accepting = append(d.accepting, isAccepting)

		for i := 0; i < d.stride; i++ {
			d.transitions = append(d.transitions, InvalidState)
		}
		d.numStates++
		return id
	}

	// 1. Initial start state (no context)
	defaultStartClosure := epsilonClosure([]nfaState{{id: uint32(prog.Start)}}, prog, 0)
	d.startState = addDfaState(defaultStartClosure)

	// 2. Process all states
	for i := 0; i < len(dfaToNfa); i++ {
		currentSet := dfaToNfa[i]
		currentDfaID := StateID(i)

		// Byte transitions
		for b := 0; b < 256; b++ {
			var nextStates []nfaState
			for _, s := range currentSet {
				inst := prog.Inst[s.id]

				if s.node == nil {
					switch inst.Op {
					case syntax.InstRune, syntax.InstRune1, syntax.InstRuneAny, syntax.InstRuneAnyNotNL:
						roots := getTrie(s.id)
						fold := inst.Op == syntax.InstRune && (inst.Arg&uint32(syntax.FoldCase) != 0)
						for _, root := range roots {
							match := matchesByte
							if fold {
								match = matchesByteFold
							}
							if match(root, byte(b)) {
								if root.next == nil {
									nextStates = append(nextStates, nfaState{id: inst.Out})
								} else {
									for _, child := range root.next {
										nextStates = append(nextStates, nfaState{id: s.id, node: child})
									}
								}
							}
						}
					}
				} else {
					inst := prog.Inst[s.id]
					fold := inst.Op == syntax.InstRune && (inst.Arg&uint32(syntax.FoldCase) != 0)
					match := matchesByte
					if fold {
						match = matchesByteFold
					}
					if match(s.node, byte(b)) {
						if s.node.next == nil {
							nextStates = append(nextStates, nfaState{id: inst.Out})
						} else {
							for _, child := range s.node.next {
								nextStates = append(nextStates, nfaState{id: s.id, node: child})
							}
						}
					}
				}

			}
			if len(nextStates) > 0 {
				nextClosure := epsilonClosure(nextStates, prog, 0)
				nextDfaID := addDfaState(nextClosure)
				d.transitions[int(currentDfaID)*d.stride+b] = nextDfaID
			}
		}

		// Virtual byte transitions for anchors
		if d.hasAnchors {
			for bit := 0; bit < numVirtualBytes; bit++ {
				op := syntax.EmptyOp(1 << bit)
				nextClosure := epsilonClosure(currentSet, prog, op)
				if serializeSet(nextClosure) != serializeSet(currentSet) {
					nextDfaID := addDfaState(nextClosure)
					d.transitions[int(currentDfaID)*d.stride+256+bit] = nextDfaID
				}
			}
		}
	}
	return nil
}

// IsAcceptingWithContext checks if a state is accepting given the current empty-width context.
func (d *DFA) IsAcceptingWithContext(s StateID, prog *syntax.Prog, context syntax.EmptyOp) bool {
	if s < 0 || int(s) >= d.numStates {
		return false
	}
	set := d.dfaToNfa[s]
	closure := epsilonClosure(set, prog, context)
	for _, ns := range closure {
		if prog.Inst[ns.id].Op == syntax.InstMatch && ns.node == nil {
			return true
		}
	}
	return false
}

func matchesByte(node *utf8Node, b byte) bool {
	for _, r := range node.ranges {
		if b >= r.lo && b <= r.hi {
			return true
		}
	}
	return false
}

func matchesByteFold(node *utf8Node, b byte) bool {
	// For now, since we've expanded the trie in runeRangesToUTF8Trie,
	// this is functionally the same as matchesByte.
	return matchesByte(node, b)
}

func epsilonClosure(states []nfaState, prog *syntax.Prog, context syntax.EmptyOp) []nfaState {
	type key struct {
		id   uint32
		node *utf8Node
	}
	seen := make(map[key]bool)
	stack := append([]nfaState{}, states...)
	var result []nfaState

	for len(stack) > 0 {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		k := key{s.id, s.node}
		if seen[k] {
			continue
		}
		seen[k] = true

		if s.node == nil {
			inst := prog.Inst[s.id]
			switch inst.Op {
			case syntax.InstAlt, syntax.InstAltMatch, syntax.InstCapture, syntax.InstNop:
				stack = append(stack, nfaState{id: inst.Out})
				if inst.Op == syntax.InstAlt || inst.Op == syntax.InstAltMatch {
					stack = append(stack, nfaState{id: inst.Arg})
				}
				continue
			case syntax.InstEmptyWidth:
				if syntax.EmptyOp(inst.Arg)&context == syntax.EmptyOp(inst.Arg) {
					stack = append(stack, nfaState{id: inst.Out})
					continue
				}
				// If it doesn't match now, keep it in the set to re-evaluate later.
			}
		}
		result = append(result, s)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].id != result[j].id {
			return result[i].id < result[j].id
		}
		return fmt.Sprintf("%p", result[i].node) < fmt.Sprintf("%p", result[j].node)
	})
	return result
}

func serializeSet(set []nfaState) string {
	var sb strings.Builder
	for i, s := range set {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.FormatUint(uint64(s.id), 10))
		if s.node != nil {
			sb.WriteByte(':')
			sb.WriteString(fmt.Sprintf("%p", s.node))
		}
	}
	return sb.String()
}
