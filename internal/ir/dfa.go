package ir

import (
	"context"
	"fmt"
	"sort"
	"unsafe"

	"github.com/kamichidu/go-regexp-re/syntax"
)

type StateID int32

const (
	InvalidState StateID = -1
	StartStateID StateID = 0
)
const (
	VirtualBeginLine = 256 + iota
	VirtualEndLine
	VirtualBeginText
	VirtualEndText
	VirtualWordBoundary
	VirtualNoWordBoundary
	numVirtualBytes = 6
)
const TaggedStateFlag StateID = -2147483648

type nfaState struct {
	ID   uint32
	node *utf8Node
}
type nfaPath struct {
	nfaState
	Priority int
	Tags     uint64
}

type PathTagUpdate struct {
	RelativePriority int32
	Tags             uint64
}

type TransitionUpdate struct {
	BasePriority int32
	PreUpdates   []PathTagUpdate
	PostUpdates  []PathTagUpdate
}

type DFA struct {
	transitions        []StateID
	tagUpdateIndices   []uint32
	tagUpdates         []TransitionUpdate
	startUpdates       []PathTagUpdate
	stride             int
	numStates          int
	searchState        StateID
	matchState         StateID
	hasAnchors         bool
	numSubexp          int
	stateIsSearch      []bool
	trieRoots          [][]*utf8Node
	accepting          []bool
	stateMatchPriority []int
	stateMatchTags     []uint64
	stateIsBestMatch   []bool
	isAlwaysTrue       []bool
	warpPoints         []int16
	warpPointState     []StateID
}

type BitParallelDFA struct {
	CharMasks [256]uint64
	Epsilon   [64]uint64
	MatchMask uint64
}

func (d *DFA) Next(current StateID, b int) StateID {
	if current < 0 || int(current) >= d.numStates || b < 0 || b >= d.stride {
		return InvalidState
	}
	offset := int(current)*d.stride + b
	if offset >= len(d.transitions) {
		return InvalidState
	}
	raw := d.transitions[offset]
	if raw == InvalidState {
		return InvalidState
	}
	return raw & 0x7FFFFFFF
}
func (d *DFA) NumStates() int                 { return d.numStates }
func (d *DFA) TotalStates() int               { return d.numStates }
func (d *DFA) Transitions() []StateID         { return d.transitions }
func (d *DFA) TagUpdateIndices() []uint32     { return d.tagUpdateIndices }
func (d *DFA) TagUpdates() []TransitionUpdate { return d.tagUpdates }
func (d *DFA) Stride() int                    { return d.stride }
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
func (d *DFA) Accepting() []bool { return d.accepting }
func (d *DFA) MatchPriority(s StateID) int {
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
func (d *DFA) SearchState() StateID          { return d.searchState }
func (d *DFA) MatchState() StateID           { return d.matchState }
func (d *DFA) StartUpdates() []PathTagUpdate { return d.startUpdates }
func (d *DFA) HasAnchors() bool              { return d.hasAnchors }
func (d *DFA) TrieRoots() [][]*utf8Node      { return d.trieRoots }

var ErrStateExplosion = fmt.Errorf("regexp: pattern too large or ambiguous")

const MaxDFAMemory = 64 * 1024 * 1024

type dfaStateKey struct {
	nfaKey   string
	isSearch bool
}

const SearchRestartPenalty = 1000000

func NewDFA(prog *syntax.Prog) (*DFA, error) {
	return NewDFAWithMemoryLimit(context.Background(), prog, MaxDFAMemory)
}
func NewDFAWithMemoryLimit(ctx context.Context, prog *syntax.Prog, maxMemory int) (*DFA, error) {
	d := &DFA{numSubexp: prog.NumCap / 2}
	if err := d.build(ctx, prog, maxMemory); err != nil {
		return nil, err
	}
	return d, nil
}

func NewBitParallelDFA(prog *syntax.Prog) *BitParallelDFA {
	if len(prog.Inst) > 64 {
		return nil
	}
	bp := &BitParallelDFA{}
	for i, inst := range prog.Inst {
		switch inst.Op {
		case syntax.InstRune, syntax.InstRune1, syntax.InstRuneAny, syntax.InstRuneAnyNotNL:
			for b := 0; b < 256; b++ {
				if inst.MatchRune(rune(b)) {
					bp.CharMasks[b] |= (1 << i)
				}
			}
		case syntax.InstMatch:
			bp.MatchMask |= (1 << i)
		}
		var visited uint64
		var dfs func(int)
		dfs = func(curr int) {
			if (visited & (1 << curr)) != 0 {
				return
			}
			visited |= (1 << curr)
			bp.Epsilon[i] |= (1 << curr)
			ii := prog.Inst[curr]
			switch ii.Op {
			case syntax.InstAlt, syntax.InstAltMatch:
				dfs(int(ii.Out))
				dfs(int(ii.Arg))
			case syntax.InstCapture, syntax.InstNop, syntax.InstEmptyWidth:
				dfs(int(ii.Out))
			}
		}
		dfs(i)
	}
	return bp
}

type closureCacheKey struct {
	paths   string
	context syntax.EmptyOp
}
type closureResult struct {
	nextClosure []nfaPath
	updates     []PathTagUpdate
}

func (d *DFA) build(ctx context.Context, prog *syntax.Prog, maxMemory int) error {
	cache := newUTF8NodeCache()
	d.hasAnchors = false
	for _, inst := range prog.Inst {
		if inst.Op == syntax.InstEmptyWidth {
			d.hasAnchors = true
			break
		}
	}
	d.stride = 256
	if d.hasAnchors {
		d.stride += numVirtualBytes
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
			fold := inst.Op == syntax.InstRune && (inst.Arg&uint32(syntax.FoldCase) != 0)
			roots = cache.runeRangesToUTF8Trie(inst.Rune, fold)
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
	updateToIdx := make(map[string]uint32)
	addUpdate := func(u TransitionUpdate) uint32 {
		key := serializeUpdate(u)
		if idx, ok := updateToIdx[key]; ok {
			return idx
		}
		idx := uint32(len(d.tagUpdates))
		d.tagUpdates = append(d.tagUpdates, u)
		updateToIdx[key] = idx
		return idx
	}
	closureCache := make(map[closureCacheKey]closureResult)
	getCachedClosure := func(paths []nfaPath, context syntax.EmptyOp) closureResult {
		if len(paths) == 0 {
			return closureResult{}
		}
		minP := paths[0].Priority
		for i := 1; i < len(paths); i++ {
			if paths[i].Priority < minP {
				minP = paths[i].Priority
			}
		}
		key := closureCacheKey{serializeSet(paths), context}
		if res, ok := closureCache[key]; ok {
			if minP == 0 {
				return res
			}
			newClosure := make([]nfaPath, len(res.nextClosure))
			for i, p := range res.nextClosure {
				newClosure[i] = nfaPath{nfaState: p.nfaState, Priority: p.Priority + minP, Tags: p.Tags}
			}
			newUpdates := make([]PathTagUpdate, len(res.updates))
			for i, u := range res.updates {
				newUpdates[i] = PathTagUpdate{RelativePriority: u.RelativePriority + int32(minP), Tags: u.Tags}
			}
			return closureResult{newClosure, newUpdates}
		}

		normPaths := make([]nfaPath, len(paths))
		for i, p := range paths {
			normPaths[i] = nfaPath{nfaState: p.nfaState, Priority: p.Priority - minP, Tags: p.Tags}
		}
		nextClosure, updates := epsilonClosureWithPathTags(normPaths, prog, context)
		res := closureResult{nextClosure, updates}
		closureCache[key] = res

		if minP == 0 {
			return res
		}
		denormClosure := make([]nfaPath, len(nextClosure))
		for i, p := range nextClosure {
			denormClosure[i] = nfaPath{nfaState: p.nfaState, Priority: p.Priority + minP, Tags: p.Tags}
		}
		denormUpdates := make([]PathTagUpdate, len(updates))
		for i, u := range updates {
			denormUpdates[i] = PathTagUpdate{RelativePriority: u.RelativePriority + int32(minP), Tags: u.Tags}
		}
		return closureResult{denormClosure, denormUpdates}
	}

	var errBuild error
	addDfaState := func(closure []nfaPath, isSearch bool) StateID {
		if errBuild != nil {
			return InvalidState
		}
		if (d.numStates+1)*d.stride*8 > maxMemory {
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
			if closure[i].Priority != closure[j].Priority {
				return closure[i].Priority < closure[j].Priority
			}
			return closure[i].Tags < closure[j].Tags
		})
		key := dfaStateKey{serializeSet(closure), isSearch}
		if id, ok := nfaToDfa[key]; ok {
			return id
		}
		id := StateID(len(dfaToNfa))
		nfaToDfa[key] = id
		cleanClosure := make([]nfaPath, len(closure))
		copy(cleanClosure, closure)
		dfaToNfa = append(dfaToNfa, cleanClosure)
		d.stateIsSearch = append(d.stateIsSearch, isSearch)
		isAcc, matchP := false, 1<<30-1
		var matchTags uint64
		minPathP := 1<<30 - 1
		for _, s := range closure {
			if s.Priority < minPathP {
				minPathP = s.Priority
			}
			if prog.Inst[s.ID].Op == syntax.InstMatch && s.node == nil {
				isAcc = true
				if s.Priority < matchP {
					matchP = s.Priority
					matchTags = s.Tags
				}
			}
		}
		d.accepting = append(d.accepting, isAcc)
		d.stateMatchPriority = append(d.stateMatchPriority, matchP)
		d.stateMatchTags = append(d.stateMatchTags, matchTags)
		d.stateIsBestMatch = append(d.stateIsBestMatch, isAcc && (matchP == minPathP))
		for i := 0; i < d.stride; i++ {
			d.transitions = append(d.transitions, InvalidState)
			d.tagUpdateIndices = append(d.tagUpdateIndices, 0)
		}
		d.numStates++
		return id
	}
	defaultStartRes := getCachedClosure([]nfaPath{{nfaState: nfaState{ID: uint32(prog.Start)}}}, 0)
	d.matchState = addDfaState(defaultStartRes.nextClosure, false)
	d.startUpdates = defaultStartRes.updates
	d.searchState = addDfaState(defaultStartRes.nextClosure, true)
	for i := 0; i < len(dfaToNfa); i++ {
		if i%100 == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
		currentClosure, currentDfaID, currentIsSearch := dfaToNfa[i], StateID(i), d.stateIsSearch[i]
		var initialPaths []nfaPath
		if currentIsSearch {
			initialPaths = make([]nfaPath, len(currentClosure)+1)
			copy(initialPaths, currentClosure)
			initialPaths[len(currentClosure)] = nfaPath{nfaState: nfaState{ID: uint32(prog.Start)}, Priority: SearchRestartPenalty}
		} else {
			initialPaths = currentClosure
		}
		searchRes := getCachedClosure(initialPaths, 0)
		for b := 0; b < 256; b++ {
			var nextPaths []nfaPath
			foundMatch := false
			for _, p := range searchRes.nextClosure {
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
						nextPaths = append(nextPaths, nfaPath{nfaState: nfaState{ID: out}, Priority: p.Priority, Tags: p.Tags})
					}
					for _, node := range matchedNodes {
						nextPaths = append(nextPaths, nfaPath{nfaState: nfaState{ID: s.ID, node: node}, Priority: p.Priority, Tags: p.Tags})
					}
				}
			}
			if foundMatch {
				nextRes := getCachedClosure(nextPaths, 0)
				if len(nextRes.nextClosure) == 0 {
					continue
				}
				minP := nextRes.nextClosure[0].Priority
				for _, p := range nextRes.nextClosure {
					if p.Priority < minP {
						minP = p.Priority
					}
				}
				nextDfaID := addDfaState(nextRes.nextClosure, currentIsSearch)
				if errBuild != nil {
					return errBuild
				}
				idx := int(currentDfaID)*d.stride + b

				var postUpdates []PathTagUpdate
				if len(nextRes.updates) > 0 {
					postUpdates = make([]PathTagUpdate, len(nextRes.updates))
					for j, u := range nextRes.updates {
						postUpdates[j] = PathTagUpdate{RelativePriority: u.RelativePriority - int32(minP), Tags: u.Tags}
					}
				}

				if minP != 0 || len(searchRes.updates) > 0 || len(postUpdates) > 0 {
					d.transitions[idx] = nextDfaID | TaggedStateFlag
					d.tagUpdateIndices[idx] = addUpdate(TransitionUpdate{BasePriority: int32(minP), PreUpdates: searchRes.updates, PostUpdates: postUpdates})
				} else {
					d.transitions[idx] = nextDfaID
				}
			} else if currentIsSearch {
				idx := int(currentDfaID)*d.stride + b
				d.transitions[idx] = d.searchState | TaggedStateFlag
				d.tagUpdateIndices[idx] = addUpdate(TransitionUpdate{BasePriority: int32(SearchRestartPenalty)})
			}
		}
		if d.hasAnchors {
			for bit := 0; bit < numVirtualBytes; bit++ {
				op := syntax.EmptyOp(1 << bit)
				var anchorPaths []nfaPath
				if currentIsSearch {
					anchorPaths = make([]nfaPath, len(currentClosure)+1)
					copy(anchorPaths, currentClosure)
					anchorPaths[len(currentClosure)] = nfaPath{nfaState: nfaState{ID: uint32(prog.Start)}, Priority: SearchRestartPenalty}
				} else {
					anchorPaths = currentClosure
				}
				nextRes := getCachedClosure(anchorPaths, op)
				if len(nextRes.nextClosure) == 0 || serializeSet(nextRes.nextClosure) == serializeSet(currentClosure) {
					continue
				}
				minP := nextRes.nextClosure[0].Priority
				for _, p := range nextRes.nextClosure {
					if p.Priority < minP {
						minP = p.Priority
					}
				}
				nextDfaID := addDfaState(nextRes.nextClosure, currentIsSearch)
				if errBuild != nil {
					return errBuild
				}
				idx := int(currentDfaID)*d.stride + 256 + bit

				var postUpdates []PathTagUpdate
				if len(nextRes.updates) > 0 {
					postUpdates = make([]PathTagUpdate, len(nextRes.updates))
					for j, u := range nextRes.updates {
						postUpdates[j] = PathTagUpdate{RelativePriority: u.RelativePriority - int32(minP), Tags: u.Tags}
					}
				}

				if minP != 0 || len(postUpdates) > 0 {
					d.transitions[idx] = nextDfaID | TaggedStateFlag
					d.tagUpdateIndices[idx] = addUpdate(TransitionUpdate{BasePriority: int32(minP), PreUpdates: postUpdates})
				} else {
					d.transitions[idx] = nextDfaID
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
			buf := make([]byte, d.stride*8)
			for b := 0; b < d.stride; b++ {
				idx := i*d.stride + b
				target := d.transitions[idx]
				if target != InvalidState {
					tg := stateToGroup[target&0x7FFFFFFF]
					var updateIdx uint32
					if target < 0 {
						updateIdx = d.tagUpdateIndices[idx] + 1
					}
					off := b * 8
					*(*int32)(unsafe.Pointer(&buf[off])) = tg
					*(*uint32)(unsafe.Pointer(&buf[off+4])) = updateIdx
				} else {
					off := b * 8
					*(*int32)(unsafe.Pointer(&buf[off])) = -1
					*(*uint32)(unsafe.Pointer(&buf[off+4])) = 0
				}
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
	newUpdateIndices := make([]uint32, int(numGroups)*d.stride)
	newAccepting, newPrio, newMatchTags, newBest, newIsSearch := make([]bool, numGroups), make([]int, numGroups), make([]uint64, numGroups), make([]bool, numGroups), make([]bool, numGroups)
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
				newID := StateID(stateToGroup[target&0x7FFFFFFF])
				if target < 0 {
					newTransitions[int(g)*d.stride+b] = newID | TaggedStateFlag
					newUpdateIndices[int(g)*d.stride+b] = d.tagUpdateIndices[oldIdx]
				} else {
					newTransitions[int(g)*d.stride+b] = newID
				}
			} else {
				newTransitions[int(g)*d.stride+b] = InvalidState
			}
		}
	}
	d.transitions, d.tagUpdateIndices, d.accepting, d.stateMatchPriority, d.stateMatchTags, d.stateIsBestMatch, d.stateIsSearch, d.numStates, d.searchState, d.matchState = newTransitions, newUpdateIndices, newAccepting, newPrio, newMatchTags, newBest, newIsSearch, int(numGroups), StateID(stateToGroup[d.searchState]), StateID(stateToGroup[d.matchState])
}

func (d *DFA) computePhase2Metadata() {
	d.isAlwaysTrue, d.warpPoints, d.warpPointState = make([]bool, d.numStates), make([]int16, d.numStates), make([]StateID, d.numStates)
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
		if d.accepting[i] {
			continue
		}
		progressByte, targetState, possible := -1, InvalidState, true
		for b := 0; b < 256; b++ {
			nextRaw := d.transitions[i*d.stride+b]
			if nextRaw == InvalidState {
				continue
			}
			next := nextRaw & 0x7FFFFFFF
			if next == currState {
				continue
			}
			if progressByte == -1 {
				progressByte = b
				targetState = next
			} else {
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
	num := 0
	index, lowlink, onStack, stack := make([]int, d.numStates), make([]int, d.numStates), make([]bool, d.numStates), []int{}
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
			nextRaw := d.transitions[v*d.stride+b]
			if nextRaw == -1 {
				continue
			}
			w := int(nextRaw & 0x7FFFFFFF)
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
			allAcc := true
			for _, s := range component {
				if !d.accepting[s] {
					allAcc = false
					break
				}
			}
			if allAcc {
				allTrans := true
				for _, s := range component {
					for b := 0; b < 256; b++ {
						nextRaw := d.transitions[s*d.stride+b]
						if nextRaw == -1 {
							allTrans = false
							break
						}
						next := int(nextRaw & 0x7FFFFFFF)
						in := false
						for _, cs := range component {
							if cs == next {
								in = true
								break
							}
						}
						if !in && !d.isAlwaysTrue[next] {
							allTrans = false
							break
						}
					}
					if !allTrans {
						break
					}
				}
				if allTrans {
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

func matchesByte(node *utf8Node, b byte) bool {
	for _, r := range node.ranges {
		if b >= r.lo && b <= r.hi {
			return true
		}
	}
	return false
}
func matchesByteFold(node *utf8Node, b byte) bool { return matchesByte(node, b) }

func epsilonClosureWithPathTags(paths []nfaPath, prog *syntax.Prog, context syntax.EmptyOp) ([]nfaPath, []PathTagUpdate) {
	type key struct {
		ID   uint32
		node *utf8Node
		Tags uint64
	}
	best := make(map[key]int)
	type pathWithNewTags struct {
		p    nfaPath
		tags uint64
	}
	stack := make([]pathWithNewTags, len(paths))
	for i, p := range paths {
		stack[i] = pathWithNewTags{p, 0}
	}

	pathTags := make(map[int]*PathTagUpdate)
	for len(stack) > 0 {
		ph := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		p, k := ph.p, key{ph.p.ID, ph.p.node, ph.p.Tags}
		if prio, ok := best[k]; ok && p.Priority >= prio {
			continue
		}
		best[k] = p.Priority

		if ph.tags != 0 {
			update := pathTags[p.Priority]
			if update == nil {
				update = &PathTagUpdate{RelativePriority: int32(p.Priority)}
				pathTags[p.Priority] = update
			}
			update.Tags |= ph.tags
		}

		if p.node == nil {
			inst := prog.Inst[p.ID]
			if inst.Op == syntax.InstMatch {
				continue
			}
			switch inst.Op {
			case syntax.InstAlt, syntax.InstAltMatch:
				stack = append(stack, pathWithNewTags{nfaPath{nfaState: nfaState{ID: inst.Arg}, Priority: p.Priority + 1, Tags: p.Tags}, ph.tags})
				stack = append(stack, pathWithNewTags{nfaPath{nfaState: nfaState{ID: inst.Out}, Priority: p.Priority, Tags: p.Tags}, ph.tags})
			case syntax.InstCapture:
				tagBit := uint64(0)
				if inst.Arg < 64 {
					tagBit = (1 << inst.Arg)
				}
				stack = append(stack, pathWithNewTags{nfaPath{nfaState: nfaState{ID: inst.Out}, Priority: p.Priority, Tags: p.Tags | tagBit}, ph.tags | tagBit})
			case syntax.InstNop:
				stack = append(stack, pathWithNewTags{nfaPath{nfaState: nfaState{ID: inst.Out}, Priority: p.Priority, Tags: p.Tags}, ph.tags})
			case syntax.InstEmptyWidth:
				if syntax.EmptyOp(inst.Arg)&context == syntax.EmptyOp(inst.Arg) {
					stack = append(stack, pathWithNewTags{nfaPath{nfaState: nfaState{ID: inst.Out}, Priority: p.Priority, Tags: p.Tags}, ph.tags})
				}
			}
		}
	}
	var result []nfaPath
	for k, prio := range best {
		result = append(result, nfaPath{nfaState: nfaState{ID: k.ID, node: k.node}, Priority: prio, Tags: k.Tags})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Priority != result[j].Priority {
			return result[i].Priority < result[j].Priority
		}
		if result[i].ID != result[j].ID {
			return result[i].ID < result[j].ID
		}
		return result[i].Tags < result[j].Tags
	})

	var updates []PathTagUpdate
	for _, u := range pathTags {
		updates = append(updates, *u)
	}
	sort.Slice(updates, func(i, j int) bool { return updates[i].RelativePriority < updates[j].RelativePriority })
	return result, updates
}

func serializeSet(set []nfaPath) string {
	if len(set) == 0 {
		return ""
	}
	minP := set[0].Priority
	for i := 1; i < len(set); i++ {
		if set[i].Priority < minP {
			minP = set[i].Priority
		}
	}
	buf := make([]byte, len(set)*20)
	for i, s := range set {
		off := i * 20
		*(*uint32)(unsafe.Pointer(&buf[off])) = s.ID
		var nodeID uint32
		if s.node != nil {
			nodeID = uint32(s.node.ID)
		}
		*(*uint32)(unsafe.Pointer(&buf[off+4])) = nodeID
		*(*uint32)(unsafe.Pointer(&buf[off+8])) = uint32(s.Priority - minP)
		*(*uint64)(unsafe.Pointer(&buf[off+12])) = s.Tags
	}
	return string(buf)
}

func serializeUpdate(u TransitionUpdate) string {
	buf := make([]byte, 8+len(u.PreUpdates)*12+len(u.PostUpdates)*12)
	*(*int32)(unsafe.Pointer(&buf[0])) = u.BasePriority
	*(*int16)(unsafe.Pointer(&buf[4])) = int16(len(u.PreUpdates))
	*(*int16)(unsafe.Pointer(&buf[6])) = int16(len(u.PostUpdates))
	off := 8
	for _, p := range u.PreUpdates {
		*(*int32)(unsafe.Pointer(&buf[off])) = p.RelativePriority
		*(*uint64)(unsafe.Pointer(&buf[off+4])) = p.Tags
		off += 12
	}
	for _, p := range u.PostUpdates {
		*(*int32)(unsafe.Pointer(&buf[off])) = p.RelativePriority
		*(*uint64)(unsafe.Pointer(&buf[off+4])) = p.Tags
		off += 12
	}
	return string(buf)
}
