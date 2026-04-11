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
	PreTags  uint64
	PostTags uint64
}

// TagRecord represents a set of tags encountered at a specific position.
type TagRecord struct {
	Tags uint64
	Pos  int
}

// DFA represents a Deterministic Finite Automaton.
type DFA struct {
	transitions            []StateID
	transPriorityIncrement []int32

	// Simple Tagged DFA: record only the tags passed by the BEST path.
	// For greedy loops, Phase 1 correctly finds the winning priority.
	// If we build the DFA correctly, Priority 0 in each transition will be the best path.
	transPreTags  []uint64
	transPostTags []uint64

	// Start tags for the best path.
	startTags uint64

	stride     int
	numStates  int
	searchState StateID
	matchState  StateID
	hasAnchors bool
	numSubexp  int

	// Metadata for each state
	stateIsSearch []bool

	// Cached trie roots for each instruction
	trieRoots [][]*utf8Node

	// Accepting info
	accepting          []bool
	stateMatchPriority []int
	stateMatchTags     []uint64
	stateIsBestMatch   []bool

	// Phase 2 Metadata
	isAlwaysTrue   []bool
	warpPoints     []int16   // byte value to skip to, or -1
	warpPointState []StateID // state to jump to after skip
}

func (d *DFA) IsAlwaysTrue(s StateID) bool {
	if s < 0 || int(s) >= d.numStates || d.isAlwaysTrue == nil {
		return false
	}
	return d.isAlwaysTrue[s]
}

func (d *DFA) IsAlwaysTrueFunc() func(StateID) bool {
	return func(s StateID) bool {
		if s < 0 || int(s) >= d.numStates || d.isAlwaysTrue == nil {
			return false
		}
		return d.isAlwaysTrue[s]
	}
}

func (d *DFA) WarpPoint(s StateID) int16 {
	if s < 0 || int(s) >= d.numStates || d.warpPoints == nil {
		return -1
	}
	return d.warpPoints[s]
}

func (d *DFA) WarpPoints() []int16 {
	return d.warpPoints
}

func (d *DFA) WarpPointState(s StateID) StateID {
	if s < 0 || int(s) >= d.numStates || d.warpPointState == nil {
		return InvalidState
	}
	return d.warpPointState[s]
}

func (d *DFA) WarpPointStates() []StateID {
	return d.warpPointState
}

func (d *DFA) computePhase2Metadata() {
	d.isAlwaysTrue = make([]bool, d.numStates)
	d.warpPoints = make([]int16, d.numStates)
	d.warpPointState = make([]StateID, d.numStates)

	for i := range d.warpPoints {
		d.warpPoints[i] = -1
		d.warpPointState[i] = InvalidState
	}

	d.findWarpPoints()
	d.findSCCs()
}

func (d *DFA) findWarpPoints() {
	for i := 0; i < d.numStates; i++ {
		currState := StateID(i)
		// A Warp Point is a state where:
		// 1. Exactly one byte leads to progress (a different state).
		// 2. All other bytes lead to InvalidState or the same state.
		// 3. The state is NOT accepting (to avoid skipping over a match).
		if d.accepting[i] {
			continue
		}

		progressByte := -1
		targetState := InvalidState
		possible := true

		for b := 0; b < 256; b++ {
			next := d.transitions[i*d.stride+b]
			if next == InvalidState || next == currState {
				continue
			}
			if progressByte == -1 {
				progressByte = b
				targetState = next
			} else {
				// More than one progress byte.
				possible = false
				break
			}
		}

		if possible && progressByte != -1 {
			d.warpPoints[i] = int16(progressByte)
			d.warpPointState[i] = targetState
		}
	}
}

func (d *DFA) findSCCs() {
	// Tarjan's algorithm to find SCCs.
	// We want to identify states that are "Always True", meaning once entered,
	// any further input will lead to a match eventually (or is already matching).
	// Actually, the stronger condition for "Always True" in a DFA for Search is:
	// A state s is Always True if all paths from s eventually hit an accepting state
	// AND stay in accepting states (or the match is already guaranteed).
	// In our Search DFA, we have a search closure (.*?), so if a state belongs to
	// an SCC where every state is accepting, it's Always True.

	num := 0
	index := make([]int, d.numStates)
	lowlink := make([]int, d.numStates)
	onStack := make([]bool, d.numStates)
	stack := []int{}

	for i := range index {
		index[i] = -1
	}

	var strongconnect func(v int)
	strongconnect = func(v int) {
		index[v] = num
		lowlink[v] = num
		num++
		stack = append(stack, v)
		onStack[v] = true

		for b := 0; b < 256; b++ {
			w := int(d.transitions[v*d.stride+b])
			if w == -1 {
				continue
			}
			if index[w] == -1 {
				strongconnect(w)
				if lowlink[w] < lowlink[v] {
					lowlink[v] = lowlink[w]
				}
			} else if onStack[w] {
				if index[w] < lowlink[v] {
					lowlink[v] = index[w]
				}
			}
		}

		if lowlink[v] == index[v] {
			// Found an SCC.
			var component []int
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				component = append(component, w)
				if w == v {
					break
				}
			}

			// Check if this component is "Always True".
			// For a component to be Always True:
			// 1. All states in the component must be accepting.
			// 2. All transitions from any state in the component must lead to
			//    another state in the same component OR another Always True component.
			// This requires processing SCCs in reverse topological order.
			// Tarjan's naturally finds them in reverse topological order.

			allAccepting := true
			for _, s := range component {
				if !d.accepting[s] {
					allAccepting = false
					break
				}
			}

			if allAccepting {
				allTransitionsToAlwaysTrue := true
				for _, s := range component {
					for b := 0; b < 256; b++ {
						next := int(d.transitions[s*d.stride+b])
						if next == -1 {
							// In a Search DFA, next should ideally not be -1 for bytes if it's always true,
							// but if it is -1, it means it doesn't match, so it's NOT always true.
							allTransitionsToAlwaysTrue = false
							break
						}
						// If next is not in current component, it must be in a previously
						// identified Always True component.
						inCurrent := false
						for _, cs := range component {
							if cs == next {
								inCurrent = true
								break
							}
						}
						if !inCurrent && !d.isAlwaysTrue[next] {
							allTransitionsToAlwaysTrue = false
							break
						}
					}
					if !allTransitionsToAlwaysTrue {
						break
					}
				}
				if allTransitionsToAlwaysTrue {
					for _, s := range component {
						d.isAlwaysTrue[s] = true
					}
				}
			}
		}
	}

	for i := 0; i < d.numStates; i++ {
		if index[i] == -1 {
			strongconnect(i)
		}
	}
}

const (
	VirtualBeginLine = 256 + iota
	VirtualEndLine
	VirtualBeginText
	VirtualEndText
	VirtualWordBoundary
	VirtualNoWordBoundary
	numVirtualBytes = 6
)

func NewDFA(prog *syntax.Prog) (*DFA, error) {
	d := &DFA{
		numSubexp: prog.NumCap / 2,
	}
	if err := d.build(context.Background(), prog); err != nil {
		return nil, fmt.Errorf("failed to build DFA: %w", err)
	}
	return d, nil
}

func NewDFAContext(ctx context.Context, prog *syntax.Prog) (*DFA, error) {
	d := &DFA{
		numSubexp: prog.NumCap / 2,
	}
	if err := d.build(ctx, prog); err != nil {
		return nil, fmt.Errorf("failed to build DFA: %w", err)
	}
	return d, nil
}

func (d *DFA) Next(current StateID, b int) StateID {
	if current < 0 || int(current) >= d.numStates || b < 0 || b >= d.stride {
		return InvalidState
	}
	offset := int(current)*d.stride + b
	if offset >= len(d.transitions) {
		return InvalidState
	}
	return d.transitions[offset]
}

func (d *DFA) Transitions() []StateID {
	return d.transitions
}

func (d *DFA) PriorityIncrements() []int32 {
	return d.transPriorityIncrement
}

func (d *DFA) PreTags() []uint64 {
	return d.transPreTags
}

func (d *DFA) PostTags() []uint64 {
	return d.transPostTags
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

func (d *DFA) SearchState() StateID {
	return d.searchState
}

func (d *DFA) MatchState() StateID {
	return d.matchState
}

func (d *DFA) StartTags() uint64 {
	return d.startTags
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

func (d *DFA) MatchTags(s StateID) uint64 {
	if s < 0 || int(s) >= d.numStates {
		return 0
	}
	return d.stateMatchTags[s]
}

var ErrStateExplosion = fmt.Errorf("regexp: pattern too large or ambiguous")

const MaxDFAMemory = 64 * 1024 * 1024

type dfaStateKey struct {
	nfaKey   string
	isSearch bool
}

func (d *DFA) build(ctx context.Context, prog *syntax.Prog) error {
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

	nfaToDfa := make(map[dfaStateKey]StateID)
	dfaToNfa := make([][]nfaPath, 0)

	var errBuild error
	addDfaState := func(closure []nfaPath, isSearch bool) StateID {
		if errBuild != nil {
			return InvalidState
		}

		if (d.numStates+1)*d.stride*8 > MaxDFAMemory {
			errBuild = ErrStateExplosion
			return InvalidState
		}

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

		key := dfaStateKey{serializeSet(closure), isSearch}
		if id, ok := nfaToDfa[key]; ok {
			return id
		}

		id := StateID(len(dfaToNfa))
		nfaToDfa[key] = id

		cleanClosure := make([]nfaPath, len(closure))
		for i, p := range closure {
			cleanClosure[i] = nfaPath{
				nfaState: p.nfaState,
				Priority: p.Priority,
			}
		}
		dfaToNfa = append(dfaToNfa, cleanClosure)
		d.stateIsSearch = append(d.stateIsSearch, isSearch)

		isAccepting := false
		matchPriority := 1<<30 - 1
		var matchTags uint64
		minPathPriority := 1<<30 - 1
		for _, s := range closure {
			if s.Priority < minPathPriority {
				minPathPriority = s.Priority
			}
			if prog.Inst[s.ID].Op == syntax.InstMatch && s.node == nil {
				isAccepting = true
				if s.Priority < matchPriority {
					matchPriority = s.Priority
					matchTags = s.PostTags
				}
			}
		}
		d.accepting = append(d.accepting, isAccepting)
		d.stateMatchPriority = append(d.stateMatchPriority, matchPriority)
		d.stateMatchTags = append(d.stateMatchTags, matchTags)
		d.stateIsBestMatch = append(d.stateIsBestMatch, isAccepting && (matchPriority == minPathPriority))

		for i := 0; i < d.stride; i++ {
			d.transitions = append(d.transitions, InvalidState)
			d.transPriorityIncrement = append(d.transPriorityIncrement, 0)
			d.transPreTags = append(d.transPreTags, 0)
			d.transPostTags = append(d.transPostTags, 0)
		}
		d.numStates++
		return id
	}

	initialPaths := []nfaPath{{nfaState: nfaState{ID: uint32(prog.Start)}, PreTags: 0, PostTags: 0}}
	defaultStartClosure := epsilonClosure(initialPaths, prog, 0)

	// Build Match start state
	d.matchState = addDfaState(defaultStartClosure, false)
	if len(defaultStartClosure) > 0 {
		d.startTags = defaultStartClosure[0].PostTags
	}

	// Build Search start state (includes the restart closure)
	searchInitialPaths := make([]nfaPath, len(defaultStartClosure))
	copy(searchInitialPaths, defaultStartClosure)
	// We don't need to add it here explicitly because it's added in the transition loop
	// for isSearch=true states. But the start state itself must be search-enabled.
	d.searchState = addDfaState(defaultStartClosure, true)

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
		currentIsSearch := d.stateIsSearch[i]

		var searchPaths []nfaPath
		if currentIsSearch {
			searchPaths = make([]nfaPath, len(currentClosure)+len(defaultStartClosure))
			copy(searchPaths, currentClosure)
			for j, p := range defaultStartClosure {
				// Priority 1000000 to ensure search paths are lower priority than existing ones.
				p.Priority += 1000000
				searchPaths[len(currentClosure)+j] = p
			}
		} else {
			searchPaths = currentClosure
		}

		searchClosure := epsilonClosure(searchPaths, prog, 0)

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
							PreTags:  p.PreTags | p.PostTags,
							PostTags: 0,
						}
						nextPaths = append(nextPaths, np)
					}
					for _, node := range matchedNodes {
						np := nfaPath{
							nfaState: nfaState{ID: s.ID, node: node},
							Priority: p.Priority,
							PreTags:  p.PreTags | p.PostTags,
							PostTags: 0,
						}
						nextPaths = append(nextPaths, np)
					}
				}
			}

			if foundMatch {
				nextClosure := epsilonClosure(nextPaths, prog, 0)

				minP := 0
				if len(nextClosure) > 0 {
					minP = nextClosure[0].Priority
					for i := 1; i < len(nextClosure); i++ {
						if nextClosure[i].Priority < minP {
							minP = nextClosure[i].Priority
						}
					}
				}

				nextDfaID := addDfaState(nextClosure, currentIsSearch)
				if errBuild != nil {
					return errBuild
				}
				idx := int(currentDfaID)*d.stride + b
				d.transitions[idx] = nextDfaID
				d.transPriorityIncrement[idx] = int32(minP)
				if len(nextClosure) > 0 {
					d.transPreTags[idx] = nextClosure[0].PreTags
					d.transPostTags[idx] = nextClosure[0].PostTags
				}
			}
		}

		if d.hasAnchors {
			for bit := 0; bit < numVirtualBytes; bit++ {
				op := syntax.EmptyOp(1 << bit)
				var initialPaths []nfaPath
				if currentIsSearch {
					initialPaths = make([]nfaPath, len(currentClosure)+1)
					for j, p := range currentClosure {
						initialPaths[j] = nfaPath{
							nfaState: p.nfaState,
							Priority: p.Priority,
							PreTags:  p.PreTags,
							PostTags: p.PostTags,
						}
					}
					initialPaths[len(currentClosure)] = nfaPath{
						nfaState: nfaState{ID: uint32(prog.Start)},
						Priority: 1000000,
						PreTags:  0,
						PostTags: 0,
					}
				} else {
					initialPaths = make([]nfaPath, len(currentClosure))
					for j, p := range currentClosure {
						initialPaths[j] = nfaPath{
							nfaState: p.nfaState,
							Priority: p.Priority,
							PreTags:  p.PreTags,
							PostTags: p.PostTags,
						}
					}
				}
				nextClosure := epsilonClosure(initialPaths, prog, op)
				if serializeSet(nextClosure) != serializeSet(currentClosure) {
					minP := 0
					if len(nextClosure) > 0 {
						minP = nextClosure[0].Priority
						for i := 1; i < len(nextClosure); i++ {
							if nextClosure[i].Priority < minP {
								minP = nextClosure[i].Priority
							}
						}
					}

					nextDfaID := addDfaState(nextClosure, currentIsSearch)
					if errBuild != nil {
						return errBuild
					}
					idx := int(currentDfaID)*d.stride + 256 + bit
					d.transitions[idx] = nextDfaID
					d.transPriorityIncrement[idx] = int32(minP)
					if len(nextClosure) > 0 {
						d.transPreTags[idx] = nextClosure[0].PreTags
						d.transPostTags[idx] = nextClosure[0].PostTags
					}
				}
			}
		}
	}

	d.minimize()
	d.computePhase2Metadata()

	return nil
}

func (d *DFA) minimize() {
	if d.numStates <= 1 {
		return
	}

	stateToGroup := make([]int32, d.numStates)
	type groupSig struct {
		acc       bool
		prio      int
		bestMatch bool
		isSearch  bool
	}
	sigToGroup := make(map[groupSig]int32)
	numGroups := int32(0)

	for i := 0; i < d.numStates; i++ {
		sig := groupSig{d.accepting[i], d.stateMatchPriority[i], d.stateIsBestMatch[i], d.stateIsSearch[i]}
		g, ok := sigToGroup[sig]
		if !ok {
			g = numGroups
			numGroups++
			sigToGroup[sig] = g
		}
		stateToGroup[i] = g
	}

	for {
		type splitKey struct {
			oldGroup int32
			transSig string
		}
		newGroups := make(map[splitKey]int32)
		nextStateToGroup := make([]int32, d.numStates)
		nextNumGroups := int32(0)

		for i := 0; i < d.numStates; i++ {
			buf := make([]byte, d.stride*20)
			for b := 0; b < d.stride; b++ {
				idx := i*d.stride + b
				target := d.transitions[idx]
				inc := d.transPriorityIncrement[idx]
				pre := d.transPreTags[idx]
				post := d.transPostTags[idx]
				var targetGroup int32 = -1
				if target != InvalidState {
					targetGroup = stateToGroup[target]
				}

				off := b * 20
				*(*int16)(unsafe.Pointer(&buf[off])) = int16(targetGroup)
				*(*int16)(unsafe.Pointer(&buf[off+2])) = int16(inc)
				*(*uint64)(unsafe.Pointer(&buf[off+4])) = pre
				*(*uint64)(unsafe.Pointer(&buf[off+12])) = post
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

	groupToFirstState := make([]int, numGroups)
	for i, g := range stateToGroup {
		groupToFirstState[g] = i
	}

	newTransitions := make([]StateID, int(numGroups)*d.stride)
	newIncrements := make([]int32, int(numGroups)*d.stride)
	newPre := make([]uint64, int(numGroups)*d.stride)
	newPost := make([]uint64, int(numGroups)*d.stride)
	newAccepting := make([]bool, numGroups)
	newPrio := make([]int, numGroups)
	newMatchTags := make([]uint64, numGroups)
	newBest := make([]bool, numGroups)
	newIsSearch := make([]bool, numGroups)

	for g := int32(0); g < numGroups; g++ {
		oldS := groupToFirstState[g]
		newAccepting[g] = d.accepting[oldS]
		newPrio[g] = d.stateMatchPriority[oldS]
		newMatchTags[g] = d.stateMatchTags[oldS]
		newBest[g] = d.stateIsBestMatch[oldS]
		newIsSearch[g] = d.stateIsSearch[oldS]

		for b := 0; b < d.stride; b++ {
			oldIdx := oldS*d.stride + b
			target := d.transitions[oldIdx]
			if target != InvalidState {
				newTransitions[int(g)*d.stride+b] = StateID(stateToGroup[target])
			} else {
				newTransitions[int(g)*d.stride+b] = InvalidState
			}
			newIncrements[int(g)*d.stride+b] = d.transPriorityIncrement[oldIdx]
			newPre[int(g)*d.stride+b] = d.transPreTags[oldIdx]
			newPost[int(g)*d.stride+b] = d.transPostTags[oldIdx]
		}
	}

	d.transitions = newTransitions
	d.transPriorityIncrement = newIncrements
	d.transPreTags = newPre
	d.transPostTags = newPost
	d.accepting = newAccepting
	d.stateMatchPriority = newPrio
	d.stateMatchTags = newMatchTags
	d.stateIsBestMatch = newBest
	d.stateIsSearch = newIsSearch
	d.numStates = int(numGroups)
	d.searchState = StateID(stateToGroup[d.searchState])
	d.matchState = StateID(stateToGroup[d.matchState])
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
				if p.Priority < minMatchPriority {
					minMatchPriority = p.Priority
				}
				continue
			}

			push := func(nextID uint32, nextPriority int, preTags, postTags uint64) {
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
						PreTags:  preTags,
						PostTags: postTags,
					},
					history: newHistory,
				})
			}

			switch inst.Op {
			case syntax.InstAlt, syntax.InstAltMatch:
				push(inst.Arg, p.Priority+1, p.PreTags, p.PostTags)
				push(inst.Out, p.Priority, p.PreTags, p.PostTags)
			case syntax.InstCapture:
				newPostTags := p.PostTags
				if inst.Arg < 64 {
					newPostTags |= (1 << inst.Arg)
				}
				push(inst.Out, p.Priority, p.PreTags, newPostTags)
			case syntax.InstNop:
				push(inst.Out, p.Priority, p.PreTags, p.PostTags)
			case syntax.InstEmptyWidth:
				if syntax.EmptyOp(inst.Arg)&context == syntax.EmptyOp(inst.Arg) {
					push(inst.Out, p.Priority, p.PreTags, p.PostTags)
				}
			}
		}
	}

	var result []nfaPath
	for _, p := range best {
		if p.Priority <= minMatchPriority {
			result = append(result, p)
		}
	}

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
