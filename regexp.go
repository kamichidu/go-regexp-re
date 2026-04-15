package regexp

import (
	"bytes"
	"context"
	"fmt"
	"math/bits"
	"unsafe"

	"github.com/kamichidu/go-regexp-re/internal/ir"
	"github.com/kamichidu/go-regexp-re/syntax"
)

type matchStrategy uint8

const (
	strategyNone matchStrategy = iota
	strategyLiteral
	strategyBitParallel
	strategyFast
	strategyExtended
)

type Regexp struct {
	expr           string
	numSubexp      int
	prefix         []byte
	prefixState    ir.StateID
	complete       bool
	anchorStart    bool
	anchorEnd      bool
	prog           *syntax.Prog
	dfa            *ir.DFA
	bpDfa          *ir.BitParallelDFA
	literalMatcher *ir.LiteralMatcher
	subexpNames    []string
	strategy       matchStrategy
}

type CompileOptions struct{ MaxMemory int }

func Compile(expr string) (*Regexp, error) { return CompileContext(context.Background(), expr) }
func CompileWithOptions(expr string, opt CompileOptions) (*Regexp, error) {
	return CompileContextWithOptions(context.Background(), expr, opt)
}
func CompileContext(ctx context.Context, expr string) (*Regexp, error) {
	return CompileContextWithOptions(ctx, expr, CompileOptions{MaxMemory: ir.MaxDFAMemory})
}
func CompileContextWithOptions(ctx context.Context, expr string, opt CompileOptions) (*Regexp, error) {
	s, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return nil, err
	}
	numSubexp := s.MaxCap()
	s = syntax.Simplify(s)
	prog, err := syntax.Compile(s)
	if err != nil {
		return nil, err
	}

	subexpNames := make([]string, numSubexp+1)
	extractCapNames(s, subexpNames)

	literalMatcher := ir.AnalyzeLiteralPattern(s, numSubexp+1)

	complete := false
	prefixStr := ""
	if s.Op == syntax.OpConcat && len(s.Sub) > 0 && s.Sub[0].Op == syntax.OpLiteral {
		prefixStr = string(s.Sub[0].Rune)
		if len(s.Sub) == 1 {
			complete = true
		}
	} else if s.Op == syntax.OpLiteral {
		prefixStr = string(s.Rune)
		complete = true
	}

	anchorStart := false
	if s.Op == syntax.OpConcat && len(s.Sub) > 0 {
		if s.Sub[0].Op == syntax.OpBeginText {
			anchorStart = true
		}
	} else if s.Op == syntax.OpBeginText {
		anchorStart = true
	}

	anchorEnd := false
	if s.Op == syntax.OpConcat && len(s.Sub) > 0 {
		if s.Sub[len(s.Sub)-1].Op == syntax.OpEndText {
			anchorEnd = true
		}
	} else if s.Op == syntax.OpEndText {
		anchorEnd = true
	}

	var dfa *ir.DFA
	var bpDfa *ir.BitParallelDFA

	if isSimpleForBP(prog) && prog.NumCap <= 2 {
		bpDfa = ir.NewBitParallelDFA(prog)
	}
	if bpDfa == nil {
		var err error
		dfa, err = ir.NewDFAWithMemoryLimit(ctx, prog, opt.MaxMemory)
		if err != nil {
			return nil, err
		}
		if len(prog.Inst) <= 62 {
			bpDfa = ir.NewBitParallelDFA(prog)
		}
	}

	prefixState := ir.InvalidState
	if dfa != nil {
		prefixState = dfa.SearchState()
		if anchorStart {
			prefixState = dfa.MatchState()
		}
		for i := 0; i < len(prefixStr); i++ {
			if prefixState != ir.InvalidState {
				rawNext := dfa.Transitions()[(int(prefixState)<<8)|int(prefixStr[i])]
				if rawNext == ir.InvalidState {
					prefixState = ir.InvalidState
					break
				}
				prefixState = rawNext & ir.StateIDMask
			}
		}
	}

	res := &Regexp{expr: expr, numSubexp: numSubexp, prefix: []byte(prefixStr), prefixState: prefixState, complete: complete, anchorStart: anchorStart, anchorEnd: anchorEnd, prog: prog, dfa: dfa, bpDfa: bpDfa, literalMatcher: literalMatcher, subexpNames: subexpNames}
	res.bindMatchStrategy()
	return res, nil
}

func isSimpleForBP(prog *syntax.Prog) bool {
	if len(prog.Inst) > 62 {
		return false
	}
	for _, inst := range prog.Inst {
		switch inst.Op {
		case syntax.InstAlt, syntax.InstAltMatch:
			return false
		case syntax.InstRune, syntax.InstRune1, syntax.InstRuneAny, syntax.InstRuneAnyNotNL:
			if inst.Op == syntax.InstRuneAny || inst.Op == syntax.InstRuneAnyNotNL {
				return false
			}
			for _, r := range inst.Rune {
				if r > 127 {
					return false
				}
			}
		}
	}
	return true
}

func (re *Regexp) bindMatchStrategy() {
	if re.literalMatcher != nil {
		re.strategy = strategyLiteral
	} else if re.dfa != nil {
		if re.dfa.HasAnchors() {
			re.strategy = strategyExtended
		} else {
			re.strategy = strategyFast
		}
	} else if re.bpDfa != nil {
		re.strategy = strategyBitParallel
	}
}

func (re *Regexp) isGreedyMatch() bool {
	for _, inst := range re.prog.Inst {
		if (inst.Op == syntax.InstAlt || inst.Op == syntax.InstAltMatch) && inst.Arg > inst.Out {
			return true
		}
	}
	return false
}

func (re *Regexp) Match(b []byte) bool {
	var start int
	switch re.strategy {
	case strategyLiteral:
		indices := re.literalMatcher.FindSubmatchIndex(b)
		if indices != nil {
			start = indices[0]
		} else {
			start = -1
		}
	case strategyBitParallel:
		start, _, _, _ = bitParallelExecLoop(re, b)
	case strategyExtended:
		start, _, _, _ = extendedExecLoop(re, b)
	case strategyFast:
		start, _, _, _ = fastExecLoop(re, b)
	default:
		start = -1
	}
	return start >= 0
}

func (re *Regexp) MatchString(s string) bool {
	b := unsafe.Slice(unsafe.StringData(s), len(s))
	return re.Match(b)
}

func (re *Regexp) NumSubexp() int                { return re.numSubexp }
func (re *Regexp) LiteralPrefix() (string, bool) { return string(re.prefix), re.complete }

func (re *Regexp) FindSubmatchIndex(b []byte) []int {
	switch re.strategy {
	case strategyLiteral:
		return re.literalMatcher.FindSubmatchIndex(b)
	case strategyBitParallel:
		start, end, _, _ := bitParallelExecLoop(re, b)
		if start < 0 {
			return nil
		}
		regs := make([]int, (re.numSubexp+1)*2)
		for i := range regs {
			regs[i] = -1
		}
		regs[0], regs[1] = start, end
		if re.numSubexp == 0 {
			return regs
		}
		var mc matchContext
		mc.prepare(end-start, 1)
		re.bpRescanLoop(&mc, b, start, end, regs)
		return regs
	case strategyExtended:
		_, _, regs := submatchExecLoop(extendedMatchTrait{}, re, b)
		return regs
	case strategyFast:
		_, _, regs := submatchExecLoop(fastMatchTrait{}, re, b)
		return regs
	default:
		return nil
	}
}

// matchContext manages pre-allocated buffers for submatch extraction.
type matchContext struct {
	historyBuf     [1024]ir.StateID
	masksBuf       [512]uint64
	pathBuf        [1024]uint32
	history        []ir.StateID
	masks          []uint64
	path           []uint32
	visitedBuf     []uint64
	activeBuf      []uint64
	currPaths      []ir.NFAPath
	nextPaths      []ir.NFAPath
	nfaPathHistory []ir.NFAPath
	matchedOut     []uint32
	matchedNodeIDs []uint32
	updatesBuf     [512]uint32
	updates        []uint32
	stride         int
}

func (mc *matchContext) prepare(matchLen int, stride int) {
	mc.stride = stride
	if matchLen+1 > len(mc.historyBuf) {
		mc.history = make([]ir.StateID, matchLen+1)
	} else {
		mc.history = mc.historyBuf[:matchLen+1]
	}
	totalMasks := (matchLen + 1) * stride
	if totalMasks > len(mc.masksBuf) {
		mc.masks = make([]uint64, totalMasks)
	} else {
		mc.masks = mc.masksBuf[:totalMasks]
	}
	if matchLen > len(mc.pathBuf) {
		mc.path = make([]uint32, matchLen)
	} else {
		mc.path = mc.pathBuf[:matchLen]
	}
	if len(mc.visitedBuf) < stride {
		mc.visitedBuf = make([]uint64, stride)
		mc.activeBuf = make([]uint64, stride)
	}
	if len(mc.nfaPathHistory) < matchLen {
		mc.nfaPathHistory = make([]ir.NFAPath, matchLen)
	}
	if mc.matchedOut == nil {
		mc.matchedOut = make([]uint32, 0, 16)
		mc.matchedNodeIDs = make([]uint32, 0, 16)
	}
	if matchLen > len(mc.updatesBuf) {
		mc.updates = make([]uint32, matchLen)
	} else {
		mc.updates = mc.updatesBuf[:matchLen]
	}
}

func (re *Regexp) bpRescanLoop(mc *matchContext, b []byte, start, end int, regs []int) {
	bp := re.bpDfa
	masks := mc.masks

	ctx := ir.CalculateContext(b, start)
	state := re.epsilonClosureWithContext(re.prog.Start, ctx)
	masks[0] = state

	searchMask := state
	for i := start; i < end; i++ {
		ctx := ir.CalculateContext(b, i)
		active := state & bp.ContextMasks[ctx]
		matched := active & bp.CharMasks[b[i]]
		state = bp.SuccessorTable[0][matched&0xff] |
			bp.SuccessorTable[1][(matched>>8)&0xff] |
			bp.SuccessorTable[2][(matched>>16)&0xff] |
			bp.SuccessorTable[3][(matched>>24)&0xff] |
			bp.SuccessorTable[4][(matched>>32)&0xff] |
			bp.SuccessorTable[5][(matched>>40)&0xff] |
			bp.SuccessorTable[6][(matched>>48)&0xff] |
			bp.SuccessorTable[7][(matched>>56)&0xff]
		if !re.anchorStart {
			state |= searchMask
		}
		masks[i-start+1] = state
	}

	re.backwardTraceback(mc, b, start, end, regs)
}

func (re *Regexp) backwardTraceback(mc *matchContext, b []byte, start, end int, regs []int) {
	dfa := re.dfa
	if dfa == nil {
		return
	}
	history := mc.history
	matchLen := end - start

	matchID := uint32(0)
	for i, inst := range re.prog.Inst {
		if inst.Op == syntax.InstMatch {
			matchID = uint32(i)
			break
		}
	}

	if end == start {
		ctx := ir.CalculateContext(b, start)
		paths, _ := dfa.GetNFAContext(history[0], nil)
		for _, p := range paths {
			if re.reachesBitWithContext(mc, p.ID, matchID, ctx) {
				re.applyPathTagsNP(mc, ir.NFAPath{NFAState: ir.NFAState{ID: uint32(re.prog.Start), NodeID: 0}}, p, start, regs, b)
				re.applyPathTagsNP(mc, p, ir.NFAPath{NFAState: ir.NFAState{ID: matchID, NodeID: 0}}, end, regs, b)
				return
			}
		}
		return
	}

	// 1. Identify winner NP at end.
	endPaths, _ := dfa.GetNFAContext(history[matchLen], nil)
	finalCtx := ir.CalculateContext(b, end)
	var chosen ir.NFAPath
	found := false
	for _, p := range endPaths {
		if p.ID == matchID || re.reachesBitWithContext(mc, p.ID, matchID, finalCtx) {
			chosen = p
			found = true
			break
		}
	}
	if !found {
		return
	}

	// 2. Trace backward using NFAPath.
	for i := matchLen - 1; i >= 0; i-- {
		mc.nfaPathHistory[i] = chosen
		pos := start + i
		prevPaths, _ := dfa.GetNFAContext(history[i], nil)
		ctx := ir.CalculateContext(b, pos)

		found := false
		for _, p := range prevPaths {
			if re.canTransitionNP(mc, p, chosen, b, pos, ctx) {
				chosen = p
				found = true
				break
			}
		}
		if !found {
			return
		}
	}

	// 3. Recap tags using NP history.
	currNP := ir.NFAPath{NFAState: ir.NFAState{ID: uint32(re.prog.Start), NodeID: 0}}
	for i := 0; i < matchLen; i++ {
		re.applyPathTagsNP(mc, currNP, mc.nfaPathHistory[i], start+i, regs, b)
		currNP = mc.nfaPathHistory[i]
	}
	re.applyPathTagsNP(mc, currNP, ir.NFAPath{NFAState: ir.NFAState{ID: matchID, NodeID: 0}}, end, regs, b)
}

func (re *Regexp) canTransitionNP(mc *matchContext, from, to ir.NFAPath, b []byte, pos int, ctx syntax.EmptyOp) bool {
	inst := re.prog.Inst[from.ID]
	mc.matchedOut = mc.matchedOut[:0]
	mc.matchedNodeIDs = mc.matchedNodeIDs[:0]
	byteVal := b[pos]

	if from.NodeID == 0 {
		switch inst.Op {
		case syntax.InstRune, syntax.InstRune1, syntax.InstRuneAny, syntax.InstRuneAnyNotNL:
			roots := re.dfa.TrieRoots()[from.ID]
			fold := inst.Op == syntax.InstRune && (inst.Arg&uint32(syntax.FoldCase) != 0)
			for _, root := range roots {
				if root.Match(byteVal, fold) {
					if root.Next == nil {
						mc.matchedOut = append(mc.matchedOut, inst.Out)
					} else {
						for _, child := range root.Next {
							mc.matchedNodeIDs = append(mc.matchedNodeIDs, uint32(child.ID))
						}
					}
				}
			}
		}
	} else {
		node := re.dfa.Nodes()[from.NodeID]
		fold := inst.Op == syntax.InstRune && (inst.Arg&uint32(syntax.FoldCase) != 0)
		if node.Match(byteVal, fold) {
			if node.Next == nil {
				mc.matchedOut = append(mc.matchedOut, inst.Out)
			} else {
				for _, child := range node.Next {
					mc.matchedNodeIDs = append(mc.matchedNodeIDs, uint32(child.ID))
				}
			}
		}
	}

	for _, out := range mc.matchedOut {
		if out == to.ID && to.NodeID == 0 {
			return true
		}
		// エプシロン遷移で to に到達できるか
		if to.NodeID == 0 && re.reachesBitWithContext(mc, out, to.ID, ir.CalculateContext(b, pos+1)) {
			return true
		}
	}
	for _, nodeID := range mc.matchedNodeIDs {
		if from.ID == to.ID && nodeID == to.NodeID {
			return true
		}
	}
	return false
}

func (re *Regexp) applyPathTagsNP(mc *matchContext, start, target ir.NFAPath, pos int, regs []int, b []byte) bool {
	if start.ID == target.ID && start.NodeID == target.NodeID {
		return true
	}
	if start.NodeID != 0 || target.NodeID != 0 {
		return true
	}

	ctx := ir.CalculateContext(b, pos)
	var stack []uint32
	stack = append(stack, start.ID)
	parent := make(map[uint32]uint32)
	found := false

	for len(stack) > 0 {
		curr := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if curr == target.ID {
			found = true
			break
		}
		inst := re.prog.Inst[curr]
		var next []uint32
		switch inst.Op {
		case syntax.InstAlt, syntax.InstAltMatch:
			next = []uint32{inst.Arg, inst.Out}
		case syntax.InstCapture, syntax.InstNop:
			next = []uint32{inst.Out}
		case syntax.InstEmptyWidth:
			if (syntax.EmptyOp(inst.Arg) & ctx) == syntax.EmptyOp(inst.Arg) {
				next = []uint32{inst.Out}
			}
		}
		for _, n := range next {
			if _, ok := parent[n]; !ok {
				parent[n] = curr
				stack = append(stack, n)
			}
		}
	}
	if found {
		curr := target.ID
		for curr != start.ID {
			p := parent[curr]
			inst := re.prog.Inst[p]
			if inst.Op == syntax.InstCapture {
				if int(inst.Arg) < len(regs) {
					println("  Tag:", inst.Arg, "at", pos)
					regs[inst.Arg] = pos
				}
			}
			curr = p
		}
	} else {
		println("  NO PATH from", start.ID, "to", target.ID)
	}
	return true
}

func (re *Regexp) reachesBitWithContext(mc *matchContext, start, target uint32, ctx syntax.EmptyOp) bool {
	if start == target {
		return true
	}
	var stackBuf [128]uint32
	stack := stackBuf[:0]
	stack = append(stack, start)
	stride := mc.stride
	visited := mc.visitedBuf
	for i := 0; i < stride; i++ {
		visited[i] = 0
	}

	for len(stack) > 0 {
		curr := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if curr == target {
			return true
		}
		if (visited[int(curr/64)] & (1 << (curr % 64))) != 0 {
			continue
		}
		visited[int(curr/64)] |= (1 << (curr % 64))

		inst := re.prog.Inst[curr]
		switch inst.Op {
		case syntax.InstAlt, syntax.InstAltMatch:
			stack = append(stack, inst.Arg, inst.Out)
		case syntax.InstCapture, syntax.InstNop:
			stack = append(stack, inst.Out)
		case syntax.InstEmptyWidth:
			if (syntax.EmptyOp(inst.Arg) & ctx) == syntax.EmptyOp(inst.Arg) {
				stack = append(stack, inst.Out)
			}
		}
	}
	return false
}

func (re *Regexp) reachesBit(mc *matchContext, start, target uint32) bool {
	if start == target {
		return true
	}
	var stackBuf [128]uint32
	stack := stackBuf[:0]
	stack = append(stack, start)
	stride := mc.stride
	visited := mc.visitedBuf
	for i := 0; i < stride; i++ {
		visited[i] = 0
	}

	for len(stack) > 0 {
		curr := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if curr == target {
			return true
		}
		if (visited[int(curr/64)] & (1 << (curr % 64))) != 0 {
			continue
		}
		visited[int(curr/64)] |= (1 << (curr % 64))

		inst := re.prog.Inst[curr]
		switch inst.Op {
		case syntax.InstAlt, syntax.InstAltMatch:
			stack = append(stack, inst.Arg, inst.Out)
		case syntax.InstCapture, syntax.InstNop, syntax.InstEmptyWidth:
			stack = append(stack, inst.Out)
		}
	}
	return false
}

func (re *Regexp) epsilonClosureWithContext(start int, ctx syntax.EmptyOp) uint64 {
	var active uint64
	var visited uint64
	var stackBuf [128]int
	stack := stackBuf[:0]
	stack = append(stack, start)

	for len(stack) > 0 {
		curr := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if curr < 0 || curr >= 64 {
			continue
		}
		if (visited & (1 << uint(curr))) != 0 {
			continue
		}
		visited |= (1 << uint(curr))
		inst := re.prog.Inst[curr]
		switch inst.Op {
		case syntax.InstAlt, syntax.InstAltMatch:
			stack = append(stack, int(inst.Arg), int(inst.Out))
		case syntax.InstCapture, syntax.InstNop:
			stack = append(stack, int(inst.Out))
		case syntax.InstEmptyWidth:
			if (syntax.EmptyOp(inst.Arg) & ctx) == syntax.EmptyOp(inst.Arg) {
				stack = append(stack, int(inst.Out))
			}
		default:
			active |= (1 << uint(curr))
		}
	}
	return active
}

func applyTags(t uint64, pos int, regs []int) {
	for t != 0 {
		i := bits.TrailingZeros64(t)
		if i < len(regs) {
			if i%2 == 0 {
				if regs[i] == -1 {
					regs[i] = pos
				}
			} else {
				regs[i] = pos
			}
		}
		t &= ^(uint64(1) << i)
	}
}

func (re *Regexp) applyContextToState(d *ir.DFA, state ir.StateID, context syntax.EmptyOp, pos int, currentPrio *int, targetPrio int, regs []int) ir.StateID {
	if state == ir.InvalidState || context == 0 {
		return state
	}
	tagUpdates := d.TagUpdates()
	anchorTagUpdateIndices := d.AnchorTagUpdateIndices()
	for iter := 0; iter < 6; iter++ {
		changed := false
		for bit := 0; bit < 6; bit++ {
			if (context & (1 << uint(bit))) != 0 {
				idx := int(state)*6 + bit
				rawNext := d.AnchorNext(state, bit)
				if rawNext != ir.InvalidState {
					nextID := rawNext & ir.StateIDMask
					if nextID != state {
						if rawNext < 0 {
							update := tagUpdates[anchorTagUpdateIndices[idx]]
							nextPrio := 0
							if currentPrio != nil {
								nextPrio = *currentPrio + int(update.BasePriority)
							}
							if regs != nil {
								for _, tu := range update.PreUpdates {
									if nextPrio+int(tu.RelativePriority) <= targetPrio {
										applyTags(tu.Tags, pos, regs)
									}
								}
								for _, tu := range update.PostUpdates {
									if nextPrio+int(tu.RelativePriority) <= targetPrio {
										applyTags(tu.Tags, pos, regs)
									}
								}
							}
							if currentPrio != nil {
								*currentPrio = nextPrio
							}
						}
						state = nextID
						changed = true
					}
				}
			}
		}
		if !changed {
			break
		}
	}
	return state
}

func bitParallelExecLoop(re *Regexp, b []byte) (int, int, int, uint64) {
	bp := re.bpDfa
	numBytes := len(b)
	bestStart, bestEnd, bestPriority, bestMatchTags := -1, -1, 1<<30-1, uint64(0)

	charMasks := &bp.CharMasks
	table := &bp.SuccessorTable

	for i := 0; i <= numBytes; i++ {
		ctx := ir.CalculateContext(b, i)
		state := re.epsilonClosureWithContext(re.prog.Start, ctx)

		for j := i; ; j++ {
			currContext := ir.CalculateContext(b, j)
			active := state & bp.ContextMasks[currContext]

			if (active & bp.MatchMasks[currContext]) != 0 {
				prio := i * ir.SearchRestartPenalty
				if prio < bestPriority || (prio == bestPriority && j >= bestEnd) {
					bestPriority, bestEnd, bestStart, bestMatchTags = prio, j, i, bp.MatchMask
				}
			}

			if j == numBytes {
				break
			}

			matched := active & charMasks[b[j]]
			if matched == 0 {
				break
			}

			state = table[0][matched&0xff] |
				table[1][(matched>>8)&0xff] |
				table[2][(matched>>16)&0xff] |
				table[3][(matched>>24)&0xff] |
				table[4][(matched>>32)&0xff] |
				table[5][(matched>>40)&0xff] |
				table[6][(matched>>48)&0xff] |
				table[7][(matched>>56)&0xff]
		}
		if bestStart != -1 {
			return bestStart, bestEnd, bestPriority, bestMatchTags
		}
		if re.anchorStart {
			break
		}
		if len(re.prefix) > 0 && i+1 < numBytes {
			skip := bytes.Index(b[i+1:], re.prefix)
			if skip < 0 {
				break
			}
			i += skip
		}
	}
	return bestStart, bestEnd, bestPriority, bestMatchTags
}

func fastExecLoop(re *Regexp, b []byte) (int, int, int, uint64) {
	d := re.dfa
	trans := d.Transitions()
	tagUpdateIndices := d.TagUpdateIndices()
	tagUpdates := d.TagUpdates()
	accepting := d.Accepting()
	numStates := d.NumStates()
	numBytes := len(b)
	anchorStart := re.anchorStart
	prefix := re.prefix
	prefixState := re.prefixState
	if len(trans) == 0 {
		return -1, -1, -1, 0
	}
	bestStart, bestEnd, bestPriority, bestMatchTags, currentPriority := -1, -1, 1<<30-1, uint64(0), 0
	state := d.SearchState()
	if anchorStart {
		state = d.MatchState()
	}
	for i := 0; i <= numBytes; {
		sidx := int(state)
		if sidx >= 0 && sidx < len(accepting) && accepting[sidx] {
			p := currentPriority + d.MatchPriority(state)
			if p <= bestPriority {
				bestPriority, bestEnd, bestMatchTags = p, i, d.MatchTags(state)
				bestStart = p / ir.SearchRestartPenalty
				if d.IsBestMatch(state) {
					return bestStart, bestEnd, bestPriority, bestMatchTags
				}
			}
		}
		if i < numBytes {
			if sidx >= 0 && sidx < numStates {
				off := (sidx << 8) | int(b[i])
				rawNext := trans[off]
				if rawNext != ir.InvalidState {
					state = rawNext & ir.StateIDMask
					if rawNext < 0 {
						update := tagUpdates[tagUpdateIndices[off]]
						currentPriority += int(update.BasePriority)
					}
					i++
					continue
				}
			}
		} else if i == numBytes {
			break
		}
		if bestStart != -1 {
			return bestStart, bestEnd, bestPriority, bestMatchTags
		}
		if anchorStart {
			break
		}
		currentPriority = (currentPriority/ir.SearchRestartPenalty + 1) * ir.SearchRestartPenalty
		i = currentPriority / ir.SearchRestartPenalty
		if i > numBytes {
			break
		}
		state = d.SearchState()
		warpByte := d.WarpPoint(state)
		if warpByte >= 0 {
			skip := bytes.Index(b[i:], []byte{byte(warpByte)})
			if skip < 0 {
				break
			}
			guard := d.WarpPointGuard(state)
			if guard != 0 {
				if ir.CalculateContext(b, i+skip)&guard == 0 {
					i += skip + 1
					currentPriority = (i * ir.SearchRestartPenalty)
					continue
				}
			}
			i += skip
			currentPriority = (i * ir.SearchRestartPenalty)
			state = d.WarpPointState(state)
			i++
		} else if len(prefix) > 0 {
			skip := bytes.Index(b[i:], prefix)
			if skip < 0 {
				break
			}
			i += skip
			currentPriority = (i * ir.SearchRestartPenalty)
			if prefixState != ir.InvalidState {
				state = prefixState
				i += len(prefix)
			}
		}
	}
	return bestStart, bestEnd, bestPriority, bestMatchTags
}

func extendedExecLoop(re *Regexp, b []byte) (int, int, int, uint64) {
	d := re.dfa
	trans := d.Transitions()
	tagUpdateIndices := d.TagUpdateIndices()
	tagUpdates := d.TagUpdates()
	accepting := d.Accepting()
	numStates := d.NumStates()
	numBytes := len(b)
	anchorStart := re.anchorStart
	prefix := re.prefix
	prefixState := re.prefixState
	if len(trans) == 0 {
		return -1, -1, -1, 0
	}
	bestStart, bestEnd, bestPriority, bestMatchTags, currentPriority := -1, -1, 1<<30-1, uint64(0), 0
	state := d.SearchState()
	if anchorStart {
		state = d.MatchState()
	}
	usedAnchors := d.UsedAnchors()
	for i := 0; i <= numBytes; {
		if (usedAnchors&(syntax.EmptyWordBoundary|syntax.EmptyNoWordBoundary)) != 0 ||
			(i == 0 && (usedAnchors&(syntax.EmptyBeginText|syntax.EmptyBeginLine)) != 0) ||
			(i == numBytes && (usedAnchors&(syntax.EmptyEndText|syntax.EmptyEndLine)) != 0) ||
			((usedAnchors&syntax.EmptyBeginLine) != 0 && i > 0 && b[i-1] == '\n') ||
			((usedAnchors&syntax.EmptyEndLine) != 0 && i < numBytes && b[i] == '\n') {
			state = re.applyContextToState(d, state, ir.CalculateContext(b, i), i, &currentPriority, 1<<30-1, nil)
		}
		sidx := int(state)
		if sidx >= 0 && sidx < len(accepting) && accepting[sidx] {
			p := currentPriority + d.MatchPriority(state)
			if p <= bestPriority {
				bestPriority, bestEnd, bestMatchTags = p, i, d.MatchTags(state)
				bestStart = p / ir.SearchRestartPenalty
				if d.IsBestMatch(state) {
					return bestStart, bestEnd, bestPriority, bestMatchTags
				}
			}
		}
		if i < numBytes {
			if sidx >= 0 && sidx < numStates {
				off := (sidx << 8) | int(b[i])
				rawNext := trans[off]
				if rawNext != ir.InvalidState {
					state = rawNext & ir.StateIDMask
					if rawNext < 0 {
						update := tagUpdates[tagUpdateIndices[off]]
						currentPriority += int(update.BasePriority)
					}
					i++
					continue
				}
			}
		} else if i == numBytes {
			break
		}
		if bestStart != -1 {
			return bestStart, bestEnd, bestPriority, bestMatchTags
		}
		if anchorStart {
			break
		}
		currentPriority = (currentPriority/ir.SearchRestartPenalty + 1) * ir.SearchRestartPenalty
		i = currentPriority / ir.SearchRestartPenalty
		if i > numBytes {
			break
		}
		state = d.SearchState()
		warpByte := d.WarpPoint(state)
		if warpByte >= 0 {
			skip := bytes.Index(b[i:], []byte{byte(warpByte)})
			if skip < 0 {
				break
			}
			guard := d.WarpPointGuard(state)
			if guard != 0 {
				if ir.CalculateContext(b, i+skip)&guard == 0 {
					i += skip + 1
					currentPriority = (i * ir.SearchRestartPenalty)
					continue
				}
			}
			i += skip
			currentPriority = (i * ir.SearchRestartPenalty)
			state = d.WarpPointState(state)
			i++
		} else if len(prefix) > 0 {
			skip := bytes.Index(b[i:], prefix)
			if skip < 0 {
				break
			}
			i += skip
			currentPriority = (i * ir.SearchRestartPenalty)
			if prefixState != ir.InvalidState {
				state = prefixState
				i += len(prefix)
			}
		}
	}
	return bestStart, bestEnd, bestPriority, bestMatchTags
}

func (re *Regexp) FindStringSubmatchIndex(s string) []int {
	b := unsafe.Slice(unsafe.StringData(s), len(s))
	return re.FindSubmatchIndex(b)
}
func (re *Regexp) FindSubmatch(b []byte) [][]byte {
	indices := re.FindSubmatchIndex(b)
	if indices == nil {
		return nil
	}
	result := make([][]byte, len(indices)/2)
	for i := range result {
		if start, end := indices[2*i], indices[2*i+1]; start >= 0 && end >= 0 {
			result[i] = b[start:end]
		}
	}
	return result
}
func (re *Regexp) FindStringSubmatch(s string) []string {
	indices := re.FindStringSubmatchIndex(s)
	if indices == nil {
		return nil
	}
	result := make([]string, len(indices)/2)
	for i := range result {
		if start, end := indices[2*i], indices[2*i+1]; start >= 0 && end >= 0 {
			result[i] = s[start:end]
		}
	}
	return result
}
func extractCapNames(re *syntax.Regexp, names []string) {
	if re.Op == syntax.OpCapture {
		if re.Cap < len(names) {
			names[re.Cap] = re.Name
		}
	}
	for _, sub := range re.Sub {
		extractCapNames(sub, names)
	}
}
func quote(s string) string {
	if len(s) <= 16 {
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprintf("%q...", s[:16])
}

func MustCompile(expr string) *Regexp {
	re, err := Compile(expr)
	if err != nil {
		panic(`regexp: Compile(` + quote(expr) + `): ` + err.Error())
	}
	return re
}
func (re *Regexp) String() string { return re.expr }

func submatchExecLoop(trait loopTrait, re *Regexp, b []byte) (int, int, []int) {
	d := re.dfa
	trans := d.Transitions()
	tagUpdateIndices := d.TagUpdateIndices()
	tagUpdates := d.TagUpdates()
	accepting := d.Accepting()
	numStates := d.NumStates()
	numBytes := len(b)
	anchorStart := re.anchorStart
	prefix := re.prefix
	hasAnchors := trait.HasAnchors()
	usedAnchors := d.UsedAnchors()

	bestStart, bestEnd, bestPriority := -1, -1, 1<<30-1
	bestRegs := make([]int, (re.numSubexp+1)*2)
	for i := range bestRegs {
		bestRegs[i] = -1
	}

	tempRegs := make([]int, (re.numSubexp+1)*2)

	for i := 0; i <= numBytes; {
		currentPriority := i * ir.SearchRestartPenalty
		state := d.SearchState()
		if anchorStart {
			state = d.MatchState()
		}

		for k := range tempRegs {
			tempRegs[k] = -1
		}

		// Apply start updates
		for _, u := range dfaStartUpdates(re) {
			if currentPriority+int(u.RelativePriority) <= bestPriority {
				applyTags(u.Tags, i, tempRegs)
			}
		}

		for j := i; j <= numBytes; {
			if hasAnchors && ((usedAnchors&(syntax.EmptyWordBoundary|syntax.EmptyNoWordBoundary)) != 0 ||
				(j == 0 && (usedAnchors&(syntax.EmptyBeginText|syntax.EmptyBeginLine)) != 0) ||
				(j == numBytes && (usedAnchors&(syntax.EmptyEndText|syntax.EmptyEndLine)) != 0) ||
				((usedAnchors&syntax.EmptyBeginLine) != 0 && j > 0 && b[j-1] == '\n') ||
				((usedAnchors&syntax.EmptyEndLine) != 0 && j < numBytes && b[j] == '\n')) {
				state = re.applyContextToState(d, state, ir.CalculateContext(b, j), j, &currentPriority, bestPriority, tempRegs)
			}

			sidx := int(state)
			if sidx >= 0 && sidx < len(accepting) && accepting[sidx] {
				p := currentPriority + d.MatchPriority(state)
				if p <= bestPriority {
					bestPriority, bestEnd, bestStart = p, j, i
					copy(bestRegs, tempRegs)
					applyTags(d.MatchTags(state), j, bestRegs)
					bestRegs[0], bestRegs[1] = i, j
					if d.IsBestMatch(state) {
						return bestStart, bestEnd, bestRegs
					}
				}
			}

			if j < numBytes {
				if sidx >= 0 && sidx < numStates {
					off := (sidx << 8) | int(b[j])
					rawNext := trans[off]
					if rawNext != ir.InvalidState {
						state = rawNext & ir.StateIDMask
						if rawNext < 0 {
							update := tagUpdates[tagUpdateIndices[off]]
							currentPriority += int(update.BasePriority)
							for _, tu := range update.PreUpdates {
								if currentPriority+int(tu.RelativePriority) <= bestPriority {
									applyTags(tu.Tags, j, tempRegs)
								}
							}
							for _, tu := range update.PostUpdates {
								if currentPriority+int(tu.RelativePriority) <= bestPriority {
									applyTags(tu.Tags, j+1, tempRegs)
								}
							}
						}
						j++
						continue
					}
				}
			}
			break
		}

		if bestStart != -1 {
			return bestStart, bestEnd, bestRegs
		}
		if anchorStart {
			break
		}

		i++
		if i < numBytes {
			state = d.SearchState()
			warpByte := d.WarpPoint(state)
			if warpByte >= 0 {
				skip := bytes.Index(b[i:], []byte{byte(warpByte)})
				if skip < 0 {
					break
				}
				i += skip
			} else if len(prefix) > 0 {
				skip := bytes.Index(b[i:], prefix)
				if skip < 0 {
					break
				}
				i += skip
			}
		}
	}
	return bestStart, bestEnd, nil
}

func dfaStartUpdates(re *Regexp) []ir.PathTagUpdate {
	if re.dfa == nil {
		return nil
	}
	// This is a bridge to access dfa.startUpdates which is currently private
	return re.dfa.StartUpdates()
}

type loopTrait interface{ HasAnchors() bool }
type fastMatchTrait struct{}

func (fastMatchTrait) HasAnchors() bool { return false }

type extendedMatchTrait struct{}

func (extendedMatchTrait) HasAnchors() bool { return true }
