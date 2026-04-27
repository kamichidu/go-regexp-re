package ir

import (
	"unsafe"

	"github.com/kamichidu/go-regexp-re/syntax"
)

type StateID uint32

const (
	InvalidState uint32 = 0xFFFFFFFF
	// Fixed Canonical Layout:
	// Bit 31: Tagged (Transition carries tags or priority shift)
	// Bit 30: Anchor (Transition requires anchor verification)
	// Bit 29: Accepting (State is an accepting state)
	// Bit 28: CCWarp (State is a candidate for SWAR skip)
	// Bit 27-22: AnchorMask (verification flags)
	// Bit 21: Warp (Transition covers full UTF-8 character)
	// Bit 20-0: StateID (up to 2M states)
	TaggedStateFlag    uint32 = 0x80000000
	AnchorVerifyFlag   uint32 = 0x40000000
	AcceptingStateFlag uint32 = 0x20000000
	CCWarpFlag         uint32 = 0x10000000
	AnchorMask         uint32 = 0x0FC00000
	WarpStateFlag      uint32 = 0x00200000
	StateIDMask        uint32 = 0x000FFFFF
)

type CCWarpKernel int

const (
	CCWarpNone CCWarpKernel = iota
	CCWarpEqual
	CCWarpSingleRange
	CCWarpNotSingleRange
	CCWarpAnyChar
	CCWarpAnyExceptNL
	CCWarpNotEqual
	CCWarpNotEqualSet
	CCWarpEqualSet
	CCWarpNotBitmask
	CCWarpBitmask
)

type CCWarpInfo struct {
	Kernel   CCWarpKernel
	V0, V1   uint64   // Fast access for common kernels (Equal, Range, etc.)
	Extra    []uint64 // Fallback for large sets (EqualSet, Bitmask)
	IndexAny string   // Fast path for SearchWarp
}

const MaxDFAMemory = 64 * 1024 * 1024
const SearchRestartPenalty = 1000

type NFAPath struct {
	ID, NodeID uint32
	Priority   int32
	Anchors    syntax.EmptyOp
	Tags       uint64
}

const NFAPathSize = int(unsafe.Sizeof(NFAPath{}))

type DFA struct {
	numStates               int
	transitions             []uint32
	tagUpdateIndices        []uint32
	tagUpdates              []TransitionUpdate
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
	ccWarpTable             []CCWarpInfo
	searchWarp              CCWarpInfo
}

func (d *DFA) IsNaked() bool                  { return d.Naked }
func (d *DFA) NumStates() int                 { return d.numStates }
func (d *DFA) RecapTables() []GroupRecapTable { return d.recapTables }
func (d *DFA) CCWarpTable() []CCWarpInfo      { return d.ccWarpTable }
func (d *DFA) SearchWarp() CCWarpInfo         { return d.searchWarp }

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
