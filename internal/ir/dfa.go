package ir

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"unicode/utf8"
	"unsafe"

	"github.com/kamichidu/go-regexp-re/syntax"
)

type StateID uint32

const (
	InvalidState uint32 = 0xFFFFFFFF
	// Fixed Canonical Layout: [31: Tagged] [30: Anchor] [29: Warp] [28-22: AnchorMask] [21-0: StateIndex]
	TaggedStateFlag  uint32 = 0x80000000
	AnchorVerifyFlag uint32 = 0x40000000
	WarpStateFlag    uint32 = 0x20000000
	AnchorMask       uint32 = 0x1FC00000
	StateIDMask      uint32 = 0x003FFFFF
)

const MaxDFAMemory = 64 * 1024 * 1024
const SearchRestartPenalty = 1000000

type NFAPath struct {
	ID, NodeID uint32
	Priority   int32
	Anchors    syntax.EmptyOp
	Tags       uint64
}

const NFAPathSize = int(unsafe.Sizeof(NFAPath{}))

type TransitionUpdate struct {
	BasePriority            int32
	PreUpdates, PostUpdates []PathTagUpdate
}

type PathTagUpdate struct {
	RelativePriority, NextPriority int32
	Tags                           uint64
}

type RecapEntry struct {
	InputPriority, NextPriority int16
	PreTags, PostTags           uint64
	WarpTags                    []WarpTagBundle
}

type WarpTagBundle struct {
	Offset int
	Tags   uint64
}

type GroupRecapTable struct{ Transitions [][]RecapEntry }

type DFA struct {
	transitions             []uint32
	tagUpdateIndices        []uint32
	tagUpdates              []TransitionUpdate
	numStates               int
	searchState, matchState uint32
	numSubexp               int
	Naked                   bool
	stateIsSearch           []bool
	accepting               []bool
	acceptingGuards         []syntax.EmptyOp
	stateMatchPriority      []int
	stateIsBestMatch        []bool
	stateMinPriority        []int32
	recapTables             []GroupRecapTable
	storage                 NFAPathStorage
	nodes                   []*UTF8Node
	maskStride              int
	stateToMask             []uint64
	startUpdates            []PathTagUpdate
	stateEntryTags          [][]PathTagUpdate
	hasAnchors              bool
}

type NFAPathStorage interface {
	Put(id uint32, paths []NFAPath) error
	Get(id uint32, buf []NFAPath) ([]NFAPath, error)
	Close() error
}

type memoryNfaSetStorage struct {
	data [][]NFAPath
	mu   sync.RWMutex
}

func (s *memoryNfaSetStorage) Put(id uint32, paths []NFAPath) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := int(id & StateIDMask)
	if idx >= len(s.data) {
		s.data = append(s.data, make([][]NFAPath, 1024)...)
	}
	s.data[idx] = append([]NFAPath(nil), paths...)
	return nil
}
func (s *memoryNfaSetStorage) Get(id uint32, buf []NFAPath) ([]NFAPath, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx := int(id & StateIDMask)
	if idx >= len(s.data) {
		return nil, nil
	}
	return s.data[idx], nil
}
func (s *memoryNfaSetStorage) Close() error { return nil }

func (d *DFA) IsNaked() bool                  { return d.Naked }
func (d *DFA) NumStates() int                 { return d.numStates }
func (d *DFA) RecapTables() []GroupRecapTable { return d.recapTables }
func (d *DFA) StateMinPriority(id uint32) int32 {
	idx := int(id & StateIDMask)
	if idx >= d.numStates {
		return 0
	}
	return d.stateMinPriority[idx]
}
func (d *DFA) Transitions() []uint32          { return d.transitions }
func (d *DFA) TagUpdateIndices() []uint32     { return d.tagUpdateIndices }
func (d *DFA) TagUpdates() []TransitionUpdate { return d.tagUpdates }
func (d *DFA) MatchPriority(id uint32) int {
	idx := int(id & StateIDMask)
	if idx >= d.numStates {
		return 1<<30 - 1
	}
	return d.stateMatchPriority[idx]
}
func (d *DFA) IsBestMatch(id uint32) bool {
	idx := int(id & StateIDMask)
	if idx >= d.numStates {
		return false
	}
	return d.stateIsBestMatch[idx]
}
func (d *DFA) IsAccepting(id uint32) bool {
	idx := int(id & StateIDMask)
	if idx >= d.numStates {
		return false
	}
	return d.accepting[idx]
}
func (d *DFA) Accepting() []bool                 { return d.accepting }
func (d *DFA) AcceptingGuards() []syntax.EmptyOp { return d.acceptingGuards }
func (d *DFA) SearchState() uint32               { return d.searchState }
func (d *DFA) MatchState() uint32                { return d.matchState }
func (d *DFA) HasAnchors() bool                  { return d.hasAnchors }
func (d *DFA) UsedAnchors() syntax.EmptyOp       { return 0 }
func (d *DFA) MaskStride() int                   { return d.maskStride }
func (d *DFA) Next(currentID uint32, b int) uint32 {
	idx := int(currentID & StateIDMask)
	if idx >= d.numStates || b < 0 || b >= 256 {
		return InvalidState
	}
	return d.transitions[idx*256+b]
}
func (d *DFA) AnchorNext(id uint32, bit int) uint32 { return InvalidState }
func (d *DFA) StartUpdates() []PathTagUpdate        { return d.startUpdates }

type ClosureResult struct {
	NextClosure  []NFAPath
	Updates      []PathTagUpdate
	MatchAnchors syntax.EmptyOp
}
type dfaStateKey struct {
	hash          [2]uint64
	matchPriority int
	isSearch      bool
}

func (d *DFA) build(ctx context.Context, s *syntax.Regexp, prog *syntax.Prog, maxMemory int) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if s, ok := r.(string); ok && s == "regexp: pattern too large or ambiguous" {
				err = fmt.Errorf("%s", s)
			} else {
				panic(r)
			}
		}
	}()
	if err := checkCompatibility(s); err != nil {
		return err
	}
	if err := checkEpsilonLoop(prog); err != nil {
		return err
	}
	for _, inst := range prog.Inst {
		if inst.Op == syntax.InstEmptyWidth {
			d.hasAnchors = true
			break
		}
	}
	d.maskStride = (len(prog.Inst) + 63) / 64
	d.storage = &memoryNfaSetStorage{data: make([][]NFAPath, 1024)}
	nfaToDfa := make(map[dfaStateKey]uint32)
	closureCache := make(map[uint64]ClosureResult)
	getCachedClosure := func(paths []NFAPath) ClosureResult {
		if len(paths) == 0 {
			return ClosureResult{}
		}
		// Calculate the minimum priority to make the hash invariant to absolute offsets
		minP := paths[0].Priority
		for _, p := range paths {
			if p.Priority < minP {
				minP = p.Priority
			}
		}

		h := uint64(14695981039346656037)
		for _, p := range paths {
			h = (h ^ uint64(p.ID)) * 1099511628211
			h = (h ^ uint64(p.NodeID)) * 1099511628211
			// Use normalized priority for the hash
			h = (h ^ uint64(p.Priority-minP)) * 1099511628211
			h = (h ^ uint64(p.Tags)) * 1099511628211
			h = (h ^ uint64(p.Anchors)) * 1099511628211
		}
		if res, ok := closureCache[h]; ok {
			// Return a copy of Updates to prevent callers from corrupting the cache
			updatesCopy := make([]PathTagUpdate, len(res.Updates))
			copy(updatesCopy, res.Updates)
			resCopy := res
			resCopy.Updates = updatesCopy
			return resCopy
		}

		// Create a normalized copy of paths for epsilonClosure
		normPaths := make([]NFAPath, len(paths))
		copy(normPaths, paths)
		for i := range normPaths {
			normPaths[i].Priority -= minP
		}

		res := epsilonClosureWithAnchorWall(prog, normPaths)
		closureCache[h] = res

		// Return a copy to prevent accidental corruption
		updatesCopy := make([]PathTagUpdate, len(res.Updates))
		copy(updatesCopy, res.Updates)
		resCopy := res
		resCopy.Updates = updatesCopy
		return resCopy
	}
	maxStates := maxMemory / 2048
	if maxStates < 100 {
		maxStates = 100
	}

	addDfaState := func(closure []NFAPath, updates []PathTagUpdate, matchAnchors syntax.EmptyOp, isSearch bool) uint32 {
		matchP := 1<<30 - 1
		for _, s := range closure {
			if prog.Inst[s.ID].Op == syntax.InstMatch && s.NodeID == 0 {
				if int(s.Priority) < matchP {
					matchP = int(s.Priority)
				}
			}
		}
		keyPrio := matchP
		key := dfaStateKey{hashSet(closure, d.Naked), keyPrio, isSearch}
		if id, ok := nfaToDfa[key]; ok {
			return id
		}
		if d.numStates >= maxStates {
			panic(fmt.Sprintf("regexp: pattern too large or ambiguous (states: %d, max: %d)", d.numStates, maxStates))
		}
		id := uint32(d.numStates)
		nfaToDfa[key] = id
		d.storage.Put(id, closure)
		d.stateIsSearch = append(d.stateIsSearch, isSearch)
		isAcc, matchP := false, 1<<30-1
		minP := int32(1<<30 - 1)
		if len(closure) > 0 {
			minP = closure[0].Priority
			for _, s := range closure {
				if s.Priority < minP {
					minP = s.Priority
				}
			}
		}
		for _, s := range closure {
			if prog.Inst[s.ID].Op == syntax.InstMatch && s.NodeID == 0 {
				isAcc = true
				prio := int(s.Priority - minP)
				if prio < matchP {
					matchP = prio
				}
			}
		}
		d.stateMinPriority = append(d.stateMinPriority, minP)
		d.stateMatchPriority = append(d.stateMatchPriority, matchP)
		d.stateEntryTags = append(d.stateEntryTags, updates)
		d.stateIsBestMatch = append(d.stateIsBestMatch, isAcc && matchP <= int(minP))

		d.accepting = append(d.accepting, isAcc)
		d.acceptingGuards = append(d.acceptingGuards, matchAnchors)
		for i := 0; i < 256; i++ {
			d.transitions = append(d.transitions, InvalidState)
		}
		d.numStates++
		return id
	}

	startRes := getCachedClosure([]NFAPath{{ID: uint32(prog.Start), Priority: 0}})
	d.matchState = addDfaState(startRes.NextClosure, startRes.Updates, startRes.MatchAnchors, false)
	d.startUpdates = startRes.Updates
	d.searchState = addDfaState(startRes.NextClosure, startRes.Updates, startRes.MatchAnchors, true)

	d.recapTables = []GroupRecapTable{{Transitions: make([][]RecapEntry, 0, 1024)}}
	d.tagUpdateIndices = make([]uint32, 0, 1024)
	d.tagUpdates = make([]TransitionUpdate, 0, 1024)
	scratchBuf := make([]NFAPath, 0, 1024)
	nextPaths := make([]NFAPath, 0, 1024)
	// Cache Tries by their content (rune ranges) to reuse them within this build.
	contentTries := make(map[string]*Trie)
	getTrie := func(id uint32) *Trie {
		inst := prog.Inst[id]
		var key string
		switch inst.Op {
		case syntax.InstRune, syntax.InstRune1:
			key = string(inst.Rune)
		case syntax.InstRuneAny:
			return GetAnyRuneTrie()
		case syntax.InstRuneAnyNotNL:
			return GetAnyRuneNotNLTrie()
		default:
			return nil
		}

		if t, ok := contentTries[key]; ok {
			return t
		}

		var t *Trie
		switch inst.Op {
		case syntax.InstRune:
			t = NewTrie()
			for i := 0; i+1 < len(inst.Rune); i += 2 {
				t.AddRuneRange(inst.Rune[i], inst.Rune[i+1])
			}
		case syntax.InstRune1:
			t = NewTrie()
			if len(inst.Rune) > 0 {
				t.AddRuneRange(inst.Rune[0], inst.Rune[0])
			}
		}
		contentTries[key] = t
		return t
	}

	processed := 0
	for processed < d.numStates {
		i := uint32(processed)
		processed++
		currentClosure, _ := d.storage.Get(i, scratchBuf)
		scratchBuf = currentClosure

		for b := 0; b < 256; b++ {
			nextPaths = nextPaths[:0]
			var preGuard syntax.EmptyOp
			foundEdge := false

			// Determine if this transition is warpable.
			isWarpable := b >= 0x80
			hasMultiByte := false
			if isWarpable {
				for _, p := range currentClosure {
					if p.NodeID != 0 {
						continue
					}
					t := getTrie(p.ID)
					if t != nil {
						if _, ok := t.GetTransitions(p.NodeID, byte(b)); ok {
							hasMultiByte = true
							if t != GetAnyRuneTrie() && t != GetAnyRuneNotNLTrie() {
								isWarpable = false
								break
							}
						}
					}
				}
			}
			isWarpable = isWarpable && hasMultiByte

			for _, p := range currentClosure {
				inst := prog.Inst[p.ID]
				match := false
				var nextNodeID uint32

				t := getTrie(p.ID)
				if t != nil {
					if next, ok := t.GetTransitions(p.NodeID, byte(b)); ok {
						match = true
						nextNodeID = next
					}
				}

				if match {
					if !foundEdge {
						preGuard = p.Anchors
						foundEdge = true
					} else {
						preGuard |= p.Anchors
					}

					nextID := inst.Out
					if nextNodeID != UTF8MatchCompleted && !isWarpable {
						nextID = p.ID
					} else {
						nextNodeID = 0
					}

					// Merge with existing path in nextPaths if ID/Priority/Anchors/NodeID match
					found := false
					for j := range nextPaths {
						if nextPaths[j].ID == nextID && nextPaths[j].NodeID == nextNodeID && nextPaths[j].Priority == p.Priority && nextPaths[j].Anchors == p.Anchors {
							nextPaths[j].Tags |= p.Tags
							found = true
							break
						}
					}
					if !found {
						nextPaths = append(nextPaths, NFAPath{ID: nextID, NodeID: nextNodeID, Priority: p.Priority, Tags: p.Tags, Anchors: 0})
					}
				}
			}
			if d.stateIsSearch[i] {
				nextPaths = append(nextPaths, NFAPath{ID: uint32(prog.Start), Priority: SearchRestartPenalty})
			}
			if len(nextPaths) == 0 {
				continue
			}

			nextRes := getCachedClosure(nextPaths)
			if len(nextRes.NextClosure) == 0 {
				continue
			}

			nextDfaID := addDfaState(nextRes.NextClosure, nextRes.Updates, nextRes.MatchAnchors, d.stateIsSearch[i])
			idx := (int(i) << 8) | b
			rawNext := nextDfaID
			if preGuard != 0 {
				rawNext |= AnchorVerifyFlag | (uint32(preGuard) << 22)
			}
			if isWarpable {
				rawNext |= WarpStateFlag
			}

			// The penalty/priority shift incurred during normalization of the next state.
			minNextPrio := int32(1<<30 - 1)
			for _, p := range nextPaths {
				if p.Priority < minNextPrio {
					minNextPrio = p.Priority
				}
			}

			for len(d.tagUpdateIndices) <= idx {
				d.tagUpdateIndices = append(d.tagUpdateIndices, 0xFFFFFFFF)
			}

			// Always record updates in RecapTable to ensure Pass 2 can trace the path
			if len(nextRes.Updates) > 0 {
				uIdx := uint32(len(d.tagUpdates))
				d.tagUpdates = append(d.tagUpdates, TransitionUpdate{
					BasePriority: minNextPrio - d.stateMinPriority[i],
					PreUpdates:   nextRes.Updates,
				})
				d.tagUpdateIndices[idx] = uIdx
				rawNext |= TaggedStateFlag
			}

			d.transitions[idx] = rawNext
			for len(d.recapTables[0].Transitions) <= idx {
				d.recapTables[0].Transitions = append(d.recapTables[0].Transitions, nil)
			}

			// Entry updates for the CURRENT state (delta tags from the closure that reached here)
			currentEntryUpdates := d.stateEntryTags[i]

			var entries []RecapEntry
			for _, u := range nextRes.Updates {
				// Find delta tags generated just before consuming this byte.
				var preByteTags uint64
				for _, eu := range currentEntryUpdates {
					if eu.NextPriority == u.RelativePriority {
						preByteTags = eu.Tags
						break
					}
				}

				entries = append(entries, RecapEntry{
					InputPriority: int16(u.RelativePriority),
					NextPriority:  int16(u.NextPriority),
					PreTags:       preByteTags,
					PostTags:      u.Tags,
				})
			}
			d.recapTables[0].Transitions[idx] = entries

		}
	}
	return nil
}

func epsilonClosureWithAnchorWall(prog *syntax.Prog, paths []NFAPath) ClosureResult {
	type stateKey struct {
		ID, NodeID uint32
		Anchors    syntax.EmptyOp
	}
	// Track min priority reached per state PER source priority.
	minPriority := make(map[int32]map[stateKey]int32)

	type pathWithMeta struct {
		p          NFAPath
		newTags    uint64
		sourcePrio int32
		sourceTags uint64
	}
	stack := make([]pathWithMeta, 0, len(paths))
	var updates []PathTagUpdate
	for _, p := range paths {
		if _, ok := minPriority[p.Priority]; !ok {
			minPriority[p.Priority] = make(map[stateKey]int32)
		}
		minPriority[p.Priority][stateKey{p.ID, p.NodeID, p.Anchors}] = p.Priority
		stack = append(stack, pathWithMeta{p, 0, p.Priority, p.Tags})

		// Initial path identity must be recorded as a potential transition target.
		inst := prog.Inst[p.ID]
		if p.NodeID != 0 || !isEpsilon(inst.Op) {
			updates = append(updates, PathTagUpdate{
				RelativePriority: p.Priority,
				NextPriority:     p.Priority,
				Tags:             0,
			})
		}
	}

	var resultPathsMap = make(map[stateKey]NFAPath)
	var matchAnchors syntax.EmptyOp
	for len(stack) > 0 {
		ph := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		p := ph.p

		sk := stateKey{p.ID, p.NodeID, p.Anchors}
		if p.Priority > minPriority[ph.sourcePrio][sk] {
			continue
		}

		inst := prog.Inst[p.ID]
		if p.NodeID != 0 || !isEpsilon(inst.Op) {
			rk := stateKey{p.ID, p.NodeID, p.Anchors}
			p.Tags |= ph.newTags

			// Always record the update for this specific source priority.
			// RECORD ONLY delta tags (ph.newTags) for the RecapTable.
			updates = append(updates, PathTagUpdate{
				RelativePriority: ph.sourcePrio,
				NextPriority:     p.Priority,
				Tags:             ph.newTags,
			})

			if existing, ok := resultPathsMap[rk]; !ok || p.Priority < existing.Priority {
				resultPathsMap[rk] = p
			} else if p.Priority == existing.Priority {
				existing.Tags |= p.Tags
				resultPathsMap[rk] = existing
			}
			if p.NodeID == 0 && inst.Op == syntax.InstMatch {
				matchAnchors |= p.Anchors
			}
			continue
		}

		// Epsilon transition
		switch inst.Op {
		case syntax.InstAlt, syntax.InstAltMatch:
			for _, next := range []struct {
				id uint32
				p  int32
			}{{inst.Arg, p.Priority + 1}, {inst.Out, p.Priority}} {
				nsk := stateKey{next.id, 0, p.Anchors}
				if _, ok := minPriority[ph.sourcePrio]; !ok {
					minPriority[ph.sourcePrio] = make(map[stateKey]int32)
				}
				if m, ok := minPriority[ph.sourcePrio][nsk]; !ok || next.p <= m {
					minPriority[ph.sourcePrio][nsk] = next.p
					stack = append(stack, pathWithMeta{
						p:          NFAPath{ID: next.id, Priority: next.p, Anchors: p.Anchors, Tags: p.Tags},
						newTags:    ph.newTags,
						sourcePrio: ph.sourcePrio,
						sourceTags: ph.sourceTags,
					})
				}
			}
		case syntax.InstCapture:
			tagBit := uint64(1 << inst.Arg)
			nsk := stateKey{inst.Out, 0, p.Anchors}
			if _, ok := minPriority[ph.sourcePrio]; !ok {
				minPriority[ph.sourcePrio] = make(map[stateKey]int32)
			}
			if m, ok := minPriority[ph.sourcePrio][nsk]; !ok || p.Priority <= m {
				minPriority[ph.sourcePrio][nsk] = p.Priority

				// Standard TDFA logic for capturing groups in loops:
				// If a tag is already set in the cumulative path (p.Tags),
				// do NOT add it to newTags for the current transition.
				nextNewTags := ph.newTags
				if (p.Tags & tagBit) == 0 {
					nextNewTags |= tagBit
				}

				stack = append(stack, pathWithMeta{
					p:          NFAPath{ID: inst.Out, Priority: p.Priority, Anchors: p.Anchors, Tags: p.Tags | tagBit},
					newTags:    nextNewTags,
					sourcePrio: ph.sourcePrio,
					sourceTags: ph.sourceTags,
				})
			}
		case syntax.InstEmptyWidth:
			newAnchors := p.Anchors | syntax.EmptyOp(inst.Arg)
			nsk := stateKey{inst.Out, 0, newAnchors}
			if _, ok := minPriority[ph.sourcePrio]; !ok {
				minPriority[ph.sourcePrio] = make(map[stateKey]int32)
			}
			if m, ok := minPriority[ph.sourcePrio][nsk]; !ok || p.Priority <= m {
				minPriority[ph.sourcePrio][nsk] = p.Priority
				stack = append(stack, pathWithMeta{
					p:          NFAPath{ID: inst.Out, Priority: p.Priority, Anchors: newAnchors, Tags: p.Tags},
					newTags:    ph.newTags,
					sourcePrio: ph.sourcePrio,
					sourceTags: ph.sourceTags,
				})
			}
		case syntax.InstNop:
			nsk := stateKey{inst.Out, 0, p.Anchors}
			if _, ok := minPriority[ph.sourcePrio]; !ok {
				minPriority[ph.sourcePrio] = make(map[stateKey]int32)
			}
			if m, ok := minPriority[ph.sourcePrio][nsk]; !ok || p.Priority <= m {
				minPriority[ph.sourcePrio][nsk] = p.Priority
				stack = append(stack, pathWithMeta{
					p:          NFAPath{ID: inst.Out, Priority: p.Priority, Anchors: p.Anchors, Tags: p.Tags},
					newTags:    ph.newTags,
					sourcePrio: ph.sourcePrio,
					sourceTags: ph.sourceTags,
				})
			}
		}
	}

	var resPaths []NFAPath
	for _, p := range resultPathsMap {
		resPaths = append(resPaths, p)
	}
	minTotalPrio := int32(1 << 30)
	if len(resPaths) > 0 {
		minTotalPrio = resPaths[0].Priority
		for _, p := range resPaths {
			if p.Priority < minTotalPrio {
				minTotalPrio = p.Priority
			}
		}
		for i := range resPaths {
			resPaths[i].Priority -= minTotalPrio
		}
		for i := range updates {
			updates[i].NextPriority -= minTotalPrio
		}
	}
	sort.Slice(resPaths, func(i, j int) bool {
		if resPaths[i].ID != resPaths[j].ID {
			return resPaths[i].ID < resPaths[j].ID
		}
		if resPaths[i].NodeID != resPaths[j].NodeID {
			return resPaths[i].NodeID < resPaths[j].NodeID
		}
		if resPaths[i].Priority != resPaths[j].Priority {
			return resPaths[i].Priority < resPaths[j].Priority
		}
		if resPaths[i].Anchors != resPaths[j].Anchors {
			return resPaths[i].Anchors < resPaths[j].Anchors
		}
		return resPaths[i].Tags < resPaths[j].Tags
	})
	return ClosureResult{resPaths, updates, matchAnchors}
}

func isEpsilon(op syntax.InstOp) bool {
	switch op {
	case syntax.InstAlt, syntax.InstAltMatch, syntax.InstCapture, syntax.InstNop, syntax.InstEmptyWidth:
		return true
	}
	return false
}

func checkEpsilonLoop(prog *syntax.Prog) error {
	visited := make([]bool, len(prog.Inst))
	onStack := make([]bool, len(prog.Inst))

	var dfs func(int) error
	dfs = func(id int) error {
		if onStack[id] {
			return &syntax.UnsupportedError{Op: "epsilon loop"}
		}
		if visited[id] {
			return nil
		}

		visited[id] = true
		onStack[id] = true
		defer func() { onStack[id] = false }()

		inst := prog.Inst[id]
		if isEpsilon(inst.Op) {
			if err := dfs(int(inst.Out)); err != nil {
				return err
			}
			if inst.Op == syntax.InstAlt || inst.Op == syntax.InstAltMatch {
				if err := dfs(int(inst.Arg)); err != nil {
					return err
				}
			}
		}
		return nil
	}

	for i := range prog.Inst {
		if !visited[i] {
			if err := dfs(i); err != nil {
				return err
			}
		}
	}
	return nil
}

func hashSet(paths []NFAPath, naked bool) [2]uint64 {
	var h1 uint64 = 14695981039346656037
	for _, p := range paths {
		h1 ^= uint64(p.ID)
		h1 *= 1099511628211
		h1 ^= uint64(p.NodeID)
		h1 *= 1099511628211
	}
	return [2]uint64{h1, 0}
}

func NewDFAWithMemoryLimit(ctx context.Context, s *syntax.Regexp, prog *syntax.Prog, maxMemory int, naked bool) (*DFA, error) {
	d := &DFA{Naked: naked, numSubexp: prog.NumCap / 2}
	if err := d.build(ctx, s, prog, maxMemory); err != nil {
		return nil, err
	}
	return d, nil
}

type BitParallelDFA struct {
	CharMasks         [256]uint64
	AnchorMasks       [6]uint64
	ContextMasks      [64]uint64
	SuccessorTable    [8][256]uint64
	MatchMask         uint64
	MatchMasks        [64]uint64
	StartMasks        [64]uint64
	CaptureMasks      [128]uint64
	IsNonGreedy       bool
	AltMatchMasks     uint64
	EpsilonMasks      [64]uint64
	PreEpsilonMasks   [64]uint64
	ContextEpsMask    [64]uint64
	ReachableToMatch  uint64
	ReverseSuccessors [64]uint64
	IsGreedy          bool
}

func (bp *BitParallelDFA) HasAnchors() bool {
	for _, m := range bp.AnchorMasks {
		if m != 0 {
			return true
		}
	}
	return false
}
func NewBitParallelDFA(prog *syntax.Prog) *BitParallelDFA { return nil }
func (d *DFA) CanReachPriority(fromState, toState uint32, context syntax.EmptyOp, p_in, p_out int32) bool {
	return false
}
func (d *DFA) registerNodes(node *UTF8Node, nodes *[]*UTF8Node) {}
func (d *DFA) computePhase2Metadata(prog *syntax.Prog)          {}
func (d *DFA) ReachableToMatch(s uint32) uint64                 { return 0 }
func (d *DFA) StateToMasks(s uint32) []uint64                   { return nil }
func (d *DFA) StateToMask(s uint32) uint64                      { return 0 }
func (d *DFA) WarpPoint(s uint32) int                           { return -1 }
func (d *DFA) WarpPointState(s uint32) uint32                   { return InvalidState }
func (d *DFA) WarpPointGuard(s uint32) syntax.EmptyOp           { return 0 }
func (d *DFA) MaxInst() int                                     { return 0 }
func (d *DFA) minimize()                                        {}

func checkCompatibility(re *syntax.Regexp) error {
	if re == nil {
		return nil
	}
	switch re.Op {
	case syntax.OpCapture:
		if hasEmptyAlternative(re.Sub[0]) {
			return &syntax.UnsupportedError{Op: "empty alternative in capture"}
		}
	case syntax.OpQuest:
		if hasCapture(re.Sub[0]) && matchesEmpty(re.Sub[0]) {
			return &syntax.UnsupportedError{Op: "optional empty capture"}
		}
	}
	for _, sub := range re.Sub {
		if err := checkCompatibility(sub); err != nil {
			return err
		}
	}
	return nil
}

func simplifyRegexp(re *syntax.Regexp) *syntax.Regexp {
	for re.Op == syntax.OpCapture {
		re = re.Sub[0]
	}
	return re
}

func hasCapture(re *syntax.Regexp) bool {
	if re.Op == syntax.OpCapture {
		return true
	}
	for _, sub := range re.Sub {
		if hasCapture(sub) {
			return true
		}
	}
	return false
}

func hasEmptyAlternative(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpEmptyMatch:
		return true
	case syntax.OpCapture:
		return hasEmptyAlternative(re.Sub[0])
	case syntax.OpAlternate:
		for _, sub := range re.Sub {
			if hasEmptyAlternative(sub) {
				return true
			}
		}
	case syntax.OpConcat:
		// If a concat consists only of empty matches, it's an empty alternative
		if len(re.Sub) == 0 {
			return true
		}
		for _, sub := range re.Sub {
			if !hasEmptyAlternative(sub) {
				return false
			}
		}
		return true
	}
	return false
}

func isGreedyQuantifier(re *syntax.Regexp) bool {
	return (re.Op == syntax.OpStar || re.Op == syntax.OpPlus || re.Op == syntax.OpQuest || re.Op == syntax.OpRepeat) && (re.Flags&syntax.NonGreedy == 0)
}

type byteSet [4]uint64 // 256 bits

func (s *byteSet) set(b byte) { s[b>>6] |= 1 << (b & 63) }
func (s *byteSet) overlaps(other byteSet) bool {
	return (s[0]&other[0]) != 0 || (s[1]&other[1]) != 0 || (s[2]&other[2]) != 0 || (s[3]&other[3]) != 0
}

func getFirstBytes(re *syntax.Regexp) byteSet {
	var set byteSet
	switch re.Op {
	case syntax.OpLiteral:
		if len(re.Rune) > 0 {
			var buf [4]byte
			utf8.EncodeRune(buf[:], re.Rune[0])
			set.set(buf[0])
		}
	case syntax.OpCharClass:
		for i := 0; i < len(re.Rune); i += 2 {
			lo, hi := re.Rune[i], re.Rune[i+1]
			if lo <= 0x7F && hi <= 0x7F {
				for b := byte(lo); b <= byte(hi); b++ {
					set.set(b)
				}
			} else {
				var buf [4]byte
				utf8.EncodeRune(buf[:], lo)
				set.set(buf[0])
				if lo != hi {
					utf8.EncodeRune(buf[:], hi)
					set.set(buf[0])
				}
			}
		}
	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		for b := 0; b < 256; b++ {
			set.set(byte(b))
		}
	case syntax.OpCapture, syntax.OpStar, syntax.OpPlus, syntax.OpQuest, syntax.OpRepeat:
		return getFirstBytes(re.Sub[0])
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			s := getFirstBytes(sub)
			for i := range set {
				set[i] |= s[i]
			}
			if !matchesEmpty(sub) {
				break
			}
		}
	case syntax.OpAlternate:
		for _, sub := range re.Sub {
			s := getFirstBytes(sub)
			for i := range set {
				set[i] |= s[i]
			}
		}
	}
	return set
}

func matchesEmpty(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpEmptyMatch, syntax.OpStar, syntax.OpQuest:
		return true
	case syntax.OpRepeat:
		return re.Min == 0
	case syntax.OpCapture:
		return matchesEmpty(re.Sub[0])
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			if !matchesEmpty(sub) {
				return false
			}
		}
		return true
	case syntax.OpAlternate:
		for _, sub := range re.Sub {
			if matchesEmpty(sub) {
				return true
			}
		}
		return false
	}
	return false
}

func overlaps(a, b *syntax.Regexp) bool {
	sa := getFirstBytes(a)
	sb := getFirstBytes(b)
	return sa.overlaps(sb)
}
