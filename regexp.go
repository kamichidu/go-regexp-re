package regexp

import (
	"bytes"
	"context"
	"fmt"
	"math/bits"
	"sync"
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
	isASCII        bool
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
	subexpNames := s.CapNames()

	s = syntax.Simplify(s)
	prog, err := syntax.Compile(s)
	if err != nil {
		return nil, err
	}

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

	if isSimpleForBP(prog) {
		bpDfa = ir.NewBitParallelDFA(prog)
	}

	// Only build Table-DFA if BP-DFA is not available or if explicitly needed.
	if bpDfa == nil {
		dfa, err = ir.NewDFAWithMemoryLimit(ctx, prog, opt.MaxMemory)
		if err != nil {
			return nil, err
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

	isASCII := isSimpleForBP(prog)

	res := &Regexp{
		expr:           expr,
		numSubexp:      numSubexp,
		prefix:         []byte(prefixStr),
		prefixState:    prefixState,
		complete:       complete,
		anchorStart:    anchorStart,
		anchorEnd:      anchorEnd,
		isASCII:        isASCII,
		prog:           prog,
		dfa:            dfa,
		bpDfa:          bpDfa,
		literalMatcher: literalMatcher,
		subexpNames:    subexpNames,
	}
	res.bindMatchStrategy()
	return res, nil
}

func isSimpleForBP(prog *syntax.Prog) bool {
	if prog == nil {
		return false
	}
	if len(prog.Inst) > 62 {
		return false
	}

	hasGreedy := false
	hasNonGreedy := false

	for _, inst := range prog.Inst {
		switch inst.Op {
		case syntax.InstRune, syntax.InstRune1:
			for _, r := range inst.Rune {
				if r > 127 {
					return false
				}
			}
		case syntax.InstRuneAny, syntax.InstRuneAnyNotNL:
			return false
		case syntax.InstAlt, syntax.InstAltMatch:
			if inst.Arg < inst.Out {
				hasNonGreedy = true
			} else {
				hasGreedy = true
			}
		}
	}

	if hasGreedy && hasNonGreedy {
		return false
	}

	return true
}

func (re *Regexp) bindMatchStrategy() {
	if re.literalMatcher != nil {
		re.strategy = strategyLiteral
	} else if re.bpDfa != nil {
		re.strategy = strategyBitParallel
	} else if re.dfa != nil {
		if re.dfa.HasAnchors() {
			re.strategy = strategyExtended
		} else {
			re.strategy = strategyFast
		}
	}
}

func (re *Regexp) hasNonGreedy() bool {
	for _, inst := range re.prog.Inst {
		if (inst.Op == syntax.InstAlt || inst.Op == syntax.InstAltMatch) && inst.Arg < inst.Out {
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
		start, _, _, _ = bitParallelExecLoop(re, b, nil)
	case strategyFast:
		start, _, _ = matchExecLoop(fastMatchTrait{}, re, b)
	case strategyExtended:
		start, _, _ = matchExecLoop(extendedMatchTrait{}, re, b)
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

type matchContext struct {
	historyBuf   [1024]ir.StateID
	history      []ir.StateID
	bpHistoryBuf [1024]uint64
	bpHistory    []uint64
	stride       int
}

func (mc *matchContext) prepare(matchLen int, stride int) {
	mc.stride = stride
	if matchLen+1 > len(mc.historyBuf) {
		mc.history = make([]ir.StateID, matchLen+1)
	} else {
		mc.history = mc.historyBuf[:matchLen+1]
	}
	if matchLen+1 > len(mc.bpHistoryBuf) {
		mc.bpHistory = make([]uint64, matchLen+1)
	} else {
		mc.bpHistory = mc.bpHistoryBuf[:matchLen+1]
	}
}

var matchContextPool = sync.Pool{
	New: func() interface{} {
		return &matchContext{}
	},
}

func (re *Regexp) FindSubmatchIndex(b []byte) []int {
	numRegs := (re.numSubexp + 1) * 2
	regs := make([]int, numRegs)
	for i := range regs {
		regs[i] = -1
	}

	maskStride := 1
	if re.dfa != nil {
		maskStride = re.dfa.MaskStride()
	}

	if re.isASCII && len(b) <= 1023 {
		var mcStack matchContext
		mcStack.prepare(len(b), maskStride)
		return re.findSubmatchIndexInternal(b, &mcStack, regs)
	}

	mc := matchContextPool.Get().(*matchContext)
	defer matchContextPool.Put(mc)
	mc.prepare(len(b), maskStride)
	return re.findSubmatchIndexInternal(b, mc, regs)
}

func (re *Regexp) findSubmatchIndexInternal(b []byte, mc *matchContext, regs []int) []int {
	var start, end, prio int

	switch re.strategy {
	case strategyLiteral:
		return re.literalMatcher.FindSubmatchIndex(b)
	case strategyBitParallel:
		start, end, _, _ = bitParallelExecLoop(re, b, mc)
		if start < 0 {
			return nil
		}
		regs[0], regs[1] = start, end
		if re.numSubexp > 0 {
			re.bitParallelForwardRecap(b, mc, start, end, regs)
		}
		return regs
	case strategyExtended:
		start, end, prio = submatchExecLoop(extendedMatchTrait{}, re, b, mc)
	case strategyFast:
		start, end, prio = submatchExecLoop(fastMatchTrait{}, re, b, mc)
	default:
		start = -1
	}

	if start < 0 {
		return nil
	}

	regs[0], regs[1] = start, end
	if re.numSubexp == 0 {
		return regs
	}

	re.burnedRecap(mc, b, start, end, prio, regs)
	return regs
}

func (re *Regexp) burnedRecap(mc *matchContext, b []byte, start, end, bestPriority int, regs []int) {
	d := re.dfa
	if d == nil {
		return
	}

	trans := d.Transitions()
	tagUpdates := d.TagUpdates()
	tagUpdateIndices := d.TagUpdateIndices()

	// Initial path priority at the end of the match.
	p_end := int32(bestPriority % ir.SearchRestartPenalty)

	// Step 1: Find the priority path from start to end that leads to p_end.
	pathPrios := make([]int32, end-start+1)
	pathPrios[end-start] = p_end

	for i := end; i > start; {
		p_out := pathPrios[i-start]

		charStart := i - 1
		for charStart > start && (b[charStart]&0xC0) == 0x80 {
			charStart--
		}

		prevState := mc.history[charStart]
		byteVal := b[charStart]
		idx := (int(prevState) << 8) | int(byteVal)
		rawNext := trans[idx]
		stateAfterByte := rawNext & ir.StateIDMask

		// Multi-byte Warp Awareness:
		// If it's a warp transition, it covers multiple bytes.
		// If it's NOT a warp transition but still a multi-byte character,
		// the DFA handles it byte-by-byte, so charStart should just be i-1.
		if (rawNext & ir.WarpStateFlag) == 0 {
			charStart = i - 1
			byteVal = b[charStart]
			idx = (int(prevState) << 8) | int(byteVal)
			rawNext = trans[idx]
			stateAfterByte = rawNext & ir.StateIDMask
		}

		bestPIn := int32(1<<30 - 1)

		for p_in := int32(0); p_in < 1000; p_in++ {
			found := false
			if rawNext < 0 {
				update := tagUpdates[tagUpdateIndices[idx]]
				for _, pu := range update.PreUpdates {
					if pu.RelativePriority == p_in {
						tempMid := pu.NextPriority
						for _, pu_post := range update.PostUpdates {
							if pu_post.RelativePriority == tempMid {
								if re.canReachPriority(d, stateAfterByte, mc.history[i], ir.CalculateContext(b, i), pu_post.NextPriority, p_out) {
									found = true
									break
								}
							}
						}
						if !found && re.canReachPriority(d, stateAfterByte, mc.history[i], ir.CalculateContext(b, i), tempMid, p_out) {
							found = true
						}
						if found {
							break
						}
					}
				}
			} else {
				if re.canReachPriority(d, stateAfterByte, mc.history[i], ir.CalculateContext(b, i), p_in, p_out) {
					found = true
				}
			}

			if found {
				bestPIn = p_in
				break
			}
		}
		pathPrios[charStart-start] = bestPIn
		i = charStart
	}

	// Step 2: Apply tags along the found priority path.
	p := pathPrios[0]

	// StartUpdates
	for _, u := range d.StartUpdates() {
		if u.NextPriority == p {
			applyTags(u.Tags, start, regs)
			break
		}
	}

	// Initial anchors at start
	initialState := d.SearchState()
	if re.anchorStart {
		initialState = d.MatchState()
	}
	p = re.followPathAnchors(d, initialState, mc.history[start], ir.CalculateContext(b, start), start, regs, p)

	for i := start; i < end; {
		byteVal := b[i]
		prevState := mc.history[i]
		idx := (int(prevState) << 8) | int(byteVal)
		rawNext := trans[idx]
		stateAfterByte := rawNext & ir.StateIDMask

		// Determine the next character's start position
		nextI := i + 1
		if (rawNext & ir.WarpStateFlag) != 0 {
			skip := bits.LeadingZeros8(^byteVal) - 1
			if skip < 0 {
				skip = 0
			}
			nextI = i + 1 + skip
		}
		if nextI > end {
			nextI = end
		}

		p_next_target := pathPrios[nextI-start]

		if rawNext < 0 {
			update := tagUpdates[tagUpdateIndices[idx]]
			for _, pu_pre := range update.PreUpdates {
				if pu_pre.RelativePriority == p {
					p_mid := pu_pre.NextPriority
					found := false
					for _, pu_post := range update.PostUpdates {
						if pu_post.RelativePriority == p_mid {
							p_after_byte := pu_post.NextPriority
							if re.canReachPriority(d, stateAfterByte, mc.history[nextI], ir.CalculateContext(b, nextI), p_after_byte, p_next_target) {
								applyTags(pu_pre.Tags, i, regs)
								applyTags(pu_post.Tags, nextI, regs)
								p = re.followPathAnchors(d, stateAfterByte, mc.history[nextI], ir.CalculateContext(b, nextI), nextI, regs, p_after_byte)
								found = true
								break
							}
						}
					}
					if !found && re.canReachPriority(d, stateAfterByte, mc.history[nextI], ir.CalculateContext(b, nextI), p_mid, p_next_target) {
						applyTags(pu_pre.Tags, i, regs)
						p = re.followPathAnchors(d, stateAfterByte, mc.history[nextI], ir.CalculateContext(b, nextI), nextI, regs, p_mid)
						found = true
					}
					if found {
						break
					}
				}
			}
		} else {
			p = re.followPathAnchors(d, stateAfterByte, mc.history[nextI], ir.CalculateContext(b, nextI), nextI, regs, p)
		}
		i = nextI
	}

	// Final MatchTags
	for _, u := range d.MatchUpdates(mc.history[end]) {
		if u.RelativePriority == pathPrios[end-start] {
			applyTags(u.Tags, end, regs)
			break
		}
	}
}

func (re *Regexp) canReachPriority(d *ir.DFA, fromState, toState ir.StateID, context syntax.EmptyOp, p_in, p_out int32) bool {
	if fromState == toState {
		return p_in == p_out
	}
	tagUpdates := d.TagUpdates()
	anchorTagUpdateIndices := d.AnchorTagUpdateIndices()
	s := fromState
	p := p_in
	for iter := 0; iter < 6; iter++ {
		changed := false
		for bit := 0; bit < 6; bit++ {
			if (context & (1 << uint(bit))) != 0 {
				rawNext := d.AnchorNext(s, bit)
				if rawNext != ir.InvalidState {
					nextID := rawNext & ir.StateIDMask
					if nextID != s {
						if rawNext < 0 {
							update := tagUpdates[anchorTagUpdateIndices[int(s)*6+bit]]
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

func (re *Regexp) followPathAnchors(d *ir.DFA, fromState, toState ir.StateID, context syntax.EmptyOp, pos int, regs []int, p_in int32) int32 {
	if fromState == toState {
		return p_in
	}
	tagUpdates := d.TagUpdates()
	anchorTagUpdateIndices := d.AnchorTagUpdateIndices()
	s := fromState
	p := p_in
	for iter := 0; iter < 6; iter++ {
		changed := false
		for bit := 0; bit < 6; bit++ {
			if (context & (1 << uint(bit))) != 0 {
				rawNext := d.AnchorNext(s, bit)
				if rawNext != ir.InvalidState {
					nextID := rawNext & ir.StateIDMask
					if nextID != s {
						if rawNext < 0 {
							update := tagUpdates[anchorTagUpdateIndices[int(s)*6+bit]]
							for _, pu := range update.PreUpdates {
								if pu.RelativePriority == p {
									applyTags(pu.Tags, pos, regs)
									p = pu.NextPriority
									break
								}
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
	return p
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

func (re *Regexp) applyContextToState(d *ir.DFA, state ir.StateID, context syntax.EmptyOp, pos int, currentPrio *int, targetPrio int) ir.StateID {
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
							if currentPrio != nil {
								*currentPrio += int(update.BasePriority)
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

func submatchExecLoop[T loopTrait](trait T, re *Regexp, b []byte, mc *matchContext) (int, int, int) {
	d := re.dfa
	trans := d.Transitions()
	tagUpdateIndices := d.TagUpdateIndices()
	tagUpdates := d.TagUpdates()
	accepting := d.Accepting()
	numStates := d.NumStates()
	numBytes := len(b)
	anchorStart := re.anchorStart
	hasAnchors := trait.HasAnchors()
	usedAnchors := d.UsedAnchors()

	bestStart, bestEnd, bestPriority := -1, -1, 1<<30-1

	state := d.SearchState()
	if anchorStart {
		state = d.MatchState()
	}
	currentPriority := 0

	for i := 0; i <= numBytes; {
		if hasAnchors && ((usedAnchors&(syntax.EmptyWordBoundary|syntax.EmptyNoWordBoundary)) != 0 ||
			(i == 0 && (usedAnchors&(syntax.EmptyBeginText|syntax.EmptyBeginLine)) != 0) ||
			(i == numBytes && (usedAnchors&(syntax.EmptyEndText|syntax.EmptyEndLine)) != 0) ||
			((usedAnchors&syntax.EmptyBeginLine) != 0 && i > 0 && b[i-1] == '\n') ||
			((usedAnchors&syntax.EmptyEndLine) != 0 && i < numBytes && b[i] == '\n')) {
			state = re.applyContextToState(d, state, ir.CalculateContext(b, i), i, &currentPriority, bestPriority)
		}

		mc.history[i] = state
		sidx := int(state)

		if sidx >= 0 && sidx < len(accepting) && accepting[sidx] {
			p := currentPriority + d.MatchPriority(state)
			if p <= bestPriority {
				bestPriority, bestEnd = p, i
				bestStart = p / ir.SearchRestartPenalty
			}
			if d.IsBestMatch(state) || re.hasNonGreedy() {
				return bestStart, bestEnd, bestPriority
			}
		}

		if i < numBytes {
			byteVal := b[i]
			if sidx >= 0 && sidx < numStates {
				off := (sidx << 8) | int(byteVal)
				rawNext := trans[off]
				if rawNext != ir.InvalidState {
					state = rawNext & ir.StateIDMask
					if rawNext < 0 {
						update := tagUpdates[tagUpdateIndices[off]]
						currentPriority += int(update.BasePriority)
					}
					if byteVal < 0x80 || (rawNext&ir.WarpStateFlag) == 0 {
						i++
					} else {
						// Multi-byte Warp: Skip trailing bytes
						skip := bits.LeadingZeros8(^byteVal) - 1
						if skip < 0 {
							skip = 0
						}
						// Fill history for skipped bytes to aid burnedRecap
						for j := 1; j <= skip; j++ {
							if i+j < len(mc.history) {
								mc.history[i+j] = state
							}
						}
						i += 1 + skip
					}
					continue
				}
			}
			if anchorStart {
				break
			}
			i++
			currentPriority = i * ir.SearchRestartPenalty
			state = d.SearchState()
		} else {
			break
		}
	}
	return bestStart, bestEnd, bestPriority
}

func bitParallelExecLoop(re *Regexp, b []byte, mc *matchContext) (int, int, int, uint64) {
	bp := re.bpDfa
	numBytes := len(b)
	bestStart, bestEnd, bestPriority, bestMatchTags := -1, -1, 1<<30-1, uint64(0)

	charMasks := &bp.CharMasks
	table := &bp.SuccessorTable

	for i := 0; i <= numBytes; i++ {
		ctx := ir.CalculateContext(b, i)
		state := bp.StartMasks[ctx]

		for j := i; ; j++ {
			currContext := ir.CalculateContext(b, j)
			active := state & bp.ContextMasks[currContext]

			if mc != nil {
				mc.bpHistory[j] = active
			}

			if (active & bp.MatchMasks[currContext]) != 0 {
				prio := i * ir.SearchRestartPenalty
				if prio < bestPriority || (prio == bestPriority && j >= bestEnd) {
					bestPriority, bestEnd, bestStart, bestMatchTags = prio, j, i, bp.MatchMask
					if re.hasNonGreedy() {
						return bestStart, bestEnd, bestPriority, bestMatchTags
					}
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

func matchExecLoop[T loopTrait](trait T, re *Regexp, b []byte) (int, int, int) {
	d := re.dfa
	trans := d.Transitions()
	accepting := d.Accepting()
	numStates := d.NumStates()
	numBytes := len(b)
	anchorStart := re.anchorStart
	prefix := re.prefix
	hasAnchors := trait.HasAnchors()
	usedAnchors := d.UsedAnchors()

	bestStart, bestEnd, bestPriority := -1, -1, 1<<30-1
	currentPriority := 0

	state := d.SearchState()
	if anchorStart {
		state = d.MatchState()
	}

	for i := 0; i <= numBytes; {
		if hasAnchors && ((usedAnchors&(syntax.EmptyWordBoundary|syntax.EmptyNoWordBoundary)) != 0 ||
			(i == 0 && (usedAnchors&(syntax.EmptyBeginText|syntax.EmptyBeginLine)) != 0) ||
			(i == numBytes && (usedAnchors&(syntax.EmptyEndText|syntax.EmptyEndLine)) != 0) ||
			((usedAnchors&syntax.EmptyBeginLine) != 0 && i > 0 && b[i-1] == '\n') ||
			((usedAnchors&syntax.EmptyEndLine) != 0 && i < numBytes && b[i] == '\n')) {
			state = re.applyContextToState(d, state, ir.CalculateContext(b, i), i, &currentPriority, bestPriority)
		}

		sidx := int(state)
		if sidx >= 0 && sidx < len(accepting) && accepting[sidx] {
			p := currentPriority + d.MatchPriority(state)
			if p <= bestPriority {
				bestPriority, bestEnd = p, i
				bestStart = p / ir.SearchRestartPenalty
			}
			if d.IsBestMatch(state) || re.hasNonGreedy() {
				return bestStart, bestEnd, bestPriority
			}
		}

		if i < numBytes {
			byteVal := b[i]
			if sidx >= 0 && sidx < numStates {
				off := (sidx << 8) | int(byteVal)
				rawNext := trans[off]
				if rawNext != ir.InvalidState {
					state = rawNext & ir.StateIDMask
					if byteVal < 0x80 || (rawNext&ir.WarpStateFlag) == 0 {
						i++
					} else {
						// Multi-byte Warp: Skip trailing bytes
						skip := bits.LeadingZeros8(^byteVal) - 1
						if skip < 0 {
							skip = 0
						}
						i += 1 + skip
					}
					continue
				}
			}
			if anchorStart {
				break
			}
			if len(prefix) > 0 {
				skip := bytes.Index(b[i+1:], prefix)
				if skip >= 0 {
					i += skip + 1
					currentPriority = i * ir.SearchRestartPenalty
					state = re.prefixState
					continue
				}
			}
			i++
			currentPriority = i * ir.SearchRestartPenalty
			state = d.SearchState()
		} else {
			break
		}
	}
	return bestStart, bestEnd, bestPriority
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

func (re *Regexp) bitParallelForwardRecap(b []byte, mc *matchContext, start, end int, regs []int) {
	bp := re.bpDfa
	if bp == nil {
		return
	}

	// Step 1: Filter history backwards to ensure we only follow paths that reach the match at 'end'.
	ctxEnd := ir.CalculateContext(b, end)
	mc.bpHistory[end] &= bp.MatchMasks[ctxEnd]
	for i := end - 1; i >= start; i-- {
		// A state at i can reach match if it matches b[i] and reaches a state at i+1 that reached match.
		matchedAtI := mc.bpHistory[i] & bp.CharMasks[b[i]]

		var canReachNext uint64
		hNext := mc.bpHistory[i+1]
		for bit := 0; bit < 64; bit++ {
			if (hNext & (1 << uint(bit))) != 0 {
				canReachNext |= bp.ReverseSuccessors[bit]
			}
		}
		mc.bpHistory[i] = matchedAtI & canReachNext
	}

	// Step 2: Forward pass through the filtered history.
	for i := start; i <= end; i++ {
		winningBit := bits.TrailingZeros64(mc.bpHistory[i])
		if winningBit < 64 {
			preClosure := bp.PreEpsilonMasks[winningBit]
			for c := 0; c < len(regs); c++ {
				if (preClosure & bp.CaptureMasks[c]) != 0 {
					if c%2 == 0 { // Start tag
						if regs[c] == -1 {
							regs[c] = i
						}
					} else { // End tag
						regs[c] = i
					}
				}
			}
		}
	}
}
