package ir

import (
	"context"
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
	stateMatchTags          []uint64
	stateIsBestMatch        []bool
	stateMinPriority        []int32
	recapTables             []GroupRecapTable
	storage                 NFAPathStorage
	nodes                   []*UTF8Node
	maskStride              int
	stateToMask             []uint64
	startUpdates            []PathTagUpdate
	stateMatchUpdates       [][]PathTagUpdate
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
func (d *DFA) MatchUpdates(id uint32) []PathTagUpdate {
	idx := int(id & StateIDMask)
	if idx >= d.numStates {
		return nil
	}
	return d.stateMatchUpdates[idx]
}

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

func (d *DFA) build(ctx context.Context, prog *syntax.Prog, maxMemory int) error {
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
		h := uint64(14695981039346656037)
		for _, p := range paths {
			h ^= uint64(p.ID)
			h *= 1099511628211
			h ^= uint64(p.NodeID)
			h *= 1099511628211
			h ^= uint64(p.Priority)
			h *= 1099511628211
			h ^= uint64(p.Anchors)
			h *= 1099511628211
			h ^= uint64(p.Tags)
			h *= 1099511628211
		}
		if res, ok := closureCache[h]; ok {
			return res
		}
		res := epsilonClosureWithAnchorWall(prog, paths)
		closureCache[h] = res
		return res
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
		key := dfaStateKey{hashSet(closure, d.Naked), matchP, isSearch}
		if id, ok := nfaToDfa[key]; ok {
			return id
		}
		id := uint32(d.numStates)
		nfaToDfa[key] = id
		d.storage.Put(id, closure)
		d.stateIsSearch = append(d.stateIsSearch, isSearch)
		isAcc, matchP := false, 1<<30-1
		var matchTags uint64
		var matchUpdates []PathTagUpdate
		minP := int32(1<<30 - 1)
		for _, s := range closure {
			if s.Priority < minP {
				minP = s.Priority
			}
			if prog.Inst[s.ID].Op == syntax.InstMatch && s.NodeID == 0 {
				isAcc = true
				prio := int(s.Priority)
				if prio < matchP {
					matchP = prio
					matchTags = s.Tags
				}
				matchUpdates = append(matchUpdates, PathTagUpdate{RelativePriority: s.Priority, NextPriority: -1, Tags: s.Tags})
			}
		}
		d.stateMinPriority = append(d.stateMinPriority, minP)
		d.stateMatchPriority = append(d.stateMatchPriority, matchP)
		d.stateMatchTags = append(d.stateMatchTags, matchTags)
		d.stateMatchUpdates = append(d.stateMatchUpdates, matchUpdates)
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
			for _, p := range currentClosure {
				inst := prog.Inst[p.ID]
				match := false
				if p.NodeID == 0 {
					if inst.Op == syntax.InstRune || inst.Op == syntax.InstRune1 || inst.Op == syntax.InstRuneAny || inst.Op == syntax.InstRuneAnyNotNL {
						if inst.MatchRune(rune(b)) {
							match = true
						} else if b >= 0x80 {
							for _, r := range inst.Rune {
								var buf [4]byte
								n := utf8.EncodeRune(buf[:], r)
								if n > 1 && buf[0] == byte(b) {
									match = true
									break
								}
							}
						}
					}
				}
				if match {
					if !foundEdge {
						preGuard = p.Anchors
						foundEdge = true
					} else {
						preGuard |= p.Anchors
					}
					nextPaths = append(nextPaths, NFAPath{ID: inst.Out, Priority: p.Priority, Tags: p.Tags})
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
			if b >= 0x80 {
				isLead := false
				for _, p := range currentClosure {
					if p.NodeID == 0 {
						inst := prog.Inst[p.ID]
						for _, r := range inst.Rune {
							var buf [4]byte
							n := utf8.EncodeRune(buf[:], r)
							if n > 1 && buf[0] == byte(b) {
								isLead = true
								break
							}
						}
					}
					if isLead {
						break
					}
				}
				if isLead {
					rawNext |= WarpStateFlag
				}
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
			uIdx := uint32(len(d.tagUpdates))
			d.tagUpdates = append(d.tagUpdates, TransitionUpdate{
				BasePriority: minNextPrio,
				PreUpdates:   nextRes.Updates,
			})
			d.tagUpdateIndices[idx] = uIdx
			rawNext |= TaggedStateFlag

			d.transitions[idx] = rawNext
			for len(d.recapTables[0].Transitions) <= idx {
				d.recapTables[0].Transitions = append(d.recapTables[0].Transitions, nil)
			}
			var entries []RecapEntry
			for _, u := range nextRes.Updates {
				entries = append(entries, RecapEntry{
					InputPriority: int16(u.RelativePriority - d.stateMinPriority[i]),
					NextPriority:  int16(u.NextPriority),
					PreTags:       u.Tags,
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
	minPriority := make(map[stateKey]int32)
	for _, p := range paths {
		if prio, ok := minPriority[stateKey{p.ID, p.NodeID, p.Anchors}]; !ok || p.Priority < prio {
			minPriority[stateKey{p.ID, p.NodeID, p.Anchors}] = p.Priority
		}
	}
	type pathWithMeta struct {
		p          NFAPath
		newTags    uint64
		sourcePrio int32
	}
	stack := make([]pathWithMeta, len(paths))
	for i, p := range paths {
		stack[i] = pathWithMeta{p, 0, p.Priority}
	}

	var updates []PathTagUpdate
	var resultPathsMap = make(map[stateKey]NFAPath)
	var matchAnchors syntax.EmptyOp
	for len(stack) > 0 {
		ph := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		p := ph.p
		if p.Priority > minPriority[stateKey{p.ID, p.NodeID, p.Anchors}] {
			continue
		}

		inst := prog.Inst[p.ID]
		if p.NodeID != 0 || !isEpsilon(inst.Op) {
			rk := stateKey{p.ID, p.NodeID, p.Anchors}
			if existing, ok := resultPathsMap[rk]; !ok || p.Priority < existing.Priority {
				resultPathsMap[rk] = p
				updates = append(updates, PathTagUpdate{RelativePriority: ph.sourcePrio, NextPriority: p.Priority, Tags: ph.newTags})
				if p.NodeID == 0 && inst.Op == syntax.InstMatch {
					matchAnchors |= p.Anchors
				}
			} else if p.Priority == existing.Priority {
				updates = append(updates, PathTagUpdate{RelativePriority: ph.sourcePrio, NextPriority: p.Priority, Tags: ph.newTags})
			}
			continue
		}
		if p.NodeID == 0 {
			switch inst.Op {
			case syntax.InstAlt, syntax.InstAltMatch:
				for _, next := range []struct {
					id uint32
					p  int32
				}{{inst.Arg, p.Priority + 1}, {inst.Out, p.Priority}} {
					if m, ok := minPriority[stateKey{next.id, 0, p.Anchors}]; !ok || next.p < m {
						minPriority[stateKey{next.id, 0, p.Anchors}] = next.p
						stack = append(stack, pathWithMeta{NFAPath{ID: next.id, Priority: next.p, Anchors: p.Anchors, Tags: p.Tags}, ph.newTags, ph.sourcePrio})
					}
				}
			case syntax.InstCapture:
				tagBit := uint64(1 << inst.Arg)
				if m, ok := minPriority[stateKey{inst.Out, 0, p.Anchors}]; !ok || p.Priority <= m {
					minPriority[stateKey{inst.Out, 0, p.Anchors}] = p.Priority
					stack = append(stack, pathWithMeta{NFAPath{ID: inst.Out, Priority: p.Priority, Anchors: p.Anchors, Tags: p.Tags | tagBit}, ph.newTags | tagBit, ph.sourcePrio})
				}
			case syntax.InstEmptyWidth:
				newAnchors := p.Anchors | syntax.EmptyOp(inst.Arg)
				if m, ok := minPriority[stateKey{inst.Out, 0, newAnchors}]; !ok || p.Priority <= m {
					minPriority[stateKey{inst.Out, 0, newAnchors}] = p.Priority
					stack = append(stack, pathWithMeta{NFAPath{ID: inst.Out, Priority: p.Priority, Anchors: newAnchors, Tags: p.Tags}, ph.newTags, ph.sourcePrio})
				}
			case syntax.InstNop:
				if m, ok := minPriority[stateKey{inst.Out, 0, p.Anchors}]; !ok || p.Priority <= m {
					minPriority[stateKey{inst.Out, 0, p.Anchors}] = p.Priority
					stack = append(stack, pathWithMeta{NFAPath{ID: inst.Out, Priority: p.Priority, Anchors: p.Anchors, Tags: p.Tags}, ph.newTags, ph.sourcePrio})
				}
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
		minSourcePrio := int32(1 << 30)
		if len(paths) > 0 {
			minSourcePrio = paths[0].Priority
			for _, p := range paths {
				if p.Priority < minSourcePrio {
					minSourcePrio = p.Priority
				}
			}
		}
		for i := range updates {
			updates[i].RelativePriority -= minSourcePrio
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

func hashSet(paths []NFAPath, naked bool) [2]uint64 {
	var h1 uint64 = 14695981039346656037
	for _, p := range paths {
		h1 ^= uint64(p.ID)
		h1 *= 1099511628211
	}
	return [2]uint64{h1, 0}
}

func NewDFAWithMemoryLimit(ctx context.Context, prog *syntax.Prog, maxMemory int, naked bool) (*DFA, error) {
	d := &DFA{Naked: naked, numSubexp: prog.NumCap / 2}
	if err := d.build(ctx, prog, maxMemory); err != nil {
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
