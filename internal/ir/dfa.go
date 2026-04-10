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
	ID   uint32
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
	tags     []int  // Just indices
	visited  []bool // Which tags in the tags slice were actually visited in this path
	Priority int
	origin   int // index in the previous state's closure
}

// DFA represents a Deterministic Finite Automaton.
type DFA struct {
	transitions            []StateID // Flattened table for better cache locality: table[state * stride + byte]
	transPriorityIncrement []int32   // Priority shift for each transition
	stride                 int       // 256 or 257
	numStates              int
	startState             StateID
	hasAnchors             bool
	dfaToNfa               [][]nfaPath // List of NFA paths for each DFA state
	numSubexp              int

	// Cached trie roots for each instruction
	trieRoots [][]*utf8Node

	transPathOffsets []uint32
	pathSources      []int16
	pathTagOffsets   []uint32
	tagPool          []TagOp

	// Initial tags (for the start of match) - per path in start state
	entryPathTags [][]TagOp

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
	return NewDFAForSearch(prog)
}

func NewDFAForSearch(prog *syntax.Prog) (*DFA, error) {
	d := &DFA{
		numSubexp: prog.NumCap / 2,
	}
	if err := d.build(prog, true); err != nil {
		return nil, fmt.Errorf("failed to build DFA: %w", err)
	}
	return d, nil
}

func NewDFAForMatch(prog *syntax.Prog) (*DFA, error) {
	d := &DFA{
		numSubexp: prog.NumCap / 2,
	}
	if err := d.build(prog, false); err != nil {
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

func (d *DFA) NumEntryPaths() int {
	return len(d.entryPathTags)
}

func (d *DFA) EntryTagsForPath(idx int) []TagOp {
	if idx < 0 || idx >= len(d.entryPathTags) {
		return nil
	}
	return d.entryPathTags[idx]
}

func (d *DFA) HasAnchors() bool {
	return d.hasAnchors
}

func (d *DFA) TotalStates() int {
	return d.numStates
}

func (d *DFA) NfaPaths(s StateID) []nfaPath {
	if s < 0 || int(s) >= d.numStates {
		return nil
	}
	return d.dfaToNfa[s]
}

func (d *DFA) TrieRoots() [][]*utf8Node {
	return d.trieRoots
}

func (d *DFA) TransitionTags(current StateID, b int) []TagOp {
	if current < 0 || int(current) >= d.numStates || b < 0 || b >= d.stride {
		return nil
	}
	idx := int(current)*d.stride + b
	if idx >= len(d.transPathOffsets)-1 {
		return nil
	}

	start := d.transPathOffsets[idx]
	end := d.transPathOffsets[idx+1]
	if start == end {
		return nil
	}

	// In TDFA with Static Priority Resolution, we only follow the tags
	// of the highest priority NFA path (index 0 in nextClosure/allTransInfo).
	tagStart := d.pathTagOffsets[start]
	tagEnd := d.pathTagOffsets[start+1]
	return d.tagPool[tagStart:tagEnd]
}

// I'll add direct fields accessors for the engine to use.
func (d *DFA) PathSources() []int16       { return d.pathSources }
func (d *DFA) TransPathOffsets() []uint32 { return d.transPathOffsets }
func (d *DFA) PathTagOffsets() []uint32   { return d.pathTagOffsets }
func (d *DFA) TagPool() []TagOp           { return d.tagPool }

func (d *DFA) TransitionInfo(current StateID, b int) (sources []int16, tags [][]TagOp) {
	if current < 0 || int(current) >= d.numStates || b < 0 || b >= d.stride {
		return nil, nil
	}
	idx := int(current)*d.stride + b
	if idx >= len(d.transPathOffsets)-1 {
		return nil, nil
	}

	start := d.transPathOffsets[idx]
	end := d.transPathOffsets[idx+1]

	sources = d.pathSources[start:end]
	tags = make([][]TagOp, len(sources))
	for i := range sources {
		tagStart := d.pathTagOffsets[start+uint32(i)]
		tagEnd := d.pathTagOffsets[start+uint32(i)+1]
		tags[i] = d.tagPool[tagStart:tagEnd]
	}
	return sources, tags
}

func (d *DFA) AcceptingPriority(s StateID) int {
	if s < 0 || int(s) >= d.numStates {
		return 1<<30 - 1
	}
	return d.stateMatchPriority[s]
}

func (d *DFA) build(prog *syntax.Prog, withSearch bool) error {
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
			roots = runeRangesToUTF8Trie(inst.Rune, foldCase)
		case syntax.InstRuneAny:
			roots = anyRuneTrie(true)
		case syntax.InstRuneAnyNotNL:
			roots = anyRuneTrie(false)
		}
		d.trieRoots[ID] = roots
		return roots
	}

	nfaToDfa := make(map[string]StateID)
	dfaToNfa := make([][]nfaPath, 0)

	type transInfo struct {
		sources []int16
		tags    [][]TagOp
	}
	allTransInfo := make([]transInfo, 0)

	addDfaState := func(closure []nfaPath) StateID {
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

		sorted := make([]nfaPath, len(closure))
		copy(sorted, closure)
		sort.Slice(sorted, func(i, j int) bool {
			if sorted[i].ID != sorted[j].ID {
				return sorted[i].ID < sorted[j].ID
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
			allTransInfo = append(allTransInfo, transInfo{})
		}
		d.numStates++
		return id
	}

	// 1. Initial start state
	initialPaths := []nfaPath{{nfaState: nfaState{ID: uint32(prog.Start)}, origin: -1}}
	defaultStartClosure, _ := epsilonClosure(initialPaths, prog, 0)
	d.startState = addDfaState(defaultStartClosure)

	// Record entry tags for each path in the start state.
	d.entryPathTags = make([][]TagOp, len(defaultStartClosure))
	for j, p := range defaultStartClosure {
		for k, t := range p.tags {
			if k < len(p.visited) && p.visited[k] {
				d.entryPathTags[j] = append(d.entryPathTags[j], MakeTagOp(t, false))
			}
		}
	}

	for i := 0; i < len(dfaToNfa); i++ {
		currentClosure := dfaToNfa[i]
		currentDfaID := StateID(i)

		var searchClosure []nfaPath
		if withSearch {
			// To support implicit .*? (O(n) search), always try starting a new match.
			searchClosure = make([]nfaPath, len(currentClosure)+len(defaultStartClosure))
			copy(searchClosure, currentClosure)
			for j, p := range defaultStartClosure {
				p.origin = -1         // new match start
				p.Priority += 1000000 // Very low priority
				searchClosure[len(currentClosure)+j] = p
			}
		} else {
			searchClosure = currentClosure
		}

		for b := 0; b < 256; b++ {
			var nextPaths []nfaPath
			foundMatch := false

			for pIdx, p := range searchClosure {
				origin := pIdx
				if withSearch && pIdx >= len(currentClosure) {
					origin = -1
				}
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
							origin:   origin,
							Priority: p.Priority,
						}
						if origin == -1 {
							np.tags = p.tags
							np.visited = p.visited
						}
						nextPaths = append(nextPaths, np)
					}
					for _, node := range matchedNodes {
						np := nfaPath{
							nfaState: nfaState{ID: s.ID, node: node},
							origin:   origin,
							Priority: p.Priority,
						}
						if origin == -1 {
							np.tags = p.tags
							np.visited = p.visited
						}
						nextPaths = append(nextPaths, np)
					}
				}
			}

			if foundMatch {
				nextClosure, _ := epsilonClosure(nextPaths, prog, 0)
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
				idx := int(currentDfaID)*d.stride + b
				d.transitions[idx] = nextDfaID
				d.transPriorityIncrement[idx] = int32(minP)

				ti := transInfo{
					sources: make([]int16, len(nextClosure)),
					tags:    make([][]TagOp, len(nextClosure)),
				}
				for j, p := range nextClosure {
					ti.sources[j] = int16(p.origin)
					for k, t := range p.tags {
						if k < len(p.visited) && p.visited[k] {
							ti.tags[j] = append(ti.tags[j], MakeTagOp(t, true))
						}
					}
				}
				allTransInfo[idx] = ti
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
							origin:   j,
							Priority: p.Priority,
						}
					}
					initialPaths[len(currentClosure)] = nfaPath{
						nfaState: nfaState{ID: uint32(prog.Start)},
						origin:   -1,
						Priority: 1000000,
					}
				} else {
					initialPaths = make([]nfaPath, len(currentClosure))
					for j, p := range currentClosure {
						initialPaths[j] = nfaPath{
							nfaState: p.nfaState,
							origin:   j,
							Priority: p.Priority,
						}
					}
				}
				nextClosure, _ := epsilonClosure(initialPaths, prog, op)
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
					idx := int(currentDfaID)*d.stride + 256 + bit
					d.transitions[idx] = nextDfaID
					d.transPriorityIncrement[idx] = int32(minP)

					ti := transInfo{
						sources: make([]int16, len(nextClosure)),
						tags:    make([][]TagOp, len(nextClosure)),
					}
					for j, p := range nextClosure {
						ti.sources[j] = int16(p.origin)
						for k, t := range p.tags {
							if k < len(p.visited) && p.visited[k] {
								ti.tags[j] = append(ti.tags[j], MakeTagOp(t, false))
							}
						}
					}
					allTransInfo[idx] = ti
				}
			}
		}
	}

	d.transPathOffsets = make([]uint32, len(allTransInfo)+1)
	d.pathTagOffsets = make([]uint32, 0)
	for i, ti := range allTransInfo {
		d.transPathOffsets[i] = uint32(len(d.pathSources))
		for j := range ti.sources {
			d.pathSources = append(d.pathSources, ti.sources[j])
			d.pathTagOffsets = append(d.pathTagOffsets, uint32(len(d.tagPool)))
			d.tagPool = append(d.tagPool, ti.tags[j]...)
		}
	}
	d.transPathOffsets[len(allTransInfo)] = uint32(len(d.pathSources))
	d.pathTagOffsets = append(d.pathTagOffsets, uint32(len(d.tagPool)))

	return nil
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
		ID   uint32
		node *utf8Node
	}
	best := make(map[key]nfaPath)

	type pathWithHistory struct {
		p       nfaPath
		history []uint32
	}
	stack := make([]pathWithHistory, 0, len(paths))
	for i := len(paths) - 1; i >= 0; i-- {
		p := paths[i]
		p.visited = make([]bool, len(p.tags))
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

			push := func(nextID uint32, nextTags []int, nextVisited []bool, nextPriority int) {
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
						tags:     nextTags,
						visited:  nextVisited,
						Priority: nextPriority,
						origin:   p.origin,
					},
					history: newHistory,
				})
			}

			switch inst.Op {
			case syntax.InstAlt, syntax.InstAltMatch:
				push(inst.Arg, p.tags, p.visited, p.Priority+1)
				push(inst.Out, p.tags, p.visited, p.Priority)
			case syntax.InstCapture:
				// Only track the overall match (capture 0) in the DFA.
				// Submatches will be handled by the 2nd pass NFA rescan.
				if inst.Arg < 2 {
					newTags := make([]int, len(p.tags)+1)
					copy(newTags, p.tags)
					newTags[len(p.tags)] = int(inst.Arg)
					newVisited := make([]bool, len(p.visited)+1)
					copy(newVisited, p.visited)
					newVisited[len(p.visited)] = true
					push(inst.Out, newTags, newVisited, p.Priority)
				} else {
					push(inst.Out, p.tags, p.visited, p.Priority)
				}
			case syntax.InstNop:
				push(inst.Out, p.tags, p.visited, p.Priority)
			case syntax.InstEmptyWidth:
				if syntax.EmptyOp(inst.Arg)&context == syntax.EmptyOp(inst.Arg) {
					push(inst.Out, p.tags, p.visited, p.Priority)
				}
			case syntax.InstMatch:
				continue
			}
		}
	}

	var result []nfaPath
	for _, p := range best {
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Priority != result[j].Priority {
			return result[i].Priority < result[j].Priority
		}
		if result[i].ID != result[j].ID {
			return result[i].ID < result[j].ID
		}
		return fmt.Sprintf("%p", result[i].node) < fmt.Sprintf("%p", result[j].node)
	})

	return result, nil
}

func serializeSet(set []nfaPath) string {
	var sb strings.Builder
	for i, s := range set {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.FormatUint(uint64(s.ID), 10))
		if s.node != nil {
			sb.WriteByte(':')
			sb.WriteString(fmt.Sprintf("%p", s.node))
		}
		sb.WriteByte('@')
		sb.WriteString(strconv.Itoa(s.Priority))
	}
	return sb.String()
}
