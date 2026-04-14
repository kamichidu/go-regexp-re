package regexp

import (
	"bytes"
	"context"
	"fmt"
	"math/bits"
	"unicode"
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

type CompileOption struct{ MaxMemory int }

func Compile(expr string) (*Regexp, error) { return CompileContext(context.Background(), expr) }
func CompileWithOption(expr string, opt CompileOption) (*Regexp, error) {
	return CompileContextWithOption(context.Background(), expr, opt)
}
func CompileContext(ctx context.Context, expr string) (*Regexp, error) {
	return CompileContextWithOption(ctx, expr, CompileOption{})
}

func CompileContextWithOption(ctx context.Context, expr string, opt CompileOption) (*Regexp, error) {
	if opt.MaxMemory <= 0 {
		opt.MaxMemory = ir.MaxDFAMemory
	}
	re, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return nil, err
	}
	numSubexp := re.MaxCap()
	subexpNames := make([]string, numSubexp+1)
	extractCapNames(re, subexpNames)
	anchorStart, anchorEnd := false, false
	if re.Op == syntax.OpConcat && len(re.Sub) > 0 {
		if re.Sub[0].Op == syntax.OpBeginText {
			anchorStart = true
		}
		if re.Sub[len(re.Sub)-1].Op == syntax.OpEndText {
			anchorEnd = true
		}
	} else if re.Op == syntax.OpBeginText {
		anchorStart = true
	} else if re.Op == syntax.OpEndText {
		anchorEnd = true
	}
	re = syntax.Simplify(re)
	re = syntax.Optimize(re)
	literalMatcher := ir.AnalyzeLiteralPattern(re, numSubexp+1)
	prefixStr, complete := syntax.Prefix(re)
	prog, err := syntax.Compile(re)
	if err != nil {
		return nil, err
	}

	var dfa *ir.DFA
	var bpDfa *ir.BitParallelDFA
	var prefixState ir.StateID = ir.InvalidState

	// ARCHITECTURAL SHORTCUT:
	// If literal matcher is possible, skip heavy DFA table construction.
	if literalMatcher != nil {
		res := &Regexp{
			expr:           expr,
			numSubexp:      numSubexp,
			subexpNames:    subexpNames,
			prefix:         []byte(prefixStr),
			complete:       complete,
			literalMatcher: literalMatcher,
			prog:           prog,
			strategy:       strategyLiteral,
		}
		return res, nil
	}
	// If Bit-parallel is possible, skip heavy DFA table construction.
	if isSimpleForBP(prog) {
		bpDfa = ir.NewBitParallelDFA(prog)
	}
	if bpDfa == nil {
		dfa, err = ir.NewDFAWithMemoryLimit(ctx, prog, opt.MaxMemory)
		if err != nil {
			return nil, err
		}
		prefixState = dfa.MatchState()
		if prefixStr != "" {
			trans := dfa.Transitions()
			for _, c := range []byte(prefixStr) {
				off := (int(prefixState) << 8) | int(c)
				rawNext := trans[off]
				if rawNext == ir.InvalidState {
					prefixState = ir.InvalidState
					break
				}
				prefixState = rawNext & ir.StateIDMask
			}
		}
	}

	res := &Regexp{expr: expr, numSubexp: numSubexp, prefix: []byte(prefixStr), prefixState: prefixState, complete: complete, anchorStart: anchorStart, anchorEnd: anchorEnd, prog: prog, dfa: dfa, bpDfa: bpDfa, subexpNames: subexpNames}
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
			// For now, any Alt takes the DFA path for priority safety.
			return false
		case syntax.InstRune, syntax.InstRune1, syntax.InstRuneAny, syntax.InstRuneAnyNotNL:
			// BP-DFA matches byte-by-byte. Multi-byte runes require DFA path.
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
	} else if re.bpDfa != nil {
		re.strategy = strategyBitParallel
	} else if re.dfa.HasAnchors() {
		re.strategy = strategyExtended
	} else {
		re.strategy = strategyFast
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

	// Hybrid Strategy:
	// If dfa is nil, we are in Bit-parallel path. For submatches, NFA is the only option.
	// Otherwise, use the principal DFA Rescan except for greedy cases.
	if re.dfa == nil || re.isGreedyMatch() {
		return re.nfaRescan(b, start, end, regs)
	}

	if re.dfa.HasAnchors() {
		rescanLoop[extendedMatchTrait](re, b, start, end, targetPriority, regs)
	} else {
		rescanLoop[fastMatchTrait](re, b, start, end, targetPriority, regs)
	}

	// Principal refinement: apply matchTags consistent with determined priority
	for i := 0; i < 64; i++ {
		if (matchTags&(1<<uint(i))) != 0 && i < len(regs) {
			if regs[i] == -1 || regs[i] > end {
				regs[i] = end
			}
		}
	}
	return regs
}

func (re *Regexp) nfaRescan(b []byte, start, end int, regs []int) []int {
	type thread struct {
		id   uint32
		regs []int
	}
	curr := make([]thread, 0, 16)
	next := make([]thread, 0, 16)

	addThread := func(q *[]thread, instID uint32, r []int, pos int) {
		visited := make(map[uint32]bool)
		var dfs func(id uint32, currentRegs []int)
		dfs = func(id uint32, currentRegs []int) {
			if visited[id] {
				return
			}
			visited[id] = true
			for _, th := range *q {
				if th.id == id {
					return
				}
			}
			inst := re.prog.Inst[id]
			switch inst.Op {
			case syntax.InstCapture:
				newRegs := make([]int, len(currentRegs))
				copy(newRegs, currentRegs)
				if int(inst.Arg) < len(newRegs) {
					newRegs[inst.Arg] = pos
				}
				dfs(inst.Out, newRegs)
			case syntax.InstNop:
				dfs(inst.Out, currentRegs)
			case syntax.InstAlt, syntax.InstAltMatch:
				dfs(inst.Out, currentRegs)
				dfs(inst.Arg, currentRegs)
			case syntax.InstEmptyWidth:
				if syntax.EmptyOp(inst.Arg)&ir.CalculateContext(b, pos) == syntax.EmptyOp(inst.Arg) {
					dfs(inst.Out, currentRegs)
				}
			default:
				*q = append(*q, thread{id, currentRegs})
			}
		}
		dfs(instID, r)
	}

	initialRegs := make([]int, len(regs))
	for i := range initialRegs {
		initialRegs[i] = -1
	}
	initialRegs[0] = start
	addThread(&curr, uint32(re.prog.Start), initialRegs, start)

	for i := start; i < end; i++ {
		next = next[:0]
		for _, th := range curr {
			inst := re.prog.Inst[th.id]
			match := false
			switch inst.Op {
			case syntax.InstRune, syntax.InstRune1:
				fold := (inst.Arg&uint32(syntax.FoldCase) != 0)
				r := rune(b[i])
				if inst.Op == syntax.InstRune1 {
					r0 := inst.Rune[0]
					if fold {
						match = (r == r0 || r == unicode.SimpleFold(r0))
					} else {
						match = (r == r0)
					}
				} else {
					for j := 0; j < len(inst.Rune); j += 2 {
						if r >= inst.Rune[j] && r <= inst.Rune[j+1] {
							match = true
							break
						}
					}
				}
			case syntax.InstRuneAny:
				match = true
			case syntax.InstRuneAnyNotNL:
				match = b[i] != '\n'
			}
			if match {
				addThread(&next, inst.Out, th.regs, i+1)
			}
		}
		curr, next = next, curr
	}

	next = next[:0]
	for _, th := range curr {
		addThread(&next, th.id, th.regs, end)
	}

	for _, th := range next {
		if re.prog.Inst[th.id].Op == syntax.InstMatch {
			copy(regs, th.regs)
			regs[1] = end
			return regs
		}
	}
	return regs
}

func rescanLoop[T loopTrait](re *Regexp, b []byte, start, end, targetPriority int, regs []int) {
	var trait T
	dfa := re.dfa
	trans, tagUpdateIndices, tagUpdates, matchState := dfa.Transitions(), dfa.TagUpdateIndices(), dfa.TagUpdates(), dfa.MatchState()

	innerTarget := targetPriority % ir.SearchRestartPenalty
	for _, u := range dfa.StartUpdates() {
		// Principle: allow tags consistent with best priority
		if int(u.RelativePriority) <= innerTarget {
			applyTags(u.Tags, start, regs)
		}
	}

	currentPrio := 0
	state := matchState
	hasAnchors := trait.HasAnchors()

	for i := start; i <= end; i++ {
		if hasAnchors {
			state = re.applyContextToState(dfa, state, ir.CalculateContext(b, i), i, &currentPrio, innerTarget, regs)
		}

		if i == end {
			break
		}

		idx := (int(state) << 8) | int(b[i])
		rawNext := trans[idx]
		if rawNext != ir.InvalidState {
			if rawNext < 0 {
				update := tagUpdates[tagUpdateIndices[idx]]
				nextPrio := currentPrio + int(update.BasePriority)
				for _, tu := range update.PreUpdates {
					if nextPrio+int(tu.RelativePriority) <= innerTarget {
						applyTags(tu.Tags, i, regs)
					}
				}
				for _, tu := range update.PostUpdates {
					if nextPrio+int(tu.RelativePriority) <= innerTarget {
						applyTags(tu.Tags, i+1, regs)
					}
				}
				currentPrio = nextPrio
			}
			state = rawNext & ir.StateIDMask
		} else {
			break
		}
	}
}

func applyTags(t uint64, pos int, regs []int) {
	for t != 0 {
		i := bits.TrailingZeros64(t)
		if i < len(regs) {
			regs[i] = pos
		}
		t &= ^(uint64(1) << i)
	}
}

func bitParallelExecLoop(re *Regexp, b []byte) (int, int, int, uint64) {
	bp := re.bpDfa
	numBytes := len(b)
	bestStart, bestEnd, bestPriority, bestMatchTags := -1, -1, 1<<30-1, uint64(0)
	matchMask := bp.MatchMask
	charMasks := &bp.CharMasks
	table := &bp.SuccessorTable

	for i := 0; i <= numBytes; i++ {
		state := bp.StartMask
		currContext := ir.CalculateContext(b, i)

		// 1. Initial Match Check (Empty string at start i)
		if (matchMask & (1 << 63)) != 0 {
			prio := i*ir.SearchRestartPenalty + 63
			if prio < bestPriority || (prio == bestPriority && i >= bestEnd) {
				bestPriority, bestEnd, bestStart, bestMatchTags = prio, i, i, (1 << 63)
			}
		}

		for j := i; ; j++ {
			// 2. Apply anchors (instant transitions)
			for k := 0; k < 8; k++ {
				active := state & bp.ContextMasks[currContext]
				if active == 0 {
					break
				}

				// MATCH check for anchors
				if (active & matchMask) != 0 {
					activeMatch := active & matchMask
					winningBit := bits.TrailingZeros64(activeMatch)
					prio := i*ir.SearchRestartPenalty + winningBit
					if prio < bestPriority || (prio == bestPriority && j >= bestEnd) {
						bestPriority, bestEnd, bestStart, bestMatchTags = prio, j, i, (1 << uint(winningBit))
					}
				}

				state = table[0][active&0xff] |
					table[1][(active>>8)&0xff] |
					table[2][(active>>16)&0xff] |
					table[3][(active>>24)&0xff] |
					table[4][(active>>32)&0xff] |
					table[5][(active>>40)&0xff] |
					table[6][(active>>48)&0xff] |
					table[7][(active>>56)&0xff] | (state & ^active)
			}

			if j == numBytes {
				break
			}

			// 3. Character transition
			active := state & charMasks[b[j]]
			if active == 0 {
				break
			}

			// MATCH check for characters
			if (active & matchMask) != 0 {
				activeMatch := active & matchMask
				winningBit := bits.TrailingZeros64(activeMatch)
				prio := i*ir.SearchRestartPenalty + winningBit
				if prio < bestPriority || (prio == bestPriority && j+1 >= bestEnd) {
					bestPriority, bestEnd, bestStart, bestMatchTags = prio, j+1, i, (1 << uint(winningBit))
				}
			}

			state = table[0][active&0xff] |
				table[1][(active>>8)&0xff] |
				table[2][(active>>16)&0xff] |
				table[3][(active>>24)&0xff] |
				table[4][(active>>32)&0xff] |
				table[5][(active>>40)&0xff] |
				table[6][(active>>48)&0xff] |
				table[7][(active>>56)&0xff]

			currContext = ir.CalculateContext(b, j+1)
		}

		if bestStart != -1 {
			return bestStart, bestEnd, bestPriority, bestMatchTags
		}
		if re.anchorStart {
			break
		}

		// Prefix skip optimization
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
							nextPrio := *currentPrio + int(update.BasePriority)
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
							*currentPrio = nextPrio
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

func MustCompile(expr string) *Regexp {
	re, err := Compile(expr)
	if err != nil {
		panic(`regexp: Compile(` + quote(expr) + `): ` + err.Error())
	}
	return re
}
func (re *Regexp) String() string { return re.expr }

type loopTrait interface {
	HasAnchors() bool
	IsCapture() bool
}
type fastMatchTrait struct{}

func (fastMatchTrait) HasAnchors() bool { return false }
func (fastMatchTrait) IsCapture() bool  { return false }

type fastCaptureTrait struct{}

func (fastCaptureTrait) HasAnchors() bool { return false }
func (fastCaptureTrait) IsCapture() bool  { return true }

type extendedMatchTrait struct{}

func (extendedMatchTrait) HasAnchors() bool { return true }
func (extendedMatchTrait) IsCapture() bool  { return false }

type extendedCaptureTrait struct{}

func (extendedCaptureTrait) HasAnchors() bool { return true }
func (extendedCaptureTrait) IsCapture() bool  { return true }

func fastExecLoop(re *Regexp, b []byte) (int, int, int, uint64) {
	d := re.dfa
	trans := d.Transitions()
	tagUpdateIndices := d.TagUpdateIndices()
	tagUpdates := d.TagUpdates()
	accepting := d.Accepting()
	numStates := d.NumStates()
	numBytes := len(b)

	// Regexp fields
	anchorStart := re.anchorStart
	prefix := re.prefix
	prefixState := re.prefixState

	// BCE hints
	if len(trans) == 0 {
		return -1, -1, -1, 0
	}
	_ = trans[len(trans)-1]
	if len(accepting) > 0 {
		_ = accepting[len(accepting)-1]
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

		// Restart search or return best match
		if bestStart != -1 {
			return bestStart, bestEnd, bestPriority, bestMatchTags
		}
		if anchorStart {
			break
		}

		// Move to next starting position
		currentPriority = (currentPriority/ir.SearchRestartPenalty + 1) * ir.SearchRestartPenalty
		i = currentPriority / ir.SearchRestartPenalty
		if i > numBytes {
			break
		}

		// Warp Point / Prefix skip optimization
		state = d.SearchState()
		warpByte := d.WarpPoint(state)
		if warpByte >= 0 {
			skip := bytes.Index(b[i:], []byte{byte(warpByte)})
			if skip < 0 {
				break
			}
			i += skip
			currentPriority = (i * ir.SearchRestartPenalty)
			// Move to target state
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

	// Regexp fields
	anchorStart := re.anchorStart
	prefix := re.prefix
	prefixState := re.prefixState

	// BCE hints
	if len(trans) == 0 {
		return -1, -1, -1, 0
	}
	_ = trans[len(trans)-1]
	if len(accepting) > 0 {
		_ = accepting[len(accepting)-1]
	}

	bestStart, bestEnd, bestPriority, bestMatchTags, currentPriority := -1, -1, 1<<30-1, uint64(0), 0
	state := d.SearchState()
	if anchorStart {
		state = d.MatchState()
	}
	usedAnchors := d.UsedAnchors()

	for i := 0; i <= numBytes; {
		// OPTIMIZATION: Only calculate context and apply if used anchors are present.
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

		// Restart search or return best match
		if bestStart != -1 {
			return bestStart, bestEnd, bestPriority, bestMatchTags
		}
		if anchorStart {
			break
		}

		// Move to next starting position
		currentPriority = (currentPriority/ir.SearchRestartPenalty + 1) * ir.SearchRestartPenalty
		i = currentPriority / ir.SearchRestartPenalty
		if i > numBytes {
			break
		}

		// Warp Point / Prefix skip optimization
		state = d.SearchState()
		warpByte := d.WarpPoint(state)
		if warpByte >= 0 {
			skip := bytes.Index(b[i:], []byte{byte(warpByte)})
			if skip < 0 {
				break
			}

			// Guarded warp verification
			guard := d.WarpPointGuard(state)
			if guard != 0 {
				if ir.CalculateContext(b, i+skip)&guard == 0 {
					// Guard failed, skip this byte and continue search
					i += skip + 1
					currentPriority = (i * ir.SearchRestartPenalty)
					continue
				}
			}

			i += skip
			currentPriority = (i * ir.SearchRestartPenalty)
			// Move to target state
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
