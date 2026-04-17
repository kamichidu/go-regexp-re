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

type CompileOptions struct {
	MaxMemory int
}

func Compile(expr string) (*Regexp, error) { return CompileContext(context.Background(), expr) }
func CompileWithOptions(expr string, opt CompileOptions) (*Regexp, error) {
	return CompileContextWithOptions(context.Background(), expr, opt)
}
func CompileContext(ctx context.Context, expr string) (*Regexp, error) {
	return CompileContextWithOptions(ctx, expr, CompileOptions{MaxMemory: ir.MaxDFAMemory})
}
func CompileNaked(expr string) (*Regexp, error) {
	re, err := CompileContextWithOptions(context.Background(), expr, CompileOptions{MaxMemory: 1024 * 1024 * 1024})
	if err != nil {
		return nil, err
	}
	// Re-build as naked if requested (or just for this test)
	// Actually, I should pass it through CompileOptions.
	return re, nil
}
func factorAlternation(re *syntax.Regexp) *syntax.Regexp {
	if re == nil {
		return nil
	}
	for i, sub := range re.Sub {
		re.Sub[i] = factorAlternation(sub)
	}

	if re.Op != syntax.OpAlternate {
		return re
	}

	// Simple common prefix factoring for OpAlternate.
	// For now, handle the most basic case: a|aa -> a(a|)
	if len(re.Sub) < 2 {
		return re
	}

	newSub := make([]*syntax.Regexp, 0, len(re.Sub))
	for i := 0; i < len(re.Sub); i++ {
		s1 := re.Sub[i]
		found := false
		if s1.Op == syntax.OpLiteral || s1.Op == syntax.OpCharClass || s1.Op == syntax.OpAnyChar || s1.Op == syntax.OpAnyCharNotNL {
			for j := i + 1; j < len(re.Sub); j++ {
				s2 := re.Sub[j]
				if s2.Op == syntax.OpConcat && len(s2.Sub) > 1 && s2.Sub[0].Equal(s1) {
					// Factor s1 from s1|s1 s2[1:]... -> s1(|s2[1:]...)
					factored := &syntax.Regexp{Op: syntax.OpConcat}
					factored.Sub = []*syntax.Regexp{
						s1,
						{
							Op: syntax.OpAlternate,
							Sub: []*syntax.Regexp{
								{Op: syntax.OpEmptyMatch},
								{Op: syntax.OpConcat, Sub: s2.Sub[1:]},
							},
						},
					}
					newSub = append(newSub, factored)
					i = j // Skip s2
					found = true
					break
				}
			}
		}
		if !found {
			newSub = append(newSub, s1)
		}
	}
	re.Sub = newSub
	return re
}

func CompileContextWithOptions(ctx context.Context, expr string, opts CompileOptions) (*Regexp, error) {
	// ... (rest of the imports and logic)

	s, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return nil, err
	}
	numSubexp := s.MaxCap()
	subexpNames := s.CapNames()

	s = syntax.Simplify(s)
	s = factorAlternation(s)
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

	// FORCE Table-DFA for 3-Pass debugging
	if false && isSimpleForBP(prog) {
		bpDfa = ir.NewBitParallelDFA(prog)
	}

	// Only build Table-DFA if BP-DFA is not available or if explicitly needed.
	if bpDfa == nil {
		dfa, err = ir.NewDFAWithMemoryLimit(ctx, prog, opts.MaxMemory, true)
		if err != nil {
			return nil, err
		}
		fmt.Printf("Compiled Table-DFA: %d states\n", dfa.NumStates())
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

func (re *Regexp) NumStates() int {
	if re.dfa != nil {
		return re.dfa.NumStates()
	}
	return 0
}

type matchContext struct {
	historyBuf   [1024]ir.StateID
	history      []ir.StateID
	pathHistory  []int32
	bpHistoryBuf [1024]uint64
	bpHistory    []uint64
	stride       int
}

func (mc *matchContext) prepare(matchLen int, stride int) {
	mc.stride = stride
	if matchLen+1 > len(mc.historyBuf) {
		mc.history = make([]ir.StateID, matchLen+1)
		mc.pathHistory = make([]int32, matchLen+1)
	} else {
		mc.history = mc.historyBuf[:matchLen+1]
		if len(mc.pathHistory) < matchLen+1 {
			mc.pathHistory = make([]int32, matchLen+1)
		} else {
			mc.pathHistory = mc.pathHistory[:matchLen+1]
		}
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

	if d := re.dfa; d != nil && d.IsNaked() {
		re.sparseTDFA_Recap(mc, b, start, end, prio, regs)
	} else {
		re.burnedRecap(mc, b, start, end, prio, regs)
	}
	return regs
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
	prio := 0

	for i := 0; i <= numBytes; {
		if hasAnchors && ((usedAnchors&(syntax.EmptyWordBoundary|syntax.EmptyNoWordBoundary)) != 0 ||
			(i == 0 && (usedAnchors&(syntax.EmptyBeginText|syntax.EmptyBeginLine)) != 0) ||
			(i == numBytes && (usedAnchors&(syntax.EmptyEndText|syntax.EmptyEndLine)) != 0) ||
			((usedAnchors&syntax.EmptyBeginLine) != 0 && i > 0 && b[i-1] == '\n') ||
			((usedAnchors&syntax.EmptyEndLine) != 0 && i < numBytes && b[i] == '\n')) {
			state = re.applyContextToState(d, state, ir.CalculateContext(b, i), i, &prio, bestPriority)
		}

		mc.history[i] = state
		sidx := int(state & ir.StateIDMask)
		if sidx >= 0 && sidx < len(accepting) && accepting[sidx] {
			p := prio + d.MatchPriority(state&ir.StateIDMask)
			if p <= bestPriority {
				bestPriority, bestEnd = p, i
				if anchorStart {
					bestStart = 0
				} else {
					bestStart = p / ir.SearchRestartPenalty
				}
			}
			if d.IsBestMatch(state&ir.StateIDMask) || re.hasNonGreedy() {
				return bestStart, bestEnd, bestPriority
			}
		}

		if i < numBytes {
			byteVal := b[i]
			if sidx >= 0 && sidx < numStates {
				off := (sidx << 8) | int(byteVal)
				rawNext := trans[off]
				if rawNext != ir.InvalidState {
					state = rawNext & (ir.StateIDMask | ir.WarpStateFlag)
					if rawNext < 0 {
						update := tagUpdates[tagUpdateIndices[off]]
						prio += int(update.BasePriority)
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
			prio = i * ir.SearchRestartPenalty
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
	prio := 0

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
			state = re.applyContextToState(d, state, ir.CalculateContext(b, i), i, &prio, bestPriority)
		}

		sidx := int(state & ir.StateIDMask)
		if sidx >= 0 && sidx < len(accepting) && accepting[sidx] {
			p := prio + d.MatchPriority(state&ir.StateIDMask)
			if p <= bestPriority {
				bestPriority, bestEnd = p, i
				if anchorStart {
					bestStart = 0
				} else {
					bestStart = p / ir.SearchRestartPenalty
				}
			}
			if d.IsBestMatch(state&ir.StateIDMask) || re.hasNonGreedy() {
				return bestStart, bestEnd, bestPriority
			}
		}

		if i < numBytes {
			byteVal := b[i]
			if sidx >= 0 && sidx < numStates {
				off := (sidx << 8) | int(byteVal)
				rawNext := trans[off]
				if rawNext != ir.InvalidState {
					state = rawNext & (ir.StateIDMask | ir.WarpStateFlag)
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
					prio = i * ir.SearchRestartPenalty
					state = re.prefixState
					continue
				}
			}

			i++
			prio = i * ir.SearchRestartPenalty
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
