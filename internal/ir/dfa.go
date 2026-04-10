package ir

import (
	"context"
	"fmt"
	"sort"
	"unsafe"

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
	ID   uint32
	node *utf8Node // if nil, the instruction hasn't started yet.
}

// nfaPath represents an NFA state reached with associated priority.
type nfaPath struct {
	nfaState
	Priority int
}

// DFA represents a Deterministic Finite Automaton.
type DFA struct {
	transitions            []StateID // Flattened table for better cache locality: table[state * stride + byte]
	transPriorityIncrement []int32   // Priority shift for each transition
	stride                 int       // 256 or 257
	numStates              int
	startState             StateID
	hasAnchors             bool
	numSubexp              int

	// Cached trie roots for each instruction
	trieRoots [][]*utf8Node

	// Accepting info
	accepting          []bool
	stateMatchPriority []int
	stateIsBestMatch   []bool
}

const (
	// Virtual bytes for different anchor types.
	// Order MUST match gosyntax.EmptyOp bits for applyContextToState.
	VirtualBeginLine      = 256 + iota // bit 0 (1)
	VirtualEndLine                     // bit 1 (2)
	VirtualBeginText                   // bit 2 (4)
	VirtualEndText                     // bit 3 (8)
	VirtualWordBoundary                // bit 4 (16)
	VirtualNoWordBoundary              // bit 5 (32)
	numVirtualBytes       = 6
)

func NewDFA(prog *syntax.Prog) (*DFA, error) {
	return NewDFAForSearch(context.Background(), prog)
}

func NewDFAForSearch(ctx context.Context, prog *syntax.Prog) (*DFA, error) {
	d := &DFA{
		numSubexp: prog.NumCap / 2,
	}
	if err := d.build(ctx, prog, true); err != nil {
		return nil, fmt.Errorf("failed to build DFA: %w", err)
	}
	return d, nil
}

func NewDFAForMatch(ctx context.Context, prog *syntax.Prog) (*DFA, error) {
	d := &DFA{
		numSubexp: prog.NumCap / 2,
	}
	if err := d.build(ctx, prog, false); err != nil {
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

func (d *DFA) Transitions() []StateID {
	return d.transitions
}

func (d *DFA) PriorityIncrements() []int32 {
	return d.transPriorityIncrement
}

func (d *DFA) Stride() int {
	return d.stride
}

func (d *DFA) Accepting() []bool {
	return d.accepting
}

func (d *DFA) IsAccepting(s StateID) bool {
	if s < 0 || int(s) >= d.numStates {
		return false
	}
	return d.accepting[s]
}

func (d *DFA) IsBestMatch(s StateID) bool {
	if s < 0 || int(s) >= d.numStates {
		return false
	}
	return d.stateIsBestMatch[s]
}

func (d *DFA) StartState() StateID {
	return d.startState
}

func (d *DFA) HasAnchors() bool {
	return d.hasAnchors
}

func (d *DFA) TotalStates() int {
	return d.numStates
}

func (d *DFA) TrieRoots() [][]*utf8Node {
	return d.trieRoots
}

func (d *DFA) AcceptingPriority(s StateID) int {
	if s < 0 || int(s) >= d.numStates {
		return 1<<30 - 1
	}
	return d.stateMatchPriority[s]
}

var ErrStateExplosion = fmt.Errorf("regexp: pattern too large or ambiguous")

// MaxDFAMemory is the maximum estimated memory for the DFA transition table.
// Default is 64MB, which corresponds to roughly 32,000 states.
const MaxDFAMemory = 64 * 1024 * 1024

func (d *DFA) build(ctx context.Context, prog *syntax.Prog, withSearch bool) error {
	cache := newUTF8NodeCache()

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

	d.trieRoots = make([][]*utf8Node, len(prog.Inst))
	getTrie := func(ID uint32) []*utf8Node {
		if roots := d.trieRoots[ID]; roots != nil {
			return roots
		}
		inst := prog.Inst[ID]
		var roots []*utf8Node
		switch inst.Op {
		case syntax.InstRune, syntax.InstRune1:
			foldCase := inst.Op == syntax.InstRune && (inst.Arg&uint32(syntax.FoldCase) != 0)
			roots = cache.runeRangesToUTF8Trie(inst.Rune, foldCase)
		case syntax.InstRuneAny:
			roots = cache.anyRuneTrie(true)
		case syntax.InstRuneAnyNotNL:
			roots = cache.anyRuneTrie(false)
		}
		d.trieRoots[ID] = roots
		return roots
	}

	nfaToDfa := make(map[string]StateID)
	dfaToNfa := make([][]nfaPath, 0)

	var errBuild error
	addDfaState := func(closure []nfaPath) StateID {
		if errBuild != nil {
			return InvalidState
		}

		// Estimate memory: (numStates+1) * stride * 8 bytes (transitions + increments)
		if (d.numStates+1)*d.stride*8 > MaxDFAMemory {
			errBuild = ErrStateExplosion
			return InvalidState
		}

		// Priority Normalization: Keep relative order to avoid state explosion
		// during search/matching when priorities increment.
		if len(closure) > 0 {
			minP := closure[0].Priority
			for i := 1; i < len(closure); i++ {
				if closure[i].Priority < minP {
					minP = closure[i].Priority
				}
			}
			if minP > 0 {
				for i := range closure {
					closure[i].Priority -= minP
				}
			}
		}

		// Canonical sorting for state key
		sort.Slice(closure, func(i, j int) bool {
			if closure[i].ID != closure[j].ID {
				return closure[i].ID < closure[j].ID
			}
			if closure[i].node != closure[j].node {
				idI, idJ := 0, 0
				if closure[i].node != nil {
					idI = closure[i].node.ID
				}
				if closure[j].node != nil {
					idJ = closure[j].node.ID
				}
				return idI < idJ
			}
			return closure[i].Priority < closure[j].Priority
		})

		key := serializeSet(closure)
		if id, ok := nfaToDfa[key]; ok {
			return id
		}

		id := StateID(len(dfaToNfa))
		nfaToDfa[key] = id
		dfaToNfa = append(dfaToNfa, closure)

		isAccepting := false
		matchPriority := 1<<30 - 1
		minPathPriority := 1<<30 - 1
		for _, s := range closure {
			if s.Priority < minPathPriority {
				minPathPriority = s.Priority
			}
			if prog.Inst[s.ID].Op == syntax.InstMatch && s.node == nil {
				isAccepting = true
				if s.Priority < matchPriority {
					matchPriority = s.Priority
				}
			}
		}
		d.accepting = append(d.accepting, isAccepting)
		d.stateMatchPriority = append(d.stateMatchPriority, matchPriority)
		// If the best matching NFA path has the same priority as the best active path,
		// then we've found the best possible match for this start position.
		d.stateIsBestMatch = append(d.stateIsBestMatch, isAccepting && (matchPriority == minPathPriority))

		for i := 0; i < d.stride; i++ {
			d.transitions = append(d.transitions, InvalidState)
			d.transPriorityIncrement = append(d.transPriorityIncrement, 0)
		}
		d.numStates++
		return id
	}

	// 1. Initial start state
	initialPaths := []nfaPath{{nfaState: nfaState{ID: uint32(prog.Start)}}}
	defaultStartClosure := epsilonClosure(initialPaths, prog, 0)
	d.startState = addDfaState(defaultStartClosure)
	if errBuild != nil {
		return errBuild
	}

	for i := 0; i < len(dfaToNfa); i++ {
		if i%100 == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
		currentClosure := dfaToNfa[i]
		currentDfaID := StateID(i)

		var searchClosure []nfaPath
		if withSearch {
			// To support implicit .*? (O(n) search), always try starting a new match.
			searchClosure = make([]nfaPath, len(currentClosure)+len(defaultStartClosure))
			copy(searchClosure, currentClosure)
			for j, p := range defaultStartClosure {
				p.Priority += 1000000 // Very low priority
				searchClosure[len(currentClosure)+j] = p
			}
		} else {
			searchClosure = currentClosure
		}

		for b := 0; b < 256; b++ {
			var nextPaths []nfaPath
			foundMatch := false

			for _, p := range searchClosure {
				s := p.nfaState
				inst := prog.Inst[s.ID]

				var matchedOut []uint32
				var matchedNodes []*utf8Node

				if s.node == nil {
					switch inst.Op {
					case syntax.InstRune, syntax.InstRune1, syntax.InstRuneAny, syntax.InstRuneAnyNotNL:
						roots := getTrie(s.ID)
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
					foundMatch = true
					for _, out := range matchedOut {
						np := nfaPath{
							nfaState: nfaState{ID: out},
							Priority: p.Priority,
						}
						nextPaths = append(nextPaths, np)
					}
					for _, node := range matchedNodes {
						np := nfaPath{
							nfaState: nfaState{ID: s.ID, node: node},
							Priority: p.Priority,
						}
						nextPaths = append(nextPaths, np)
					}
				}
			}

			if foundMatch {
				nextClosure := epsilonClosure(nextPaths, prog, 0)
				// Calculate normalization minP
				minP := 0
				if len(nextClosure) > 0 {
					minP = nextClosure[0].Priority
					for i := 1; i < len(nextClosure); i++ {
						if nextClosure[i].Priority < minP {
							minP = nextClosure[i].Priority
						}
					}
				}

				nextDfaID := addDfaState(nextClosure)
				if errBuild != nil {
					return errBuild
				}
				idx := int(currentDfaID)*d.stride + b
				d.transitions[idx] = nextDfaID
				d.transPriorityIncrement[idx] = int32(minP)
			}
		}

		if d.hasAnchors {
			for bit := 0; bit < numVirtualBytes; bit++ {
				op := syntax.EmptyOp(1 << bit)
				var initialPaths []nfaPath
				if withSearch {
					initialPaths = make([]nfaPath, len(currentClosure)+1)
					for j, p := range currentClosure {
						initialPaths[j] = nfaPath{
							nfaState: p.nfaState,
							Priority: p.Priority,
						}
					}
					initialPaths[len(currentClosure)] = nfaPath{
						nfaState: nfaState{ID: uint32(prog.Start)},
						Priority: 1000000,
					}
				} else {
					initialPaths = make([]nfaPath, len(currentClosure))
					for j, p := range currentClosure {
						initialPaths[j] = nfaPath{
							nfaState: p.nfaState,
							Priority: p.Priority,
						}
					}
				}
				nextClosure := epsilonClosure(initialPaths, prog, op)
				if serializeSet(nextClosure) != serializeSet(currentClosure) {
					// Calculate normalization minP
					minP := 0
					if len(nextClosure) > 0 {
						minP = nextClosure[0].Priority
						for i := 1; i < len(nextClosure); i++ {
							if nextClosure[i].Priority < minP {
								minP = nextClosure[i].Priority
							}
						}
					}

					nextDfaID := addDfaState(nextClosure)
					if errBuild != nil {
						return errBuild
					}
					idx := int(currentDfaID)*d.stride + 256 + bit
					d.transitions[idx] = nextDfaID
					d.transPriorityIncrement[idx] = int32(minP)
				}
			}
		}
	}

	d.minimize()

	return nil
}

func (d *DFA) minimize() {
	if d.numStates <= 1 {
		return
	}

	// Moore's Algorithm (Partition Refinement)
	// 1. Initial partition based on acceptance properties.
	stateToGroup := make([]int32, d.numStates)
	type groupSig struct {
		acc       bool
		prio      int
		bestMatch bool
	}
	sigToGroup := make(map[groupSig]int32)
	numGroups := int32(0)

	for i := 0; i < d.numStates; i++ {
		sig := groupSig{d.accepting[i], d.stateMatchPriority[i], d.stateIsBestMatch[i]}
		g, ok := sigToGroup[sig]
		if !ok {
			g = numGroups
			numGroups++
			sigToGroup[sig] = g
		}
		stateToGroup[i] = g
	}

	// 2. Refine partition.
	for {
		type splitKey struct {
			oldGroup int32
			transSig string
		}
		newGroups := make(map[splitKey]int32)
		nextStateToGroup := make([]int32, d.numStates)
		nextNumGroups := int32(0)

		for i := 0; i < d.numStates; i++ {
			// Transition signature: (targetGroup, priorityIncrement) for each byte.
			buf := make([]byte, d.stride*8)
			for b := 0; b < d.stride; b++ {
				idx := i*d.stride + b
				target := d.transitions[idx]
				inc := d.transPriorityIncrement[idx]

				var targetGroup int32 = -1
				if target != InvalidState {
					targetGroup = stateToGroup[target]
				}

				off := b * 8
				*(*int32)(unsafe.Pointer(&buf[off])) = targetGroup
				*(*int32)(unsafe.Pointer(&buf[off+4])) = inc
			}

			key := splitKey{stateToGroup[i], string(buf)}
			g, ok := newGroups[key]
			if !ok {
				g = nextNumGroups
				nextNumGroups++
				newGroups[key] = g
			}
			nextStateToGroup[i] = g
		}

		if nextNumGroups == numGroups {
			break
		}
		stateToGroup = nextStateToGroup
		numGroups = nextNumGroups
	}

	// 3. Rebuild DFA with minimal states.
	groupToFirstState := make([]int, numGroups)
	for i, g := range stateToGroup {
		groupToFirstState[g] = i
	}

	newTransitions := make([]StateID, int(numGroups)*d.stride)
	newIncrements := make([]int32, int(numGroups)*d.stride)
	newAccepting := make([]bool, numGroups)
	newPrio := make([]int, numGroups)
	newBest := make([]bool, numGroups)

	for g := int32(0); g < numGroups; g++ {
		oldS := groupToFirstState[g]
		newAccepting[g] = d.accepting[oldS]
		newPrio[g] = d.stateMatchPriority[oldS]
		newBest[g] = d.stateIsBestMatch[oldS]

		for b := 0; b < d.stride; b++ {
			oldIdx := oldS*d.stride + b
			target := d.transitions[oldIdx]
			if target != InvalidState {
				newTransitions[int(g)*d.stride+b] = StateID(stateToGroup[target])
			} else {
				newTransitions[int(g)*d.stride+b] = InvalidState
			}
			newIncrements[int(g)*d.stride+b] = d.transPriorityIncrement[oldIdx]
		}
	}

	d.transitions = newTransitions
	d.transPriorityIncrement = newIncrements
	d.accepting = newAccepting
	d.stateMatchPriority = newPrio
	d.stateIsBestMatch = newBest
	d.numStates = int(numGroups)
	d.startState = StateID(stateToGroup[d.startState])
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

func epsilonClosure(paths []nfaPath, prog *syntax.Prog, context syntax.EmptyOp) []nfaPath {
	type key struct {
		ID   uint32
		node *utf8Node
	}
	best := make(map[key]nfaPath)
	minMatchPriority := 1<<30 - 1

	type pathWithHistory struct {
		p       nfaPath
		history []uint32
	}
	stack := make([]pathWithHistory, 0, len(paths))
	for i := len(paths) - 1; i >= 0; i-- {
		p := paths[i]
		stack = append(stack, pathWithHistory{p, []uint32{p.ID}})
	}

	for len(stack) > 0 {
		ph := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		p := ph.p

		// Static Priority Resolution:
		// If we already have a path to this instruction with better priority, skip.
		k := key{p.ID, p.node}
		if existing, ok := best[k]; ok {
			if p.Priority >= existing.Priority {
				continue
			}
		}
		best[k] = p

		if p.node == nil {
			inst := prog.Inst[p.ID]

			if inst.Op == syntax.InstMatch {
				// Record the best priority that results in a match at the current position.
				if p.Priority < minMatchPriority {
					minMatchPriority = p.Priority
				}
				continue
			}

			push := func(nextID uint32, nextPriority int) {
				for _, id := range ph.history {
					if id == nextID {
						return
					}
				}
				newHistory := make([]uint32, len(ph.history)+1)
				copy(newHistory, ph.history)
				newHistory[len(ph.history)] = nextID
				stack = append(stack, pathWithHistory{
					p: nfaPath{
						nfaState: nfaState{ID: nextID},
						Priority: nextPriority,
					},
					history: newHistory,
				})
			}

			switch inst.Op {
			case syntax.InstAlt, syntax.InstAltMatch:
				// First branch (Out) has higher priority in standard Go regexp
				push(inst.Arg, p.Priority+1)
				push(inst.Out, p.Priority)
			case syntax.InstCapture:
				push(inst.Out, p.Priority)
			case syntax.InstNop:
				push(inst.Out, p.Priority)
			case syntax.InstEmptyWidth:
				if syntax.EmptyOp(inst.Arg)&context == syntax.EmptyOp(inst.Arg) {
					push(inst.Out, p.Priority)
				}
			}
		}
	}

	var result []nfaPath
	for _, p := range best {
		// Static Priority Resolution:
		// Any path with priority worse than the best match found in this closure is useless
		// for finding the leftmost-first match. Because priorities are assigned at branches
		// and never decrease along a path, a higher priority match (lower numerical value)
		// will always shadow any matches from lower priority branches starting at the same position.
		if p.Priority <= minMatchPriority {
			result = append(result, p)
		}
	}

	// Canonical sort for state key
	sort.Slice(result, func(i, j int) bool {
		if result[i].Priority != result[j].Priority {
			return result[i].Priority < result[j].Priority
		}
		if result[i].ID != result[j].ID {
			return result[i].ID < result[j].ID
		}
		idI, idJ := 0, 0
		if result[i].node != nil {
			idI = result[i].node.ID
		}
		if result[j].node != nil {
			idJ = result[j].node.ID
		}
		return idI < idJ
	})

	return result
}

func serializeSet(set []nfaPath) string {
	if len(set) == 0 {
		return ""
	}
	// Binary serialization for speed and memory efficiency.
	// Each path is 12 bytes: ID(4), nodeID(4), Priority(4).
	buf := make([]byte, len(set)*12)
	for i, s := range set {
		off := i * 12
		id := s.ID
		buf[off] = byte(id)
		buf[off+1] = byte(id >> 8)
		buf[off+2] = byte(id >> 16)
		buf[off+3] = byte(id >> 24)

		var nodeID uint32
		if s.node != nil {
			nodeID = uint32(s.node.ID)
		}
		buf[off+4] = byte(nodeID)
		buf[off+5] = byte(nodeID >> 8)
		buf[off+6] = byte(nodeID >> 16)
		buf[off+7] = byte(nodeID >> 24)

		p := uint32(s.Priority)
		buf[off+8] = byte(p)
		buf[off+9] = byte(p >> 8)
		buf[off+10] = byte(p >> 16)
		buf[off+11] = byte(p >> 24)
	}
	return string(buf)
}
