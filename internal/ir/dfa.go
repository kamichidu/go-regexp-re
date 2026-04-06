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

// TagOp represents a capture group register index and whether it's after the byte.
type TagOp uint32

func (t TagOp) Index() int {
	return int(t >> 1)
}

func (t TagOp) After() bool {
	return (t & 1) != 0
}

func MakeTagOp(index int, after bool) TagOp {
	if after {
		return TagOp(index<<1 | 1)
	}
	return TagOp(index << 1)
}

// nfaPath represents an NFA state reached with associated tags.
type nfaPath struct {
	nfaState
	tags []int // Just indices
}

// DFA represents a Deterministic Finite Automaton.
type DFA struct {
	transitions   []StateID // Flattened table for better cache locality: table[state * stride + byte]
	tagOffsets    []uint32  // tagPool[tagOffsets[i]:tagOffsets[i+1]] are tags for transition i
	tagPool       []TagOp
	stride        int // 256 or 257
	numStates     int
	startState    StateID
	entryTags     []TagOp
	accepting     []bool
	hasAnchors    bool
	dfaToNfa      [][]nfaPath // Carry paths with tags
	stateBestTags [][]int     // Best tags (indices) for each state
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

func (d *DFA) Tags(current StateID, b int) []TagOp {
	if current < 0 || int(current) >= d.numStates || b < 0 || b >= d.stride || len(d.tagOffsets) == 0 {
		return nil
	}
	idx := int(current)*d.stride + b
	return d.tagPool[d.tagOffsets[idx]:d.tagOffsets[idx+1]]
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

// EntryTags returns the tags to be applied at the start of matching.
func (d *DFA) EntryTags() []TagOp {
	return d.entryTags
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
	dfaToNfa := make([][]nfaPath, 0)
	allTags := make([][]TagOp, 0)

	addDfaState := func(closure []nfaPath, bestTags []int) StateID {
		sorted := make([]nfaPath, len(closure))
		copy(sorted, closure)
		sort.Slice(sorted, func(i, j int) bool {
			if sorted[i].id != sorted[j].id {
				return sorted[i].id < sorted[j].id
			}
			return fmt.Sprintf("%p", sorted[i].node) < fmt.Sprintf("%p", sorted[j].node)
		})

		key := serializeSet(sorted)
		if id, ok := nfaToDfa[key]; ok {
			return id
		}

		id := StateID(len(dfaToNfa))
		nfaToDfa[key] = id
		dfaToNfa = append(dfaToNfa, closure)
		d.dfaToNfa = append(d.dfaToNfa, closure)
		d.stateBestTags = append(d.stateBestTags, bestTags)

		// Determine if this state is an accepting state WITHOUT any extra context.
		isAccepting := false
		for _, s := range closure {
			if prog.Inst[s.id].Op == syntax.InstMatch && s.node == nil {
				isAccepting = true
				break
			}
		}
		d.accepting = append(d.accepting, isAccepting)

		for i := 0; i < d.stride; i++ {
			d.transitions = append(d.transitions, InvalidState)
			allTags = append(allTags, nil)
		}
		d.numStates++
		return id
	}

	// 1. Initial start state (no context)
	defaultStartClosure, entryIndices := epsilonClosure([]nfaPath{{nfaState: nfaState{id: uint32(prog.Start)}}}, prog, 0)
	d.startState = addDfaState(defaultStartClosure, entryIndices)
	d.entryTags = make([]TagOp, len(entryIndices))
	for i, idx := range entryIndices {
		d.entryTags[i] = MakeTagOp(idx, false)
	}

	// 2. Process all states
	for i := 0; i < len(dfaToNfa); i++ {
		currentClosure := dfaToNfa[i]
		currentBestTags := d.stateBestTags[i]
		currentDfaID := StateID(i)

		// Byte transitions
		for b := 0; b < 256; b++ {
			var nextPaths []nfaPath
			var bestBefore []int
			foundMatch := false

			for _, p := range currentClosure {
				s := p.nfaState
				inst := prog.Inst[s.id]

				var matchedOut []uint32
				var matchedNodes []*utf8Node

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
									matchedOut = append(matchedOut, inst.Out)
								} else {
									for _, child := range root.next {
										matchedNodes = append(matchedNodes, child)
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
							matchedOut = append(matchedOut, inst.Out)
						} else {
							for _, child := range s.node.next {
								matchedNodes = append(matchedNodes, child)
							}
						}
					}
				}

				if len(matchedOut) > 0 || len(matchedNodes) > 0 {
					if !foundMatch {
						bestBefore = p.tags
					}
					for _, out := range matchedOut {
						nextPaths = append(nextPaths, nfaPath{nfaState: nfaState{id: out}})
					}
					for _, node := range matchedNodes {
						nextPaths = append(nextPaths, nfaPath{nfaState: nfaState{id: s.id, node: node}})
					}
					foundMatch = true
				}
			}

			if foundMatch {
				nextClosure, tagsAfterIndices := epsilonClosure(nextPaths, prog, 0)
				nextDfaID := addDfaState(nextClosure, tagsAfterIndices)
				idx := int(currentDfaID)*d.stride + b
				d.transitions[idx] = nextDfaID

				// diffBefore = bestBefore - currentBestTags
				diffBefore := diffTags(bestBefore, currentBestTags)
				edgeTags := make([]TagOp, 0, len(diffBefore)+len(tagsAfterIndices))
				for _, t := range diffBefore {
					edgeTags = append(edgeTags, MakeTagOp(t, false))
				}
				for _, t := range tagsAfterIndices {
					edgeTags = append(edgeTags, MakeTagOp(t, true))
				}
				allTags[idx] = edgeTags
			}
		}

		// Virtual byte transitions for anchors
		if d.hasAnchors {
			for bit := 0; bit < numVirtualBytes; bit++ {
				op := syntax.EmptyOp(1 << bit)
				nextClosure, tagsIndices := epsilonClosure(currentClosure, prog, op)

				if serializeSet(nextClosure) != serializeSet(currentClosure) {
					nextDfaID := addDfaState(nextClosure, tagsIndices)
					idx := int(currentDfaID)*d.stride + 256 + bit
					d.transitions[idx] = nextDfaID

					diff := diffTags(tagsIndices, currentBestTags)
					edgeTags := make([]TagOp, len(diff))
					for k, t := range diff {
						edgeTags[k] = MakeTagOp(t, false) // virtual is same position
					}
					allTags[idx] = edgeTags
				}
			}
		}
	}

	// Flatten tags
	d.tagOffsets = make([]uint32, len(allTags)+1)
	for i, tags := range allTags {
		d.tagOffsets[i] = uint32(len(d.tagPool))
		d.tagPool = append(d.tagPool, tags...)
	}
	d.tagOffsets[len(allTags)] = uint32(len(d.tagPool))

	return nil
}

func diffTags(next, curr []int) []int {
	i := 0
	for i < len(next) && i < len(curr) && next[i] == curr[i] {
		i++
	}
	return next[i:]
}

// IsAcceptingWithContext checks if a state is accepting given the current empty-width context.
func (d *DFA) IsAcceptingWithContext(s StateID, prog *syntax.Prog, context syntax.EmptyOp) bool {
	if s < 0 || int(s) >= d.numStates {
		return false
	}
	set := d.dfaToNfa[s]
	closure, _ := epsilonClosure(set, prog, context)
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
	return matchesByte(node, b)
}

func epsilonClosure(paths []nfaPath, prog *syntax.Prog, context syntax.EmptyOp) ([]nfaPath, []int) {
	type key struct {
		id   uint32
		node *utf8Node
	}
	seen := make(map[key]bool)
	stack := make([]nfaPath, 0, len(paths))
	for i := len(paths) - 1; i >= 0; i-- {
		stack = append(stack, paths[i])
	}
	var result []nfaPath

	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		k := key{p.id, p.node}
		if seen[k] {
			continue
		}
		seen[k] = true

		if p.node == nil {
			inst := prog.Inst[p.id]
			switch inst.Op {
			case syntax.InstAlt, syntax.InstAltMatch:
				stack = append(stack, nfaPath{nfaState: nfaState{id: inst.Arg}, tags: p.tags})
				stack = append(stack, nfaPath{nfaState: nfaState{id: inst.Out}, tags: p.tags})
				continue
			case syntax.InstCapture:
				newTags := make([]int, len(p.tags)+1)
				copy(newTags, p.tags)
				newTags[len(p.tags)] = int(inst.Arg)
				stack = append(stack, nfaPath{nfaState: nfaState{id: inst.Out}, tags: newTags})
				continue
			case syntax.InstNop:
				stack = append(stack, nfaPath{nfaState: nfaState{id: inst.Out}, tags: p.tags})
				continue
			case syntax.InstEmptyWidth:
				if syntax.EmptyOp(inst.Arg)&context == syntax.EmptyOp(inst.Arg) {
					stack = append(stack, nfaPath{nfaState: nfaState{id: inst.Out}, tags: p.tags})
					continue
				}
			}
		}
		result = append(result, p)
	}

	var bestTags []int
	if len(result) > 0 {
		bestTags = result[0].tags
	}

	return result, bestTags
}

func serializeSet(set []nfaPath) string {
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
