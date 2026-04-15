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

	// anchorStart is true only if the pattern is strictly anchored to the beginning of text (\A).
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
	var start, end, targetPriority int
	var matchTags uint64
	switch re.strategy {
	case strategyLiteral:
		indices := re.literalMatcher.FindSubmatchIndex(b)
		if indices != nil {
			start, end, targetPriority = indices[0], indices[1], 0
		} else {
			start = -1
		}
	case strategyBitParallel:
		start, end, targetPriority, matchTags = bitParallelExecLoop(re, b)
	case strategyExtended:
		start, end, targetPriority, matchTags = extendedExecLoop(re, b)
	case strategyFast:
		start, end, targetPriority, matchTags = fastExecLoop(re, b)
	default:
		start = -1
	}
	if start < 0 {
		return nil
	}
	return re.extractSubmatches(b, start, end, targetPriority, matchTags)
}

func (re *Regexp) extractSubmatches(b []byte, start, end, targetPriority int, matchTags uint64) []int {
	regs := make([]int, (re.numSubexp+1)*2)
	for i := range regs {
		regs[i] = -1
	}
	regs[0], regs[1] = start, end
	if start < 0 || end < 0 {
		return nil
	}

	if re.numSubexp == 0 {
		return regs
	}

	if re.bpDfa != nil && len(re.prog.Inst) <= 62 {
		bpRescanLoop(re, b, start, end, targetPriority, matchTags, regs)
		return regs
	}

	if re.dfa != nil {
		if re.dfa.HasAnchors() {
			rescanLoop[extendedMatchTrait](re, b, start, end, targetPriority, matchTags, regs)
		} else {
			rescanLoop[fastMatchTrait](re, b, start, end, targetPriority, matchTags, regs)
		}
	}

	return regs
}

func bpRescanLoop(re *Regexp, b []byte, start, end, targetPriority int, matchTags uint64, regs []int) {
	bp := re.bpDfa
	if bp == nil {
		return
	}

	matchLen := end - start
	var masksBuf [512]uint64
	masks := masksBuf[:]
	if matchLen+1 > len(masks) {
		masks = make([]uint64, matchLen+1)
	} else {
		masks = masks[:matchLen+1]
	}

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

	re.backwardTraceback(b, start, end, masks, regs)
}

func (re *Regexp) epsilonClosureWithContext(start int, ctx syntax.EmptyOp) uint64 {
	var active uint64
	var visited uint64
	var dfs func(int)
	dfs = func(curr int) {
		if (visited & (1 << uint(curr))) != 0 {
			return
		}
		visited |= (1 << uint(curr))
		inst := re.prog.Inst[curr]
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
			active |= (1 << uint(curr))
		}
	}
	dfs(start)
	return active
}

func rescanLoop[T loopTrait](re *Regexp, b []byte, start, end, targetPriority int, matchTags uint64, regs []int) {
	var trait T
	dfa := re.dfa
	trans, matchState := dfa.Transitions(), dfa.MatchState()

	matchLen := end - start
	var historyBuf [1024]ir.StateID // 4KiB
	history := historyBuf[:]
	if matchLen+1 > len(history) {
		history = make([]ir.StateID, matchLen+1)
	} else {
		history = history[:matchLen+1]
	}

	state := matchState
	if !re.anchorStart {
		state = dfa.SearchState()
	}
	history[0] = state

	hasAnchors := trait.HasAnchors()
	for i := start; i < end; i++ {
		if hasAnchors {
			state = re.applyContextToState(dfa, state, ir.CalculateContext(b, i), i, nil, 0, nil)
		}
		idx := (int(state) << 8) | int(b[i])
		rawNext := trans[idx]
		if rawNext == ir.InvalidState {
			break
		}
		state = rawNext & ir.StateIDMask
		history[i-start+1] = state
	}

	var masksBuf [512]uint64
	masks := masksBuf[:]
	if len(history) > len(masks) {
		masks = make([]uint64, len(history))
	} else {
		masks = masks[:len(history)]
	}
	for i, sid := range history {
		masks[i] = dfa.StateToMask(sid)
	}
	re.backwardTraceback(b, start, end, masks, regs)
}

func (re *Regexp) backwardTraceback(b []byte, start, end int, masks []uint64, regs []int) {
	bp := re.bpDfa
	if bp == nil {
		return
	}

	matchID := uint32(0)
	for i, inst := range re.prog.Inst {
		if inst.Op == syntax.InstMatch {
			matchID = uint32(i)
			break
		}
	}

	if end == start {
		ctx := ir.CalculateContext(b, start)
		state := masks[0] & bp.ContextMasks[ctx]
		for k := 0; k < 64; k++ {
			if (state & (1 << uint(k))) != 0 {
				if re.reachesBitWithContext(uint32(k), matchID, ctx) {
					re.applyPathTags(uint32(re.prog.Start), uint32(k), start, regs, b)
					re.applyPathTags(uint32(k), matchID, end, regs, b)
					return
				}
			}
		}
		return
	}

	matchLen := end - start
	var pathBuf [1024]uint32 // 4KiB
	path := pathBuf[:]
	if matchLen > len(path) {
		path = make([]uint32, matchLen)
	} else {
		path = path[:matchLen]
	}

	// 1. Identify winners at the END position.
	finalContext := ir.CalculateContext(b, end)
	lastPos := end - 1
	mask := masks[lastPos-start]
	ctx := ir.CalculateContext(b, lastPos)
	active := mask & bp.ContextMasks[ctx] & bp.CharMasks[b[lastPos]]

	var winners uint64
	for i := 0; i < 64; i++ {
		if (active & (1 << uint(i))) != 0 {
			if re.reachesBitWithContext(re.prog.Inst[i].Out, matchID, finalContext) {
				winners |= (1 << uint(i))
			}
		}
	}

	if winners == 0 {
		return
	}
	currBit := uint32(bits.TrailingZeros64(winners))
	path[matchLen-1] = currBit

	// 2. Trace backward to find predecessors.
	for i := matchLen - 2; i >= 0; i-- {
		pos := start + i
		mask := masks[i]
		byteVal := b[pos]
		target := path[i+1]

		found := false
		for k := 0; k < 64; k++ {
			if (mask & (1 << uint(k))) != 0 {
				afterCtx := ir.CalculateContext(b, pos+1)
				if re.reachesViaByteWithContext(uint32(k), target, byteVal, afterCtx) {
					currBit = uint32(k)
					path[i] = currBit
					found = true
					break
				}
			}
		}
		if !found {
			return
		}
	}

	// 3. Forward walk along the FIXED prioritized path.
	curr := uint32(re.prog.Start)
	for i := 0; i < len(path); i++ {
		re.applyPathTags(curr, path[i], start+i, regs, b)
		inst := re.prog.Inst[path[i]]
		curr = inst.Out
	}
	re.applyPathTags(curr, matchID, end, regs, b)
}

func (re *Regexp) reachesViaByteWithContext(pc, target uint32, byteVal byte, ctx syntax.EmptyOp) bool {
	inst := re.prog.Inst[pc]
	if inst.Op == syntax.InstRune || inst.Op == syntax.InstRune1 || inst.Op == syntax.InstRuneAny || inst.Op == syntax.InstRuneAnyNotNL {
		if inst.MatchRune(rune(byteVal)) {
			return re.reachesBitWithContext(inst.Out, target, ctx)
		}
	}
	return false
}

func (re *Regexp) applyPathTags(start, target uint32, pos int, regs []int, b []byte) bool {
	visited := uint64(0)
	ctx := ir.CalculateContext(b, pos)
	var dfs func(uint32) bool
	dfs = func(pc uint32) bool {
		if pc == target {
			inst := re.prog.Inst[pc]
			if inst.Op == syntax.InstCapture {
				if int(inst.Arg) < len(regs) {
					regs[inst.Arg] = pos
				}
			}
			return true
		}
		if (visited & (1 << pc)) != 0 {
			return false
		}
		visited |= (1 << pc)
		inst := re.prog.Inst[pc]
		switch inst.Op {
		case syntax.InstCapture:
			if re.reachesBitWithContext(inst.Out, target, ctx) {
				if int(inst.Arg) < len(regs) {
					regs[inst.Arg] = pos
				}
				return dfs(inst.Out)
			}
		case syntax.InstNop:
			if re.reachesBitWithContext(inst.Out, target, ctx) {
				return dfs(inst.Out)
			}
		case syntax.InstEmptyWidth:
			if (syntax.EmptyOp(inst.Arg) & ctx) == syntax.EmptyOp(inst.Arg) {
				if re.reachesBitWithContext(inst.Out, target, ctx) {
					return dfs(inst.Out)
				}
			}
		case syntax.InstAlt, syntax.InstAltMatch:
			if re.reachesBitWithContext(inst.Out, target, ctx) {
				return dfs(inst.Out)
			}
			if re.reachesBitWithContext(inst.Arg, target, ctx) {
				return dfs(inst.Arg)
			}
		}
		return false
	}
	return dfs(start)
}

func (re *Regexp) reachesBitWithContext(start, target uint32, ctx syntax.EmptyOp) bool {
	if start == target {
		return true
	}
	visited := uint64(0)
	var dfs func(uint32) bool
	dfs = func(pc uint32) bool {
		if pc == target {
			return true
		}
		if (visited & (1 << pc)) != 0 {
			return false
		}
		visited |= (1 << pc)
		inst := re.prog.Inst[pc]
		switch inst.Op {
		case syntax.InstAlt, syntax.InstAltMatch:
			return dfs(inst.Out) || dfs(inst.Arg)
		case syntax.InstCapture, syntax.InstNop:
			return dfs(inst.Out)
		case syntax.InstEmptyWidth:
			if (syntax.EmptyOp(inst.Arg) & ctx) == syntax.EmptyOp(inst.Arg) {
				return dfs(inst.Out)
			}
		}
		return false
	}
	return dfs(start)
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

type loopTrait interface{ HasAnchors() bool }
type fastMatchTrait struct{}

func (fastMatchTrait) HasAnchors() bool { return false }

type extendedMatchTrait struct{}

func (extendedMatchTrait) HasAnchors() bool { return true }
