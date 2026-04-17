package ir

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"unsafe"

	"github.com/kamichidu/go-regexp-re/syntax"
)

type StateID int32

const (
	InvalidState StateID = -1
	StartStateID StateID = 0
)

// NFAState represents a state in the NFA.
type NFAState struct {
	ID     uint32
	NodeID uint32 // 0 means nil
}

// NFAPath represents a state in the NFA with its priority and tags.
type NFAPath struct {
	NFAState
	Priority int32
	_        int32 // padding
	Tags     uint64
}

const NFAPathSize = int(unsafe.Sizeof(NFAPath{}))

type TransitionUpdate struct {
	BasePriority int32
	PreUpdates   []PathTagUpdate
	PostUpdates  []PathTagUpdate
}

// NFAPathStorage defines how to store and retrieve NFA path sets during DFA construction.
type NFAPathStorage interface {
	Put(id StateID, paths []NFAPath) error
	Get(id StateID, buf []NFAPath) ([]NFAPath, error)
	Close() error
}

// memoryNfaSetStorage keeps everything in a slice.
type memoryNfaSetStorage struct {
	data [][]NFAPath
}

func (s *memoryNfaSetStorage) Put(id StateID, paths []NFAPath) error {
	if int(id) >= len(s.data) {
		newSize := int(id) + 1024
		if newSize < len(s.data)*2 {
			newSize = len(s.data) * 2
		}
		newData := make([][]NFAPath, newSize)
		copy(newData, s.data)
		s.data = newData
	}
	cp := make([]NFAPath, len(paths))
	copy(cp, paths)
	s.data[id] = cp
	return nil
}

func (s *memoryNfaSetStorage) Get(id StateID, buf []NFAPath) ([]NFAPath, error) {
	if int(id) >= len(s.data) {
		return nil, fmt.Errorf("state not found")
	}
	src := s.data[id]
	if len(buf) < len(src) {
		return src, nil
	}
	copy(buf, src)
	return buf[:len(src)], nil
}

func (s *memoryNfaSetStorage) Close() error { return nil }

// fileNfaSetStorage offloads NFA path sets to a temporary file using raw binary dump.
type fileNfaSetStorage struct {
	file    *os.File
	offsets []int64
	lengths []int32
	mu      sync.Mutex
}

func newFileNfaSetStorage() (*fileNfaSetStorage, error) {
	f, err := os.CreateTemp("", "go-regexp-re-nfa-*")
	if err != nil {
		return nil, err
	}
	return &fileNfaSetStorage{file: f}, nil
}

func (s *fileNfaSetStorage) Put(id StateID, paths []NFAPath) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	offset, err := s.file.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}

	var b []byte
	if len(paths) > 0 {
		b = unsafe.Slice((*byte)(unsafe.Pointer(&paths[0])), len(paths)*NFAPathSize)
	}

	if _, err := s.file.Write(b); err != nil {
		return err
	}

	if int(id) >= len(s.offsets) {
		newSize := int(id) + 4096
		newOffsets := make([]int64, newSize)
		newLengths := make([]int32, newSize)
		copy(newOffsets, s.offsets)
		copy(newLengths, s.lengths)
		s.offsets = newOffsets
		s.lengths = newLengths
	}
	s.offsets[id] = offset
	s.lengths[id] = int32(len(paths))
	return nil
}

func (s *fileNfaSetStorage) Get(id StateID, buf []NFAPath) ([]NFAPath, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if int(id) >= len(s.offsets) || (id > 0 && s.lengths[id] == 0 && s.offsets[id] == 0) {
		return nil, fmt.Errorf("state %d not found on disk", id)
	}

	length := int(s.lengths[id])
	if length == 0 {
		return nil, nil
	}

	if len(buf) < length {
		buf = make([]NFAPath, length)
	}

	targetBuf := unsafe.Slice((*byte)(unsafe.Pointer(&buf[0])), length*NFAPathSize)
	if _, err := s.file.ReadAt(targetBuf, s.offsets[id]); err != nil {
		return nil, err
	}

	return buf[:length], nil
}

func (s *fileNfaSetStorage) Close() error {
	name := s.file.Name()
	s.file.Close()
	return os.Remove(name)
}

const (
	AnchorBitBeginLine = iota
	AnchorBitEndLine
	AnchorBitBeginText
	AnchorBitEndText
	AnchorBitWordBoundary
	AnchorBitNoWordBoundary
	numAnchors = 6
)

const (
	TaggedStateFlag StateID = -2147483648 // Bit 31

	AnchorVerifyFlag StateID = 0x40000000 // Bit 30
	WarpStateFlag    StateID = 0x00800000 // Bit 23
	AnchorMask       StateID = 0x3F000000 // Bits 24-29 (6 bits for syntax.EmptyOp)
	StateIDMask      StateID = 0x007FFFFF // Bits 0-22 (up to 8M states)
)

type DFA struct {
	transitions            []StateID
	anchorTransitions      []StateID // numStates * 6
	tagUpdateIndices       []uint32
	anchorTagUpdateIndices []uint32 // numStates * 6
	tagUpdates             []TransitionUpdate
	startUpdates           []PathTagUpdate
	stride                 int
	numStates              int
	searchState            StateID
	matchState             StateID
	hasAnchors             bool
	usedAnchors            syntax.EmptyOp
	numSubexp              int
	Naked                  bool
	stateIsSearch          []bool
	maskStride             int
	stateToMask            []uint64
	instReachableToMatch   uint64
	greedyTags             uint64
	trieRoots              [][]*UTF8Node
	accepting              []bool
	stateMatchPriority     []int
	stateMatchTags         []uint64
	stateIsBestMatch       []bool
	isAlwaysTrue           []bool
	warpPoints             []int16
	warpPointState         []StateID
	warpPointGuards        []syntax.EmptyOp
	reachableToMatch       []uint64
	nodes                  []*UTF8Node
	storage                NFAPathStorage
	predecessorMasks       []uint64 // byte (256) -> targetNFA (NumInst) -> sourceNFA bitset (maskStride)
	instPriorities         []int32  // NFA Inst ID -> Absolute Priority
	maxInst                int
	stateMatchUpdates      [][]PathTagUpdate // priority -> tags (encoded as updates with NextPriority=-1)
}

func (d *DFA) PredecessorMasks() []uint64 { return d.predecessorMasks }
func (d *DFA) InstPriorities() []int32    { return d.instPriorities }

func (d *DFA) GetNFAContext(s StateID, buf []NFAPath) ([]NFAPath, error) {
	if d.storage == nil {
		return nil, fmt.Errorf("DFA storage not available")
	}
	return d.storage.Get(s, buf)
}

func (d *DFA) MaskStride() int { return d.maskStride }

func (d *DFA) ReachableToMatch(s StateID) uint64 {
	if s < 0 || int(s) >= len(d.reachableToMatch) {
		return 0
	}
	return d.reachableToMatch[s]
}

func (d *DFA) StateToMasks(s StateID) []uint64 {
	if s < 0 || int(s)*d.maskStride >= len(d.stateToMask) {
		return nil
	}
	return d.stateToMask[int(s)*d.maskStride : (int(s)+1)*d.maskStride]
}

func (d *DFA) StateToMask(s StateID) uint64 {
	if s < 0 || int(s)*d.maskStride >= len(d.stateToMask) {
		return 0
	}
	return d.stateToMask[int(s)*d.maskStride]
}

type BitParallelDFA struct {
	CharMasks         [256]uint64
	AnchorMasks       [6]uint64
	ContextMasks      [64]uint64
	SuccessorTable    [8][256]uint64
	MatchMask         uint64
	MatchMasks        [64]uint64
	StartMasks        [64]uint64
	CaptureMasks      [128]uint64 // 2 * numSubexp masks
	IsNonGreedy       bool
	AltMatchMasks     uint64     // For InstAlt: bit i is set if Alt is prioritized (standard greedy)
	EpsilonMasks      [64]uint64 // [inst_idx] -> closure of non-epsilon instructions
	PreEpsilonMasks   [64]uint64 // [inst_idx] -> epsilon closure BEFORE consuming a byte
	ContextEpsMask    [64]uint64 // [context] -> union of closures for all anchors active in context
	ReachableToMatch  uint64     // Bitmask of NFA states that can reach InstMatch
	ReverseSuccessors [64]uint64 // [target_bit] -> bitmask of source bits j such that target_bit in successors[j]
}

func (bp *BitParallelDFA) HasAnchors() bool {
	for _, m := range bp.AnchorMasks {
		if m != 0 {
			return true
		}
	}
	return false
}

func (d *DFA) Next(current StateID, b int) StateID {
	if current < 0 || int(current) >= d.numStates || b < 0 || b >= 256 {
		return InvalidState
	}
	offset := int(current)*256 + b
	if offset >= len(d.transitions) {
		return InvalidState
	}
	raw := d.transitions[offset]
	if raw == InvalidState {
		return InvalidState
	}
	return raw & StateIDMask
}
func (d *DFA) AnchorNext(current StateID, bit int) StateID {
	if current < 0 || int(current) >= d.numStates || bit < 0 || bit >= 6 {
		return InvalidState
	}
	return d.anchorTransitions[int(current)*6+bit]
}
func (d *DFA) NumStates() int                   { return d.numStates }
func (d *DFA) TotalStates() int                 { return d.numStates }
func (d *DFA) Transitions() []StateID           { return d.transitions }
func (d *DFA) AnchorTransitions() []StateID     { return d.anchorTransitions }
func (d *DFA) TagUpdateIndices() []uint32       { return d.tagUpdateIndices }
func (d *DFA) AnchorTagUpdateIndices() []uint32 { return d.anchorTagUpdateIndices }
func (d *DFA) TagUpdates() []TransitionUpdate   { return d.tagUpdates }
func (d *DFA) Stride() int                      { return 256 }
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
func (d *DFA) MatchUpdates(s StateID) []PathTagUpdate {
	if s < 0 || int(s) >= d.numStates {
		return nil
	}
	return d.stateMatchUpdates[s]
}
func (d *DFA) MatchTags(s StateID) uint64 {
	if s < 0 || int(s) >= d.numStates {
		return 0
	}
	return d.stateMatchTags[s]
}
func (d *DFA) SearchState() StateID             { return d.searchState }
func (d *DFA) MatchState() StateID              { return d.matchState }
func (d *DFA) StartUpdates() []PathTagUpdate    { return d.startUpdates }
func (d *DFA) HasAnchors() bool                 { return d.hasAnchors }
func (d *DFA) UsedAnchors() syntax.EmptyOp      { return d.usedAnchors }
func (d *DFA) Transitions() []StateID           { return d.transitions }
func (d *DFA) TagUpdates() []TransitionUpdate   { return d.tagUpdates }
func (d *DFA) TagUpdateIndices() []uint32       { return d.tagUpdateIndices }
func (d *DFA) AnchorTagUpdateIndices() []uint32 { return d.anchorTagUpdateIndices }
func (d *DFA) AnchorNext(s StateID, bit int) StateID {
	if s < 0 || int(s) >= d.numStates {
		return InvalidState
	}
	return d.anchorTransitions[int(s)*6+bit]
}
func (d *DFA) Nodes() []*UTF8Node { return d.nodes }
func (d *DFA) MaxInst() int       { return d.maxInst }

func (d *DFA) WarpPoint(s StateID) int {
	if s < 0 || int(s) >= d.numStates {
		return -1
	}
	return int(d.warpPoints[s])
}
func (d *DFA) WarpPointState(s StateID) StateID {
	if s < 0 || int(s) >= d.numStates {
		return InvalidState
	}
	return d.warpPointState[s]
}
func (d *DFA) WarpPointGuard(s StateID) syntax.EmptyOp {
	if s < 0 || int(s) >= d.numStates {
		return 0
	}
	return d.warpPointGuards[s]
}

var ErrStateExplosion = fmt.Errorf("regexp: pattern too large or ambiguous")

const MaxDFAMemory = 64 * 1024 * 1024

type dfaStateKey struct {
	hash     [2]uint64
	isSearch bool
}

const SearchRestartPenalty = 1000000

func NewDFA(prog *syntax.Prog) (*DFA, error) {
	return NewDFAWithMemoryLimit(context.Background(), prog, MaxDFAMemory, true)
}
func NewDFAWithMemoryLimit(ctx context.Context, prog *syntax.Prog, maxMemory int, naked bool) (*DFA, error) {
	d := &DFA{numSubexp: prog.NumCap / 2, maxInst: len(prog.Inst), Naked: naked}
	if err := d.checkCompatibility(prog); err != nil {
		return nil, err
	}
	if err := d.build(ctx, prog, maxMemory); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *DFA) GreedyTags() uint64 { return d.greedyTags }

func (d *DFA) InstReachableToMatch() uint64 { return d.instReachableToMatch }

func NewBitParallelDFA(prog *syntax.Prog) *BitParallelDFA {
	if len(prog.Inst) > 62 { // Reserve bit 63
		return nil
	}

	// Step 1: Uniform Greediness Detection
	hasGreedy := false
	hasNonGreedy := false
	for _, inst := range prog.Inst {
		if inst.Op == syntax.InstAlt || inst.Op == syntax.InstAltMatch {
			if inst.Arg < inst.Out {
				hasNonGreedy = true
			} else {
				hasGreedy = true
			}
		}
	}
	if hasGreedy && hasNonGreedy {
		// Mixed greediness is currently unsupported by the 2-system BP-DFA.
		return nil
	}

	bp := &BitParallelDFA{
		IsNonGreedy: hasNonGreedy,
	}

	// epsilonClosure returns a bitmask of states reachable from i via epsilons.
	epsilonClosureWithContext := func(start int, ctx syntax.EmptyOp, full bool) uint64 {
		var active uint64
		var visited uint64
		var dfs func(int)
		dfs = func(curr int) {
			if (visited & (1 << uint(curr))) != 0 {
				return
			}
			visited |= (1 << uint(curr))
			inst := prog.Inst[curr]
			if full {
				active |= (1 << uint(curr))
			}
			switch inst.Op {
			case syntax.InstAlt, syntax.InstAltMatch:
				dfs(int(inst.Out))
				dfs(int(inst.Arg))
			case syntax.InstCapture, syntax.InstNop:
				dfs(int(inst.Out))
			case syntax.InstEmptyWidth:
				if (syntax.EmptyOp(inst.Arg) & ctx) == syntax.EmptyOp(inst.Arg) {
					dfs(int(inst.Out))
				}
			default:
				if !full {
					active |= (1 << uint(curr))
				}
			}
		}
		dfs(start)
		return active
	}

	for i, inst := range prog.Inst {
		switch inst.Op {
		case syntax.InstRune, syntax.InstRune1, syntax.InstRuneAny, syntax.InstRuneAnyNotNL:
			for b := 0; b < 256; b++ {
				if inst.MatchRune(rune(b)) {
					bp.CharMasks[b] |= (1 << uint(i))
				}
			}
		case syntax.InstEmptyWidth:
			for bit := 0; bit < 6; bit++ {
				if (inst.Arg & (1 << uint(bit))) != 0 {
					bp.AnchorMasks[bit] |= (1 << uint(i))
				}
			}
		case syntax.InstCapture:
			if inst.Arg < 128 {
				bp.CaptureMasks[inst.Arg] |= (1 << uint(i))
			}
		case syntax.InstAlt, syntax.InstAltMatch:
			if !bp.IsNonGreedy {
				// In standard greedy, Alt.Out (the loop) is prioritized.
				// We record which instruction is the prioritized one.
				bp.AltMatchMasks |= (1 << uint(i))
			}
		case syntax.InstMatch:
			bp.MatchMask |= (1 << uint(i))
		}
	}

	successors := make([]uint64, len(prog.Inst))
	for i, inst := range prog.Inst {
		switch inst.Op {
		case syntax.InstRune, syntax.InstRune1, syntax.InstRuneAny, syntax.InstRuneAnyNotNL, syntax.InstEmptyWidth:
			var active uint64
			var visited uint64
			var dfs func(int)
			dfs = func(curr int) {
				if (visited & (1 << uint(curr))) != 0 {
					return
				}
				visited |= (1 << uint(curr))
				inst := prog.Inst[curr]
				switch inst.Op {
				case syntax.InstAlt, syntax.InstAltMatch:
					dfs(int(inst.Out))
					dfs(int(inst.Arg))
				case syntax.InstCapture, syntax.InstNop:
					dfs(int(inst.Out))
				default:
					active |= (1 << uint(curr))
				}
			}
			dfs(int(inst.Out))
			successors[i] = active
		}
	}

	for i, s := range successors {
		for bit := 0; bit < 64; bit++ {
			if (s & (1 << uint(bit))) != 0 {
				bp.ReverseSuccessors[bit] |= (1 << uint(i))
			}
		}
	}

	for t := 0; t < 8; t++ {
		for byteVal := 0; byteVal < 256; byteVal++ {
			var union uint64
			for bit := 0; bit < 8; bit++ {
				if (byteVal & (1 << uint(bit))) != 0 {
					idx := t*8 + bit
					if idx < len(successors) {
						union |= successors[idx]
					}
				}
			}
			bp.SuccessorTable[t][byteVal] = union
		}
	}

	allAnchors := uint64(0)
	for i := 0; i < 6; i++ {
		allAnchors |= bp.AnchorMasks[i]
	}
	nonAnchors := ^allAnchors

	for c := 0; c < 64; c++ {
		m := nonAnchors
		for bit := 0; bit < 6; bit++ {
			if (c & (1 << uint(bit))) != 0 {
				m |= bp.AnchorMasks[bit]
			}
		}
		bp.ContextMasks[c] = m
	}

	for c := 0; c < 64; c++ {
		ctx := syntax.EmptyOp(c)
		var matchMask uint64
		for i := 0; i < len(prog.Inst); i++ {
			if (epsilonClosureWithContext(i, ctx, false) & bp.MatchMask) != 0 {
				matchMask |= (1 << uint(i))
			}
		}
		bp.MatchMasks[c] = matchMask
		bp.StartMasks[c] = epsilonClosureWithContext(prog.Start, ctx, false)

		// Context-dependent epsilon closure for all anchors
		var contextClosure uint64
		for i, inst := range prog.Inst {
			if inst.Op == syntax.InstEmptyWidth && (syntax.EmptyOp(inst.Arg)&ctx) == syntax.EmptyOp(inst.Arg) {
				contextClosure |= epsilonClosureWithContext(i, ctx, false)
			}
		}
		bp.ContextEpsMask[c] = contextClosure
	}

	// Backward epsilon closure calculation for submatch extraction.
	// bp.PreEpsilonMasks[i] will contain all instructions j such that there is an
	// epsilon-only path from j to i.
	reverseEpsilon := make([]uint64, len(prog.Inst))
	for i, inst := range prog.Inst {
		switch inst.Op {
		case syntax.InstAlt, syntax.InstAltMatch:
			reverseEpsilon[int(inst.Out)] |= (1 << uint(i))
			reverseEpsilon[int(inst.Arg)] |= (1 << uint(i))
		case syntax.InstCapture, syntax.InstNop:
			reverseEpsilon[int(inst.Out)] |= (1 << uint(i))
		case syntax.InstEmptyWidth:
			reverseEpsilon[int(inst.Out)] |= (1 << uint(i))
		}
	}

	for i := 0; i < len(prog.Inst); i++ {
		visited := uint64(1 << uint(i))
		queue := make([]int, 0, 64)
		queue = append(queue, i)
		for len(queue) > 0 {
			curr := queue[0]
			queue = queue[1:]

			prevs := reverseEpsilon[curr]
			for j := 0; j < len(prog.Inst); j++ {
				if (prevs & (1 << uint(j))) != 0 {
					if (visited & (1 << uint(j))) == 0 {
						visited |= (1 << uint(j))
						queue = append(queue, j)
					}
				}
			}
		}
		bp.PreEpsilonMasks[i] = visited
	}

	for i := 0; i < len(prog.Inst); i++ {
		bp.EpsilonMasks[i] = epsilonClosureWithContext(i, 0, false)
		if (epsilonClosureWithContext(i, 0, true) & bp.MatchMask) != 0 {
			bp.ReachableToMatch |= (1 << uint(i))
		}
	}

	return bp
}

type closureCacheKey struct {
	hash    [2]uint64
	context syntax.EmptyOp
}
type ClosureResult struct {
	NextClosure []NFAPath
	Updates     []PathTagUpdate
}

func EpsilonClosureWithPathTags(paths []NFAPath, prog *syntax.Prog, context syntax.EmptyOp, nodes []*UTF8Node) ([]NFAPath, []PathTagUpdate) {
	return epsilonClosureWithPathTags(paths, prog, context, nodes)
}

func hashSet(set []NFAPath, naked bool) [2]uint64 {
	if len(set) == 0 {
		return [2]uint64{0, 0}
	}
	minP := set[0].Priority
	if !naked {
		for i := 1; i < len(set); i++ {
			if set[i].Priority < minP {
				minP = set[i].Priority
			}
		}
	}

	h1 := uint64(14695981039346656037)
	h2 := uint64(1000000000000000003)

	for _, s := range set {
		h1 ^= uint64(s.ID)
		h1 *= 1099511628211
		h2 ^= uint64(s.ID)
		h2 *= 1000003

		h1 ^= uint64(s.NodeID)
		h1 *= 1099511628211
		h2 ^= uint64(s.NodeID)
		h2 *= 1000003

		if !naked {
			prio := uint64(uint32(s.Priority - minP))
			h1 ^= prio
			h1 *= 1099511628211
			h2 ^= prio
			h2 *= 1000003

			h1 ^= s.Tags
			h1 *= 1099511628211
			h2 ^= s.Tags
			h2 *= 1000003
		}
	}
	return [2]uint64{h1, h2}
}

func hashUpdate(u TransitionUpdate) [2]uint64 {
	h1 := uint64(14695981039346656037)
	h2 := uint64(1000000000000000003)

	h1 ^= uint64(uint32(u.BasePriority))
	h1 *= 1099511628211
	h2 ^= uint64(uint32(u.BasePriority))
	h2 *= 1000003

	for _, p := range u.PreUpdates {
		h1 ^= uint64(uint32(p.RelativePriority))
		h1 *= 1099511628211
		h1 ^= p.Tags
		h1 *= 1099511628211

		h2 ^= uint64(uint32(p.RelativePriority))
		h2 *= 1000003
		h2 ^= p.Tags
		h2 *= 1000003
	}
	for _, p := range u.PostUpdates {
		h1 ^= uint64(uint32(p.RelativePriority))
		h1 *= 1099511628211
		h1 ^= p.Tags
		h1 *= 1099511628211

		h2 ^= uint64(uint32(p.RelativePriority))
		h2 *= 1000003
		h2 ^= p.Tags
		h2 *= 1000003
	}
	return [2]uint64{h1, h2}
}

func (d *DFA) IsNaked() bool {
	return d.Naked
}

func (d *DFA) TransitionUpdate(s StateID, b byte) TransitionUpdate {
	if s < 0 || int(s) >= d.numStates {
		return TransitionUpdate{}
	}
	idx := (int(s) << 8) | int(b)
	uIdx := d.tagUpdateIndices[idx]
	return d.tagUpdates[uIdx]
}

func (d *DFA) AnchorTransitionUpdate(s StateID, bit int) TransitionUpdate {
	if s < 0 || int(s) >= d.numStates {
		return TransitionUpdate{}
	}
	idx := int(s)*6 + bit
	uIdx := d.anchorTagUpdateIndices[idx]
	return d.tagUpdates[uIdx]
}

func (d *DFA) CanReachPriority(fromState, toState StateID, context syntax.EmptyOp, p_in, p_out int32) bool {
	if fromState == toState {
		return p_in == p_out
	}
	s := fromState
	p := p_in
	for iter := 0; iter < 6; iter++ {
		changed := false
		for bit := 0; bit < 6; bit++ {
			if (context & (1 << uint(bit))) != 0 {
				rawNext := d.AnchorNext(s, bit)
				if rawNext != InvalidState {
					nextID := rawNext & StateIDMask
					if nextID != s {
						if rawNext < 0 {
							update := d.AnchorTransitionUpdate(s, bit)
							found := false
							for _, pu := range update.PreUpdates {
								if pu.RelativePriority == p {
									p = pu.NextPriority
									found = true
									break
								}
							}
							if !found {
								return false
							}
						}
						s = nextID
						changed = true
					}
				}
			}
		}
		if !changed {
			break
		}
	}
	return s == toState && p == p_out
}

func (d *DFA) build(ctx context.Context, prog *syntax.Prog, maxMemory int) error {
	d.maskStride = (len(prog.Inst) + 63) / 64
	cache := newUTF8NodeCache()
	d.hasAnchors = false
	d.usedAnchors = 0
	for _, inst := range prog.Inst {
		if inst.Op == syntax.InstEmptyWidth {
			d.hasAnchors = true
			d.usedAnchors |= syntax.EmptyOp(inst.Arg)
		}
	}
	d.stride = 256

	reachableToMatchSet := make(map[int]bool)
	changed := true
	for changed {
		changed = false
		for i, inst := range prog.Inst {
			if reachableToMatchSet[i] {
				continue
			}
			if inst.Op == syntax.InstMatch {
				reachableToMatchSet[i] = true
				changed = true
				continue
			}

			can := false
			switch inst.Op {
			case syntax.InstAlt, syntax.InstAltMatch:
				can = reachableToMatchSet[int(inst.Out)] || reachableToMatchSet[int(inst.Arg)]
			case syntax.InstCapture, syntax.InstNop, syntax.InstEmptyWidth, syntax.InstRune, syntax.InstRune1, syntax.InstRuneAny, syntax.InstRuneAnyNotNL:
				can = reachableToMatchSet[int(inst.Out)]
			}
			if can {
				reachableToMatchSet[i] = true
				changed = true
			}
		}
	}
	var reachableToMatch uint64
	for i := range prog.Inst {
		if i < 64 && reachableToMatchSet[i] {
			reachableToMatch |= (1 << uint(i))
		}
	}
	d.instReachableToMatch = reachableToMatch

	var greedyTags uint64
	for _, inst := range prog.Inst {
		if (inst.Op == syntax.InstAlt || inst.Op == syntax.InstAltMatch) && inst.Arg > inst.Out {
			target := int(inst.Arg)
			for {
				next := prog.Inst[target]
				if next.Op == syntax.InstCapture {
					if next.Arg < 64 {
						greedyTags |= (1 << next.Arg)
					}
					target = int(next.Out)
				} else if next.Op == syntax.InstNop {
					target = int(next.Out)
				} else {
					break
				}
			}
		}
	}
	d.greedyTags = greedyTags

	d.trieRoots = make([][]*UTF8Node, len(prog.Inst))
	var nodes []*UTF8Node
	nodes = append(nodes, nil) // ID 0 is nil

	getTrie := func(ID uint32) []*UTF8Node {
		if roots := d.trieRoots[ID]; roots != nil {
			return roots
		}
		inst := prog.Inst[ID]
		var roots []*UTF8Node
		switch inst.Op {
		case syntax.InstRune, syntax.InstRune1:
			fold := inst.Op == syntax.InstRune && (inst.Arg&uint32(syntax.FoldCase) != 0)
			roots = cache.runeRangesToUTF8Trie(inst.Rune, fold, false)
		case syntax.InstRuneAny:
			roots = cache.anyRuneTrie(true)
		case syntax.InstRuneAnyNotNL:
			roots = cache.anyRuneTrie(false)
		}
		d.trieRoots[ID] = roots
		for _, root := range roots {
			d.registerNodes(root, &nodes)
		}
		return roots
	}
	d.nodes = nodes

	var storage NFAPathStorage
	isFileMode := maxMemory > 1024*1024*1024
	if isFileMode {
		var err error
		storage, err = newFileNfaSetStorage()
		if err != nil {
			return err
		}
	} else {
		storage = &memoryNfaSetStorage{data: make([][]NFAPath, 0)}
	}
	d.storage = storage

	nfaToDfa := make(map[dfaStateKey]StateID)
	updateToIdx := make(map[[2]uint64]uint32) // Use hash key
	addUpdate := func(u TransitionUpdate) uint32 {
		key := hashUpdate(u)
		if idx, ok := updateToIdx[key]; ok {
			return idx
		}
		idx := uint32(len(d.tagUpdates))
		d.tagUpdates = append(d.tagUpdates, u)
		updateToIdx[key] = idx
		return idx
	}
	closureCache := make(map[closureCacheKey]ClosureResult)
	getCachedClosure := func(paths []NFAPath, context syntax.EmptyOp) ClosureResult {
		if len(paths) == 0 {
			return ClosureResult{}
		}
		minP := paths[0].Priority
		if !d.Naked {
			for i := 1; i < len(paths); i++ {
				if paths[i].Priority < minP {
					minP = paths[i].Priority
				}
			}
		}
		key := closureCacheKey{hashSet(paths, d.Naked), context}
		if res, ok := closureCache[key]; ok {
			if minP == 0 || d.Naked {
				return res
			}
			newClosure := make([]NFAPath, len(res.NextClosure))
			for i, p := range res.NextClosure {
				newClosure[i] = NFAPath{NFAState: NFAState{ID: p.ID, NodeID: p.NodeID}, Priority: p.Priority + minP, Tags: p.Tags}
			}
			newUpdates := make([]PathTagUpdate, len(res.Updates))
			for i, u := range res.Updates {
				newUpdates[i] = PathTagUpdate{RelativePriority: u.RelativePriority + int32(minP), NextPriority: u.NextPriority + int32(minP), Tags: u.Tags}
			}
			return ClosureResult{newClosure, newUpdates}
		}

		normPaths := make([]NFAPath, len(paths))
		if d.Naked {
			copy(normPaths, paths)
		} else {
			for i, p := range paths {
				normPaths[i] = NFAPath{NFAState: NFAState{ID: p.ID, NodeID: p.NodeID}, Priority: p.Priority - minP, Tags: p.Tags}
			}
		}
		nextClosure, updates := epsilonClosureWithPathTags(normPaths, prog, context, d.nodes)
		res := ClosureResult{nextClosure, updates}

		limit := 100000
		if isFileMode {
			limit = 10000
		} // Aggressive clearing in file mode
		if len(closureCache) > limit {
			closureCache = make(map[closureCacheKey]ClosureResult)
		}
		closureCache[key] = res

		if minP == 0 || d.Naked {
			return res
		}
		denormClosure := make([]NFAPath, len(nextClosure))
		for i, p := range nextClosure {
			denormClosure[i] = NFAPath{NFAState: NFAState{ID: p.ID, NodeID: p.NodeID}, Priority: p.Priority + minP, Tags: p.Tags}
		}
		denormUpdates := make([]PathTagUpdate, len(updates))
		for i, u := range updates {
			denormUpdates[i] = PathTagUpdate{RelativePriority: u.RelativePriority + int32(minP), NextPriority: u.NextPriority + int32(minP), Tags: u.Tags}
		}
		return ClosureResult{denormClosure, denormUpdates}
	}

	var errBuild error
	usedMemory := 0
	checkMem := func(bytes int) error {
		usedMemory += bytes
		if usedMemory > maxMemory && !d.Naked {
			return ErrStateExplosion
		}
		return nil
	}

	addDfaState := func(closure []NFAPath, isSearch bool) StateID {
		if errBuild != nil {
			return InvalidState
		}

		if len(closure) > 0 && !d.Naked {
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
			if closure[i].NodeID != closure[j].NodeID {
				return closure[i].NodeID < closure[j].NodeID
			}
			if d.Naked {
				return false
			}
			if closure[i].Priority != closure[j].Priority {
				return closure[i].Priority < closure[j].Priority
			}
			return closure[i].Tags < closure[j].Tags
		})
		key := dfaStateKey{hashSet(closure, d.Naked), isSearch}
		if id, ok := nfaToDfa[key]; ok {
			return id
		}

		// New state: charge memory
		stateMem := 256*4 + 256*4 + d.maskStride*8 + 128
		if err := checkMem(stateMem); err != nil {
			errBuild = err
			return InvalidState
		}

		if d.numStates >= 1000000 {
			errBuild = fmt.Errorf("regexp: pattern too large or ambiguous (state limit exceeded)")
			return InvalidState
		}
		id := StateID(d.numStates)
		nfaToDfa[key] = id
		if d.numStates%100 == 0 {
		}
		if err := checkMem(64); err != nil { // Map entry overhead
			errBuild = err
			return InvalidState
		}

		if err := storage.Put(id, closure); err != nil {
			errBuild = err
			return InvalidState
		}
		if !isFileMode {
			if err := checkMem(len(closure) * NFAPathSize); err != nil {
				errBuild = err
				return InvalidState
			}
		}

		for i := 0; i < d.maskStride; i++ {
			d.stateToMask = append(d.stateToMask, 0)
		}
		for _, p := range closure {
			d.stateToMask[int(id)*d.maskStride+int(p.ID/64)] |= (1 << (p.ID % 64))
		}

		d.stateIsSearch = append(d.stateIsSearch, isSearch)
		isAcc, matchP := false, 1<<30-1
		var matchTags uint64
		var matchUpdates []PathTagUpdate
		for _, s := range closure {
			if prog.Inst[s.ID].Op == syntax.InstMatch && s.NodeID == 0 {
				isAcc = true
				prio := int(s.Priority)
				if prio < matchP {
					matchP = prio
					matchTags = s.Tags
				}
				// All paths that reach Match in this closure are potential winning tags.
				// NextPriority = -1 indicates match destination.
				matchUpdates = append(matchUpdates, PathTagUpdate{
					RelativePriority: s.Priority, // In backward recap, we search for this src priority.
					NextPriority:     -1,
					Tags:             s.Tags,
				})
			}
		}

		isBest := false
		if isAcc {
			isBest = true
			for _, s := range closure {
				// NFA priority of this path
				if int(s.Priority) <= matchP {
					// If there is any path that is not yet matched but has better or equal priority
					// than our current match, then our current match is not yet "best" (final).
					// It might be beaten by a longer match from an equal priority path,
					// or by a shorter match from a better priority path.
					if prog.Inst[s.ID].Op == syntax.InstMatch && s.NodeID == 0 {
						// This is the current best match itself or another match with same priority.
						// If multiple paths reach Match with same priority, the first one in closure wins.
						continue
					}
					if s.ID < 64 && (d.instReachableToMatch&(1<<uint(s.ID))) != 0 {
						isBest = false
						break
					} else if s.ID >= 64 {
						isBest = false
						break
					}
				}
			}
		}

		d.accepting = append(d.accepting, isAcc)
		d.stateMatchPriority = append(d.stateMatchPriority, matchP)
		d.stateMatchTags = append(d.stateMatchTags, matchTags)
		d.stateMatchUpdates = append(d.stateMatchUpdates, matchUpdates)
		d.stateIsBestMatch = append(d.stateIsBestMatch, isBest)
		for i := 0; i < 256; i++ {
			d.transitions = append(d.transitions, InvalidState)
			d.tagUpdateIndices = append(d.tagUpdateIndices, 0)
		}
		for i := 0; i < 6; i++ {
			d.anchorTransitions = append(d.anchorTransitions, InvalidState)
			d.anchorTagUpdateIndices = append(d.anchorTagUpdateIndices, 0)
		}
		d.numStates++
		return id
	}

	defaultStartRes := getCachedClosure([]NFAPath{{NFAState: NFAState{ID: uint32(prog.Start), NodeID: 0}}}, 0)
	d.matchState = addDfaState(defaultStartRes.NextClosure, false)
	d.startUpdates = defaultStartRes.Updates
	d.searchState = addDfaState(defaultStartRes.NextClosure, true)

	scratchBuf := make([]NFAPath, 0, 1024)
	nextPaths := make([]NFAPath, 0, 1024)

	isMeaningful := func(updates []PathTagUpdate) bool {
		for _, u := range updates {
			if u.Tags != 0 || u.NextPriority != u.RelativePriority {
				return true
			}
		}
		return false
	}

	for i := 0; i < d.numStates; i++ {
		if i%100 == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
		currentDfaID := StateID(i)
		currentIsSearch := d.stateIsSearch[i]

		currentClosure, err := storage.Get(currentDfaID, scratchBuf)
		if err != nil {
			return err
		}
		scratchBuf = currentClosure

		var initialPaths []NFAPath
		if currentIsSearch {
			initialPaths = nextPaths[:0]
			initialPaths = append(initialPaths, currentClosure...)
			initialPaths = append(initialPaths, NFAPath{NFAState: NFAState{ID: uint32(prog.Start), NodeID: 0}, Priority: SearchRestartPenalty})
		} else {
			initialPaths = currentClosure
		}
		searchRes := getCachedClosure(initialPaths, 0)
		// Track memory for closure results
		if err := checkMem(len(searchRes.NextClosure)*NFAPathSize + len(searchRes.Updates)*16 + 128); err != nil {
			return err
		}
		for b := 0; b < 256; b++ {
			nextPaths = nextPaths[:0]
			foundMatch := false
			canWarp := true
			hasWarpCandidate := false
			var shortcutUpdates []PathTagUpdate
			for _, p := range searchRes.NextClosure {
				s := p.NFAState
				inst := prog.Inst[s.ID]
				var matchedOut []uint32
				var matchedNodeIDs []uint32
				if s.NodeID == 0 {
					switch inst.Op {
					case syntax.InstRune, syntax.InstRune1, syntax.InstRuneAny, syntax.InstRuneAnyNotNL:
						roots := getTrie(s.ID)
						d.nodes = nodes
						fold := inst.Op == syntax.InstRune && (inst.Arg&uint32(syntax.FoldCase) != 0)
						for _, root := range roots {
							match := matchesByte
							if fold {
								match = matchesByteFold
							}
							if match(root, byte(b)) {
								if b >= 0x80 {
									hasWarpCandidate = true
									if root.Next != nil {
										canWarp = false
									}
								}
								if root.Next == nil {
									matchedOut = append(matchedOut, inst.Out)
								} else {
									for _, child := range root.Next {
										matchedNodeIDs = append(matchedNodeIDs, uint32(child.ID))
									}
								}
							}
						}
					}
				} else {
					node := d.nodes[s.NodeID]
					fold := inst.Op == syntax.InstRune && (inst.Arg&uint32(syntax.FoldCase) != 0)
					match := matchesByte
					if fold {
						match = matchesByteFold
					}
					if match(node, byte(b)) {
						if b >= 0x80 {
							hasWarpCandidate = true
							if node.Next != nil {
								canWarp = false
							}
						}
						if node.Next == nil {
							matchedOut = append(matchedOut, inst.Out)
						} else {
							for _, child := range node.Next {
								matchedNodeIDs = append(matchedNodeIDs, uint32(child.ID))
							}
						}
					}
				}
				if len(matchedOut) > 0 || len(matchedNodeIDs) > 0 {
					foundMatch = true
					for _, out := range matchedOut {
						// Shortcut! Follow epsilons and collect tags.
						res := getCachedClosure([]NFAPath{{NFAState: NFAState{ID: out, NodeID: 0}, Priority: p.Priority, Tags: p.Tags}}, 0)
						for _, np := range res.NextClosure {
							nextPaths = append(nextPaths, np)
						}
						for _, u := range res.Updates {
							shortcutUpdates = append(shortcutUpdates, u)
						}
					}
					for _, nodeID := range matchedNodeIDs {
						nextPaths = append(nextPaths, NFAPath{NFAState: NFAState{ID: s.ID, NodeID: nodeID}, Priority: p.Priority, Tags: p.Tags})
					}
				}
			}
			if foundMatch {
				nextRes := getCachedClosure(nextPaths, 0)
				if len(nextRes.NextClosure) == 0 {
					continue
				}

				// Merge shortcut updates into nextRes.Updates
				allPostUpdates := make([]PathTagUpdate, 0, len(nextRes.Updates)+len(shortcutUpdates))
				allPostUpdates = append(allPostUpdates, nextRes.Updates...)
				for _, su := range shortcutUpdates {
					found := false
					for _, au := range allPostUpdates {
						if su == au {
							found = true
							break
						}
					}
					if !found {
						allPostUpdates = append(allPostUpdates, su)
					}
				}

				minP := int32(1 << 30)
				if len(nextRes.NextClosure) > 0 {
					minP = nextRes.NextClosure[0].Priority
					for _, p := range nextRes.NextClosure {
						if p.Priority < minP {
							minP = p.Priority
						}
					}
				}
				for _, u := range allPostUpdates {
					if u.NextPriority < minP {
						minP = u.NextPriority
					}
				}

				nextDfaID := addDfaState(nextRes.NextClosure, currentIsSearch)
				if errBuild != nil {
					return errBuild
				}
				idx := int(currentDfaID)*256 + b

				var postUpdates []PathTagUpdate
				if len(allPostUpdates) > 0 {
					postUpdates = make([]PathTagUpdate, len(allPostUpdates))
					for j, u := range allPostUpdates {
						postUpdates[j] = PathTagUpdate{RelativePriority: u.RelativePriority - int32(minP), NextPriority: u.NextPriority - int32(minP), Tags: u.Tags}
					}
				}

				flag := StateID(0)
				if hasWarpCandidate && canWarp {
					flag |= WarpStateFlag
				}

				if minP != 0 || isMeaningful(searchRes.Updates) || isMeaningful(postUpdates) {
					d.transitions[idx] = nextDfaID | TaggedStateFlag | flag
					update := TransitionUpdate{BasePriority: int32(minP), PreUpdates: searchRes.Updates, PostUpdates: postUpdates}
					d.tagUpdateIndices[idx] = addUpdate(update)
				} else {
					d.transitions[idx] = nextDfaID | flag
				}
			}
		}
		if d.hasAnchors {
			for bit := 0; bit < 6; bit++ {
				op := syntax.EmptyOp(1 << bit)
				var anchorPaths []NFAPath
				if currentIsSearch {
					anchorPaths = nextPaths[:0]
					anchorPaths = append(anchorPaths, currentClosure...)
					anchorPaths = append(anchorPaths, NFAPath{NFAState: NFAState{ID: uint32(prog.Start), NodeID: 0}, Priority: SearchRestartPenalty})
				} else {
					anchorPaths = currentClosure
				}
				nextRes := getCachedClosure(anchorPaths, op)
				if len(nextRes.NextClosure) == 0 || hashSet(nextRes.NextClosure, d.Naked) == hashSet(currentClosure, d.Naked) {
					continue
				}
				minP := nextRes.NextClosure[0].Priority
				for _, p := range nextRes.NextClosure {
					if p.Priority < minP {
						minP = p.Priority
					}
				}
				nextDfaID := addDfaState(nextRes.NextClosure, currentIsSearch)
				if errBuild != nil {
					return errBuild
				}
				idx := int(currentDfaID)*6 + bit

				var postUpdates []PathTagUpdate
				if len(nextRes.Updates) > 0 {
					postUpdates = make([]PathTagUpdate, len(nextRes.Updates))
					for j, u := range nextRes.Updates {
						postUpdates[j] = PathTagUpdate{RelativePriority: u.RelativePriority - int32(minP), NextPriority: u.NextPriority - int32(minP), Tags: u.Tags}
					}
				}

				if minP != 0 || isMeaningful(postUpdates) {
					d.anchorTransitions[idx] = nextDfaID | TaggedStateFlag
					d.anchorTagUpdateIndices[idx] = addUpdate(TransitionUpdate{BasePriority: int32(minP), PreUpdates: postUpdates})
				} else {
					d.anchorTransitions[idx] = nextDfaID
				}
			}
		}
	}
	d.minimize()
	d.computePhase2Metadata(prog)

	nfaToDfa = nil
	updateToIdx = nil
	closureCache = nil
	scratchBuf = nil
	nextPaths = nil

	return nil
}

func (d *DFA) registerNodes(node *UTF8Node, nodes *[]*UTF8Node) {
	if node == nil {
		return
	}
	for len(*nodes) <= node.ID {
		*nodes = append(*nodes, nil)
	}
	(*nodes)[node.ID] = node
	for _, child := range node.Next {
		d.registerNodes(child, nodes)
	}
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
			hash     [2]uint64
		}
		newGroups := make(map[splitKey]int32)
		nextStateToGroup := make([]int32, d.numStates)
		nextNumGroups := int32(0)
		for i := 0; i < d.numStates; i++ {
			h1 := uint64(14695981039346656037)
			h2 := uint64(1000000000000000003)
			for b := 0; b < 256; b++ {
				idx := i*256 + b
				target := d.transitions[idx]
				var tg int32 = -1
				var updateIdx uint32 = 0
				if target != InvalidState {
					tg = stateToGroup[target&StateIDMask]
					if target < 0 {
						updateIdx = d.tagUpdateIndices[idx] + 1
					}
				}
				h1 ^= uint64(uint32(tg))
				h1 *= 1099511628211
				h1 ^= uint64(updateIdx)
				h1 *= 1099511628211
				h2 ^= uint64(uint32(tg))
				h2 *= 1000003
				h2 ^= uint64(updateIdx)
				h2 *= 1000003
			}
			for bit := 0; bit < 6; bit++ {
				idx := i*6 + bit
				target := d.anchorTransitions[idx]
				var tg int32 = -1
				var updateIdx uint32 = 0
				if target != InvalidState {
					tg = stateToGroup[target&StateIDMask]
					if target < 0 {
						updateIdx = d.anchorTagUpdateIndices[idx] + 1
					}
				}
				h1 ^= uint64(uint32(tg))
				h1 *= 1099511628211
				h1 ^= uint64(updateIdx)
				h1 *= 1099511628211
				h2 ^= uint64(uint32(tg))
				h2 *= 1000003
				h2 ^= uint64(updateIdx)
				h2 *= 1000003
			}
			key := splitKey{stateToGroup[i], [2]uint64{h1, h2}}
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
	newTransitions := make([]StateID, int(numGroups)*256)
	newAnchorTransitions := make([]StateID, int(numGroups)*6)
	newUpdateIndices := make([]uint32, int(numGroups)*256)
	newAnchorUpdateIndices := make([]uint32, int(numGroups)*6)
	newAccepting, newPrio, newMatchTags, newBest, newIsSearch := make([]bool, numGroups), make([]int, numGroups), make([]uint64, numGroups), make([]bool, numGroups), make([]bool, numGroups)
	newStateMatchUpdates := make([][]PathTagUpdate, numGroups)
	for g := int32(0); g < numGroups; g++ {
		oldS := groupToFirstState[g]
		newAccepting[g] = d.accepting[oldS]
		newPrio[g] = d.stateMatchPriority[oldS]
		newMatchTags[g] = d.stateMatchTags[oldS]
		newBest[g] = d.stateIsBestMatch[oldS]
		newIsSearch[g] = d.stateIsSearch[oldS]
		newStateMatchUpdates[g] = d.stateMatchUpdates[oldS]
		for b := 0; b < 256; b++ {
			oldIdx := oldS*256 + b
			target := d.transitions[oldIdx]
			if target != InvalidState {
				newID := StateID(stateToGroup[target&StateIDMask])
				flags := target & ^StateIDMask
				newTransitions[int(g)*256+b] = newID | flags
				if target < 0 {
					newUpdateIndices[int(g)*256+b] = d.tagUpdateIndices[oldIdx]
				}
			} else {
				newTransitions[int(g)*256+b] = InvalidState
			}
		}
		for bit := 0; bit < 6; bit++ {
			oldIdx := oldS*6 + bit
			target := d.anchorTransitions[oldIdx]
			if target != InvalidState {
				newID := StateID(stateToGroup[target&StateIDMask])
				flags := target & ^StateIDMask
				newAnchorTransitions[int(g)*6+bit] = newID | flags
				if target < 0 {
					newAnchorUpdateIndices[int(g)*6+bit] = d.anchorTagUpdateIndices[oldIdx]
				}
			} else {
				newAnchorTransitions[int(g)*6+bit] = InvalidState
			}
		}
	}
	newMasks := make([]uint64, int(numGroups)*d.maskStride)
	newStorage := &memoryNfaSetStorage{data: make([][]NFAPath, int(numGroups))}
	for oldS, g := range stateToGroup {
		for i := 0; i < d.maskStride; i++ {
			newMasks[int(g)*d.maskStride+i] |= d.stateToMask[oldS*d.maskStride+i]
		}
		if newStorage.data[g] == nil {
			paths, _ := d.storage.Get(StateID(oldS), nil)
			newStorage.data[g] = paths
		}
	}
	d.transitions, d.anchorTransitions, d.tagUpdateIndices, d.anchorTagUpdateIndices, d.accepting, d.stateMatchPriority, d.stateMatchTags, d.stateMatchUpdates, d.stateIsBestMatch, d.stateIsSearch, d.stateToMask, d.storage, d.numStates, d.searchState, d.matchState = newTransitions, newAnchorTransitions, newUpdateIndices, newAnchorUpdateIndices, newAccepting, newPrio, newMatchTags, newStateMatchUpdates, newBest, newIsSearch, newMasks, newStorage, int(numGroups), StateID(stateToGroup[d.searchState]), StateID(stateToGroup[d.matchState])
}

func (d *DFA) computePhase2Metadata(prog *syntax.Prog) {
	d.isAlwaysTrue, d.warpPoints, d.warpPointState, d.warpPointGuards = make([]bool, d.numStates), make([]int16, d.numStates), make([]StateID, d.numStates), make([]syntax.EmptyOp, d.numStates)
	for i := range d.warpPoints {
		d.warpPoints[i] = -1
		d.warpPointState[i] = InvalidState
	}

	d.findWarpPoints()
	d.findSCCs()
}

func (d *DFA) computePhase3Metadata(prog *syntax.Prog) {
}

func (d *DFA) findWarpPoints() {
	for i := 0; i < d.numStates; i++ {
		currState := StateID(i)
		if d.accepting[i] {
			continue
		}
		progressByte, targetState, possible := -1, InvalidState, true
		for b := 0; b < 256; b++ {
			nextRaw := d.transitions[i*256+b]
			if nextRaw == InvalidState {
				continue
			}
			next := nextRaw & StateIDMask
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
			continue
		}

		for bit := 0; bit < 6; bit++ {
			s_ctx := d.anchorTransitions[i*6+bit]
			if s_ctx == InvalidState {
				continue
			}
			s_ctx &= StateIDMask
			if s_ctx == currState {
				continue
			}
			pByte, tState, ok := -1, InvalidState, true
			for b := 0; b < 256; b++ {
				nextRaw := d.transitions[int(s_ctx)*256+b]
				if nextRaw == InvalidState {
					continue
				}
				next := nextRaw & StateIDMask
				if next == s_ctx || next == currState {
					continue
				}
				if pByte == -1 {
					pByte = b
					tState = next
				} else {
					ok = false
					break
				}
			}
			if ok && pByte != -1 {
				d.warpPoints[i] = int16(pByte)
				d.warpPointState[i] = tState
				d.warpPointGuards[i] = syntax.EmptyOp(1 << bit)
				break
			}
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
			nextRaw := d.transitions[v*256+b]
			if nextRaw == -1 {
				continue
			}
			w := int(nextRaw & StateIDMask)
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
						nextRaw := d.transitions[s*256+b]
						if nextRaw == -1 {
							allTrans = false
							break
						}
						next := int(nextRaw & StateIDMask)
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
func matchesByte(node *UTF8Node, b byte) bool {
	return node.Match(b, false)
}
func matchesByteFold(node *UTF8Node, b byte) bool { return matchesByte(node, b) }

func (d *DFA) checkCompatibility(prog *syntax.Prog) error {
	visited := make([]bool, len(prog.Inst))
	onStack := make([]bool, len(prog.Inst))
	var hasCycle func(int) bool
	hasCycle = func(id int) bool {
		if onStack[id] {
			return true
		}
		if visited[id] {
			return false
		}
		visited[id] = true
		onStack[id] = true
		defer func() { onStack[id] = false }()

		inst := prog.Inst[id]
		switch inst.Op {
		case syntax.InstAlt, syntax.InstAltMatch:
			if hasCycle(int(inst.Out)) || hasCycle(int(inst.Arg)) {
				return true
			}
		case syntax.InstCapture, syntax.InstNop, syntax.InstEmptyWidth:
			if hasCycle(int(inst.Out)) {
				return true
			}
		}
		return false
	}

	for i := range prog.Inst {
		for j := range visited {
			visited[j] = false
		}
		if hasCycle(i) {
			return fmt.Errorf("DFA: unsupported epsilon loop at instruction %d", i)
		}
	}
	return nil
}

type PathTagUpdate struct {
	RelativePriority int32
	NextPriority     int32
	Tags             uint64
}

func epsilonClosureWithPathTags(paths []NFAPath, prog *syntax.Prog, context syntax.EmptyOp, nodes []*UTF8Node) ([]NFAPath, []PathTagUpdate) {
	// minPriority tracks the best priority reached for each (NFA ID, NodeID).
	type stateKey struct {
		ID     uint32
		NodeID uint32
	}
	minPriority := make(map[stateKey]int32)
	for _, p := range paths {
		key := stateKey{p.ID, p.NodeID}
		if prio, ok := minPriority[key]; !ok || p.Priority < prio {
			minPriority[key] = p.Priority
		}
	}

	type pathWithNewTags struct {
		p          NFAPath
		newTags    uint64
		sourcePrio int32
	}
	stack := make([]pathWithNewTags, len(paths))
	for i, p := range paths {
		stack[i] = pathWithNewTags{p, 0, p.Priority}
	}

	var updates []PathTagUpdate
	// resultPathsMap handles multiplexing: (ID, NodeID, Tags) -> NFAPath
	type resultMapKey struct {
		ID     uint32
		NodeID uint32
		Tags   uint64
	}
	resultPathsMap := make(map[resultMapKey]NFAPath)

	for len(stack) > 0 {
		ph := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		p := ph.p

		key := stateKey{p.ID, p.NodeID}
		if p.Priority > minPriority[key] {
			// This path is a loser; it can never win against a higher priority path reaching this state.
			continue
		}

		// Non-epsilon state or anchor that didn't match.
		if p.NodeID != 0 || !isEpsilon(prog.Inst[p.ID].Op) {
			rk := resultMapKey{p.ID, p.NodeID, p.Tags}
			if existing, ok := resultPathsMap[rk]; !ok || p.Priority < existing.Priority {
				resultPathsMap[rk] = p
			}

			updates = append(updates, PathTagUpdate{
				RelativePriority: ph.sourcePrio,
				NextPriority:     p.Priority,
				Tags:             ph.newTags,
			})
		}

		if p.NodeID == 0 {
			inst := prog.Inst[p.ID]
			switch inst.Op {
			case syntax.InstAlt, syntax.InstAltMatch:
				nextPrio := p.Priority // In our model, Alt doesn't always increase priority, it's about path order.
				// But to distinguish paths from the SAME start, we use the original logic if needed.
				// However, for DFA, we only care about the winning path.

				// Standard greedy: Out (loop/next) is higher priority than Arg (alternative).
				// We keep the original order: ph.p.Out, then ph.p.Arg.

				// Arg path (lower priority)
				argPrio := nextPrio + 1
				argKey := stateKey{inst.Arg, 0}
				if m, ok := minPriority[argKey]; !ok || argPrio < m {
					minPriority[argKey] = argPrio
					stack = append(stack, pathWithNewTags{NFAPath{NFAState: NFAState{ID: inst.Arg, NodeID: 0}, Priority: argPrio, Tags: p.Tags}, ph.newTags, ph.sourcePrio})
				} else if argPrio == m {
					stack = append(stack, pathWithNewTags{NFAPath{NFAState: NFAState{ID: inst.Arg, NodeID: 0}, Priority: argPrio, Tags: p.Tags}, ph.newTags, ph.sourcePrio})
				}

				// Out path (higher priority)
				outPrio := nextPrio
				outKey := stateKey{inst.Out, 0}
				if m, ok := minPriority[outKey]; !ok || outPrio < m {
					minPriority[outKey] = outPrio
					stack = append(stack, pathWithNewTags{NFAPath{NFAState: NFAState{ID: inst.Out, NodeID: 0}, Priority: outPrio, Tags: p.Tags}, ph.newTags, ph.sourcePrio})
				} else if outPrio == m {
					stack = append(stack, pathWithNewTags{NFAPath{NFAState: NFAState{ID: inst.Out, NodeID: 0}, Priority: outPrio, Tags: p.Tags}, ph.newTags, ph.sourcePrio})
				}
				continue
			case syntax.InstCapture:
				tagBit := uint64(0)
				if inst.Arg < 64 {
					tagBit = (1 << inst.Arg)
				}
				nextKey := stateKey{inst.Out, 0}
				if m, ok := minPriority[nextKey]; !ok || p.Priority <= m {
					minPriority[nextKey] = p.Priority
					stack = append(stack, pathWithNewTags{NFAPath{NFAState: NFAState{ID: inst.Out, NodeID: 0}, Priority: p.Priority, Tags: p.Tags | tagBit}, ph.newTags | tagBit, ph.sourcePrio})
				}
				continue
			case syntax.InstNop:
				nextKey := stateKey{inst.Out, 0}
				if m, ok := minPriority[nextKey]; !ok || p.Priority <= m {
					minPriority[nextKey] = p.Priority
					stack = append(stack, pathWithNewTags{NFAPath{NFAState: NFAState{ID: inst.Out, NodeID: 0}, Priority: p.Priority, Tags: p.Tags}, ph.newTags, ph.sourcePrio})
				}
				continue
			case syntax.InstEmptyWidth:
				if syntax.EmptyOp(inst.Arg)&context == syntax.EmptyOp(inst.Arg) {
					nextKey := stateKey{inst.Out, 0}
					if m, ok := minPriority[nextKey]; !ok || p.Priority <= m {
						minPriority[nextKey] = p.Priority
						stack = append(stack, pathWithNewTags{NFAPath{NFAState: NFAState{ID: inst.Out, NodeID: 0}, Priority: p.Priority, Tags: p.Tags}, ph.newTags, ph.sourcePrio})
					}
				}
				continue
			}
		}
	}

	var resultPaths []NFAPath
	for _, p := range resultPathsMap {
		resultPaths = append(resultPaths, p)
	}

	minTotalPrio := int32(1 << 30)
	if len(resultPaths) > 0 {
		minTotalPrio = resultPaths[0].Priority
		for _, p := range resultPaths {
			if p.Priority < minTotalPrio {
				minTotalPrio = p.Priority
			}
		}
		for i := range resultPaths {
			resultPaths[i].Priority -= minTotalPrio
		}
		for i := range updates {
			updates[i].NextPriority -= minTotalPrio
		}
	}

	sort.Slice(resultPaths, func(i, j int) bool {
		if resultPaths[i].ID != resultPaths[j].ID {
			return resultPaths[i].ID < resultPaths[j].ID
		}
		if resultPaths[i].NodeID != resultPaths[j].NodeID {
			return resultPaths[i].NodeID < resultPaths[j].NodeID
		}
		if resultPaths[i].Priority != resultPaths[j].Priority {
			return resultPaths[i].Priority < resultPaths[j].Priority
		}
		return resultPaths[i].Tags < resultPaths[j].Tags
	})

	if len(updates) > 1 {
		sort.Slice(updates, func(i, j int) bool {
			if updates[i].RelativePriority != updates[j].RelativePriority {
				return updates[i].RelativePriority < updates[j].RelativePriority
			}
			if updates[i].NextPriority != updates[j].NextPriority {
				return updates[i].NextPriority < updates[j].NextPriority
			}
			return updates[i].Tags < updates[j].Tags
		})
		unique := updates[:0]
		for i := 0; i < len(updates); i++ {
			if i == 0 || updates[i] != updates[i-1] {
				unique = append(unique, updates[i])
			}
		}
		updates = unique
	}

	return resultPaths, updates
}

func isEpsilon(op syntax.InstOp) bool {
	switch op {
	case syntax.InstAlt, syntax.InstAltMatch, syntax.InstCapture, syntax.InstNop, syntax.InstEmptyWidth:
		return true
	}
	return false
}
