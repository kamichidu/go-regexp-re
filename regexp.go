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

	dfa, err = ir.NewDFAWithMemoryLimit(ctx, prog, opt.MaxMemory)
	if err != nil {
		return nil, err
	}
	if isSimpleForBP(prog) {
		bpDfa = ir.NewBitParallelDFA(prog)
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

	res := &Regexp{expr: expr, numSubexp: numSubexp, prefix: []byte(prefixStr), prefixState: prefixState, complete: complete, anchorStart: anchorStart, anchorEnd: anchorEnd, isASCII: isASCII, prog: prog, dfa: dfa, bpDfa: bpDfa, literalMatcher: literalMatcher, subexpNames: subexpNames}
	res.bindMatchStrategy()
	return res, nil
}

func isSimpleForBP(prog *syntax.Prog) bool {
	if len(prog.Inst) > 62 {
		return false
	}
	for _, inst := range prog.Inst {
		switch inst.Op {
		case syntax.InstAlt, syntax.InstAltMatch, syntax.InstEmptyWidth:
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
		start, _, _, _ = bitParallelExecLoop(re, b)
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

// matchContext manages pre-allocated buffers for submatch extraction.
type matchContext struct {
	historyBuf [1024]ir.StateID
	history    []ir.StateID
	stride     int
}

func (mc *matchContext) prepare(matchLen int, stride int) {
	mc.stride = stride
	if matchLen+1 > len(mc.historyBuf) {
		mc.history = make([]ir.StateID, matchLen+1)
	} else {
		mc.history = mc.historyBuf[:matchLen+1]
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

	if re.isASCII && len(b) <= 1023 {
		var mcStack matchContext
		mcStack.prepare(len(b), re.dfa.MaskStride())
		return re.findSubmatchIndexInternal(b, &mcStack, regs)
	}

	mc := matchContextPool.Get().(*matchContext)
	defer matchContextPool.Put(mc)
	mc.prepare(len(b), re.dfa.MaskStride())
	return re.findSubmatchIndexInternal(b, mc, regs)
}

func (re *Regexp) findSubmatchIndexInternal(b []byte, mc *matchContext, regs []int) []int {
	var start, end int

	// Check if start state is accepting (null match) and it's non-greedy.
	if re.hasNonGreedy() && re.dfa != nil {
		startState := re.dfa.SearchState()
		if re.anchorStart {
			startState = re.dfa.MatchState()
		}
		// Apply initial context
		startState = re.applyContextToState(re.dfa, startState, ir.CalculateContext(b, 0), 0, nil, 1<<30-1)
		if re.dfa.Accepting()[startState] && re.dfa.MatchPriority(startState) == 0 {
			regs[0], regs[1] = 0, 0
			// Use start updates
			for _, u := range re.dfa.StartUpdates() {
				applyTags(u.Tags, 0, regs)
			}
			return regs
		}
	}

	switch re.strategy {
	case strategyLiteral:
		return re.literalMatcher.FindSubmatchIndex(b)
	case strategyBitParallel:
		start, end, _, _ = bitParallelExecLoop(re, b)
		if start < 0 {
			return nil
		}
		regs[0], regs[1] = start, end
		if re.numSubexp == 0 {
			return regs
		}
		// Bit-parallel rescan is still needed for BP-DFA because it doesn't support TDFA yet
		re.bpRescanLoop(mc, b, start, end, regs)
		return regs
	case strategyExtended:
		start, end = submatchExecLoop(extendedMatchTrait{}, re, b, mc)
	case strategyFast:
		start, end = submatchExecLoop(fastMatchTrait{}, re, b, mc)
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

	re.forwardRecap(mc, b, start, end, regs)
	return regs
}

func (re *Regexp) forwardRecap(mc *matchContext, b []byte, start, end int, regs []int) {
	dfa := re.dfa
	trans := dfa.Transitions()
	tagUpdates := dfa.TagUpdates()
	tagUpdateIndices := dfa.TagUpdateIndices()
	history := mc.history

	// Initial tags
	for _, u := range dfa.StartUpdates() {
		applyTags(u.Tags, start, regs)
	}

	for i := 0; i < end-start; i++ {
		pos := start + i
		state := history[pos]
		byteVal := b[pos]

		idx := (int(state) << 8) | int(byteVal)
		rawNext := trans[idx]
		if rawNext < 0 {
			update := tagUpdates[tagUpdateIndices[idx]]
			for _, pu := range update.PreUpdates {
				applyTags(pu.Tags, pos, regs)
			}
			for _, pu := range update.PostUpdates {
				applyTags(pu.Tags, pos+1, regs)
			}
		}
	}
}

func (re *Regexp) bpRescanLoop(mc *matchContext, b []byte, start, end int, regs []int) {
	// This part still uses DASH-BT approach or similar for BP-DFA
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

func bitParallelExecLoop(re *Regexp, b []byte) (int, int, int, uint64) {
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

	if hasAnchors {
		state = re.applyContextToState(d, state, ir.CalculateContext(b, 0), 0, &currentPriority, bestPriority)
	}

	for i := 0; i <= numBytes; {
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
			if sidx >= 0 && sidx < numStates {
				off := (sidx << 8) | int(b[i])
				rawNext := trans[off]
				if rawNext != ir.InvalidState {
					state = rawNext & ir.StateIDMask
					if hasAnchors && ((usedAnchors&(syntax.EmptyWordBoundary|syntax.EmptyNoWordBoundary)) != 0 ||
						(i+1 == numBytes && (usedAnchors&(syntax.EmptyEndText|syntax.EmptyEndLine)) != 0) ||
						((usedAnchors&syntax.EmptyBeginLine) != 0 && b[i] == '\n') ||
						((usedAnchors&syntax.EmptyEndLine) != 0 && i+1 < numBytes && b[i+1] == '\n')) {
						state = re.applyContextToState(d, state, ir.CalculateContext(b, i+1), i+1, &currentPriority, bestPriority)
					}
					i++
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
					if hasAnchors {
						state = re.applyContextToState(d, state, ir.CalculateContext(b, i), i, &currentPriority, bestPriority)
					}
					continue
				}
			}
			i++
			currentPriority = i * ir.SearchRestartPenalty
			state = d.SearchState()
			if hasAnchors {
				state = re.applyContextToState(d, state, ir.CalculateContext(b, i), i, &currentPriority, bestPriority)
			}
		} else {
			break
		}
	}
	return bestStart, bestEnd, bestPriority
}

func submatchExecLoop[T loopTrait](trait T, re *Regexp, b []byte, mc *matchContext) (int, int) {
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

	if hasAnchors {
		state = re.applyContextToState(d, state, ir.CalculateContext(b, 0), 0, &currentPriority, bestPriority)
	}

	for i := 0; i <= numBytes; {
		mc.history[i] = state
		sidx := int(state)

		if sidx >= 0 && sidx < len(accepting) && accepting[sidx] {
			p := currentPriority + d.MatchPriority(state)
			if p <= bestPriority {
				bestPriority, bestEnd = p, i
				bestStart = p / ir.SearchRestartPenalty
			}
			if d.IsBestMatch(state) || re.hasNonGreedy() {
				return bestStart, bestEnd
			}
		}

		if hasAnchors && ((usedAnchors&(syntax.EmptyWordBoundary|syntax.EmptyNoWordBoundary)) != 0 ||
			(i == 0 && (usedAnchors&(syntax.EmptyBeginText|syntax.EmptyBeginLine)) != 0) ||
			(i == numBytes && (usedAnchors&(syntax.EmptyEndText|syntax.EmptyEndLine)) != 0) ||
			((usedAnchors&syntax.EmptyBeginLine) != 0 && i > 0 && b[i-1] == '\n') ||
			((usedAnchors&syntax.EmptyEndLine) != 0 && i < numBytes && b[i] == '\n')) {
			state = re.applyContextToState(d, state, ir.CalculateContext(b, i), i, &currentPriority, bestPriority)
			mc.history[i] = state
			sidx = int(state)
		}

		if sidx >= 0 && sidx < len(accepting) && accepting[sidx] {
			p := currentPriority + d.MatchPriority(state)
			if p <= bestPriority {
				bestPriority, bestEnd = p, i
				bestStart = p / ir.SearchRestartPenalty
			}
			if d.IsBestMatch(state) || re.hasNonGreedy() {
				return bestStart, bestEnd
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
					mc.history[i] = state
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
	return bestStart, bestEnd
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
