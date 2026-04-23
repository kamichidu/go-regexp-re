package regexp

import (
	"bytes"
	"context"
	"sync"
	"unsafe"

	"github.com/kamichidu/go-regexp-re/internal/ir"
	"github.com/kamichidu/go-regexp-re/syntax"
)

// UnsupportedError represents a valid regular expression pattern that is not
// currently supported by the DFA engine due to structural limitations.
type UnsupportedError = syntax.UnsupportedError

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
	complete       bool
	anchorStart    bool
	prog           *syntax.Prog
	dfa            *ir.DFA
	literalMatcher *ir.LiteralMatcher
	subexpNames    []string
	strategy       matchStrategy
	searchState    uint32
	matchState     uint32
}

type CompileOptions struct {
	MaxMemory     int
	forceStrategy matchStrategy // Internal use for testing (strategyFast or strategyExtended)
}

func Compile(expr string) (*Regexp, error) { return CompileContext(context.Background(), expr) }
func CompileWithOptions(expr string, opt CompileOptions) (*Regexp, error) {
	return CompileContextWithOptions(context.Background(), expr, opt)
}
func CompileContext(ctx context.Context, expr string) (*Regexp, error) {
	return CompileContextWithOptions(ctx, expr, CompileOptions{MaxMemory: ir.MaxDFAMemory})
}

func CompileContextWithOptions(ctx context.Context, expr string, opts CompileOptions) (*Regexp, error) {
	s, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return nil, err
	}
	numSubexp := s.MaxCap()
	subexpNames := s.CapNames()

	s = syntax.Simplify(s)
	s = syntax.Optimize(s)
	prog, err := syntax.Compile(s)
	if err != nil {
		return nil, err
	}

	var literalMatcher *ir.LiteralMatcher
	if opts.forceStrategy == strategyNone {
		literalMatcher = ir.AnalyzeLiteralPattern(s, numSubexp+1)
	}
	prefix, complete := calculateLiteralPrefix(s)

	anchorStart := false
	if s.Op == syntax.OpConcat && len(s.Sub) > 0 && s.Sub[0].Op == syntax.OpBeginText {
		anchorStart = true
	} else if s.Op == syntax.OpBeginText {
		anchorStart = true
	}

	var dfa *ir.DFA
	var searchState, matchState uint32
	if literalMatcher == nil {
		dfa, err = ir.NewDFAWithMemoryLimit(ctx, s, prog, opts.MaxMemory, true)
		if err != nil {
			return nil, err
		}
		acc := dfa.Accepting()
		searchState = uint32(dfa.SearchState())
		if acc[searchState&ir.StateIDMask] {
			searchState |= ir.AcceptingStateFlag
		}
		matchState = uint32(dfa.MatchState())
		if acc[matchState&ir.StateIDMask] {
			matchState |= ir.AcceptingStateFlag
		}
	}

	res := &Regexp{
		expr:           expr,
		numSubexp:      numSubexp,
		prefix:         []byte(prefix),
		complete:       complete,
		anchorStart:    anchorStart,
		prog:           prog,
		dfa:            dfa,
		literalMatcher: literalMatcher,
		subexpNames:    subexpNames,
		searchState:    searchState,
		matchState:     matchState,
	}
	if opts.forceStrategy != strategyNone {
		res.strategy = opts.forceStrategy
	} else {
		res.bindMatchStrategy()
	}
	return res, nil
}

func calculateLiteralPrefix(re *syntax.Regexp) (string, bool) {
	switch re.Op {
	default:
		return "", false
	case syntax.OpLiteral:
		if re.Flags&syntax.FoldCase != 0 {
			return "", false
		}
		return string(re.Rune), true
	case syntax.OpCharClass:
		if (re.Flags&syntax.FoldCase == 0) && len(re.Rune) == 2 && re.Rune[0] == re.Rune[1] {
			return string(re.Rune[0]), true
		}
		return "", false
	case syntax.OpCapture:
		return calculateLiteralPrefix(re.Sub[0])
	case syntax.OpConcat:
		var prefix string
		for i, sub := range re.Sub {
			p, c := calculateLiteralPrefix(sub)
			prefix += p
			if !c {
				return prefix, false
			}
			if i == len(re.Sub)-1 {
				return prefix, true
			}
		}
		return prefix, true
	}
}

func (re *Regexp) bindMatchStrategy() {
	if re.literalMatcher != nil {
		re.strategy = strategyLiteral
		return
	}

	// Threshold for bit-parallel is 62 instructions (Mandate 2.8)
	if len(re.prog.Inst) <= 62 {
		re.strategy = strategyBitParallel
		return
	}

	if re.dfa != nil && re.dfa.HasAnchors() {
		re.strategy = strategyExtended
	} else {
		re.strategy = strategyFast
	}
}

func (re *Regexp) Match(b []byte) bool {
	start, _, _ := re.findSubmatchIndexInternal(b, nil, nil)
	return start >= 0
}

func (re *Regexp) MatchString(s string) bool {
	b := unsafe.Slice(unsafe.StringData(s), len(s))
	return re.Match(b)
}

func (re *Regexp) FindSubmatchIndex(b []byte) []int {
	if re.strategy == strategyLiteral {
		return re.literalMatcher.FindSubmatchIndex(b)
	}

	mc := matchContextPool.Get().(*matchContext)
	defer matchContextPool.Put(mc)
	mc.prepare(len(b))

	start, end, prio := re.submatch(b, mc)
	if start < 0 {
		return nil
	}

	regs := make([]int, (re.numSubexp+1)*2)
	for i := range regs {
		regs[i] = -1
	}

	regs[0], regs[1] = start, end
	if re.numSubexp > 0 {
		re.sparseTDFA_PathSelection(mc, b, start, end, prio)
		re.sparseTDFA_Recap(mc, b, start, end, prio, regs)
	}
	return regs
}

func (re *Regexp) findSubmatchIndexInternal(b []byte, mc *matchContext, regs []int) (int, int, int) {
	switch re.strategy {
	case strategyLiteral:
		res := re.literalMatcher.FindSubmatchIndex(b)
		if res == nil {
			return -1, -1, 0
		}
		return res[0], res[1], 0
	case strategyBitParallel, strategyFast, strategyExtended:
		if mc == nil {
			return re.match(b)
		}
		return re.submatch(b, mc)
	}
	return -1, -1, 0
}

func (re *Regexp) match(b []byte) (int, int, int) {
	if re.strategy == strategyExtended {
		return extendedMatchExecLoop(re, b)
	}
	return fastMatchExecLoop(re, b)
}

func fastMatchExecLoop(re *Regexp, b []byte) (int, int, int) {
	d := re.dfa
	trans := d.Transitions()
	guards := d.AcceptingGuards()
	numStates := d.NumStates()
	numBytes := len(b)
	searchState := re.searchState
	matchState := re.matchState

	anchorStart := re.anchorStart
	bestStart, bestEnd, bestPriority := -1, -1, 1<<60-1
	state, prio := searchState, 0
	if anchorStart {
		state = matchState
	}

	// BCE hint
	if len(trans) > 0 {
		_ = trans[len(trans)-1]
	}
	if len(guards) > 0 {
		_ = guards[len(guards)-1]
	}

	for i := 0; i <= numBytes; {
		sidx := state & ir.StateIDMask

		// SIMD Warp: skip to next prefix match if we are in search state and not anchored
		if !anchorStart && (state&ir.StateIDMask) == (searchState&ir.StateIDMask) && len(re.prefix) > 0 && i < numBytes {
			pos := bytes.Index(b[i:], re.prefix)
			if pos < 0 {
				break
			}
			if pos > 0 {
				i += pos
				prio = i * ir.SearchRestartPenalty
				continue
			}
		}

		if (state & ir.AcceptingStateFlag) != 0 {
			req := guards[sidx]
			if req == 0 || (ir.CalculateContext(b, i, req)&req) == req {
				p := prio + d.MatchPriority(sidx)
				if p <= bestPriority {
					bestPriority, bestEnd = p, i
					if anchorStart {
						bestStart = 0
					} else {
						bestStart = p / ir.SearchRestartPenalty
					}
					if d.IsBestMatch(sidx) {
						return bestStart, bestEnd, bestPriority
					}
				}
			}
		}

		if i >= numBytes {
			break
		}

		byteVal := b[i]
		if int(sidx) < numStates {
			off := (int(sidx) << 8) | int(byteVal)
			rawNext := trans[off]
			if rawNext != ir.InvalidState {
				if (rawNext & (ir.AnchorVerifyFlag | ir.TaggedStateFlag | ir.WarpStateFlag)) == 0 {
					state = rawNext
					i++
					continue
				}

				// Handle special flags
				if (rawNext & ir.AnchorVerifyFlag) != 0 {
					req := syntax.EmptyOp((rawNext & ir.AnchorMask) >> 22)
					if (ir.CalculateContext(b, i, req) & req) != req {
						rawNext = ir.InvalidState
					}
				}
				if rawNext != ir.InvalidState {
					state = rawNext
					if (byteVal < 0x80) || (rawNext&ir.WarpStateFlag) == 0 {
						i++
					} else {
						i += 1 + ir.GetTrailingByteCount(byteVal)
					}
					continue
				}
			}
		}
		if anchorStart {
			break
		}
		i++
		prio = i * ir.SearchRestartPenalty
		state = searchState
	}
	return bestStart, bestEnd, bestPriority
}

func extendedMatchExecLoop(re *Regexp, b []byte) (int, int, int) {
	d := re.dfa
	trans := d.Transitions()
	guards := d.AcceptingGuards()
	numStates := d.NumStates()
	numBytes := len(b)
	uIndices := d.TagUpdateIndices()
	uUpdates := d.TagUpdates()
	searchState := re.searchState
	matchState := re.matchState

	anchorStart := re.anchorStart
	bestStart, bestEnd, bestPriority := -1, -1, 1<<60-1
	state, prio := searchState, 0
	if anchorStart {
		state = matchState
	}

	// BCE hint
	if len(trans) > 0 {
		_ = trans[len(trans)-1]
	}
	if len(guards) > 0 {
		_ = guards[len(guards)-1]
	}

	for i := 0; i <= numBytes; {
		sidx := state & ir.StateIDMask

		// SIMD Warp: skip to next prefix match if we are in search state and not anchored
		if !anchorStart && (state&ir.StateIDMask) == (searchState&ir.StateIDMask) && len(re.prefix) > 0 && i < numBytes {
			pos := bytes.Index(b[i:], re.prefix)
			if pos < 0 {
				break
			}
			if pos > 0 {
				i += pos
				prio = i * ir.SearchRestartPenalty
				continue
			}
		}

		if (state & ir.AcceptingStateFlag) != 0 {
			req := guards[sidx]
			if req == 0 || (ir.CalculateContext(b, i, req)&req) == req {
				p := prio + d.MatchPriority(sidx)
				if p <= bestPriority {
					bestPriority, bestEnd = p, i
					if anchorStart {
						bestStart = 0
					} else {
						bestStart = p / ir.SearchRestartPenalty
					}
					if d.IsBestMatch(sidx) {
						return bestStart, bestEnd, bestPriority
					}
				}
			}
		}

		if i >= numBytes {
			break
		}

		byteVal := b[i]
		if int(sidx) < numStates {
			off := (int(sidx) << 8) | int(byteVal)
			rawNext := trans[off]
			if rawNext != ir.InvalidState {
				if (rawNext & (ir.AnchorVerifyFlag | ir.TaggedStateFlag | ir.WarpStateFlag)) == 0 {
					state = rawNext
					i++
					continue
				}

				// Handle special flags
				if (rawNext & ir.AnchorVerifyFlag) != 0 {
					req := syntax.EmptyOp((rawNext & ir.AnchorMask) >> 22)
					if (ir.CalculateContext(b, i, req) & req) != req {
						rawNext = ir.InvalidState
					}
				}
				if rawNext != ir.InvalidState {
					if (rawNext & ir.TaggedStateFlag) != 0 {
						if off < len(uIndices) {
							uIdx := uIndices[off]
							if int(uIdx) < len(uUpdates) {
								prio += int(uUpdates[uIdx].BasePriority)
							}
						}
					}
					state = rawNext
					if (byteVal < 0x80) || (rawNext&ir.WarpStateFlag) == 0 {
						i++
					} else {
						i += 1 + ir.GetTrailingByteCount(byteVal)
					}
					continue
				}
			}
		}
		if anchorStart {
			break
		}
		i++
		prio = i * ir.SearchRestartPenalty
		state = searchState
	}
	return bestStart, bestEnd, bestPriority
}

func (re *Regexp) submatch(b []byte, mc *matchContext) (int, int, int) {
	// Submatch always uses the extended loop because it needs to record history
	return extendedSubmatchExecLoop(re, b, mc)
}

func extendedSubmatchExecLoop(re *Regexp, b []byte, mc *matchContext) (int, int, int) {
	d := re.dfa
	trans := d.Transitions()
	guards := d.AcceptingGuards()
	numStates := d.NumStates()
	numBytes := len(b)
	uIndices := d.TagUpdateIndices()
	uUpdates := d.TagUpdates()
	searchState := re.searchState
	matchState := re.matchState

	anchorStart := re.anchorStart
	bestStart, bestEnd, bestPriority := -1, -1, 1<<60-1
	state, prio := searchState, 0
	if anchorStart {
		state = matchState
	}

	// BCE hint
	if len(trans) > 0 {
		_ = trans[len(trans)-1]
	}
	if len(guards) > 0 {
		_ = guards[len(guards)-1]
	}
	_ = mc.history[len(mc.history)-1]

	for i := 0; i <= numBytes; {
		sidx := state & ir.StateIDMask

		// SIMD Warp: skip to next prefix match if we are in search state and not anchored
		if !anchorStart && (state&ir.StateIDMask) == (searchState&ir.StateIDMask) && len(re.prefix) > 0 && i < numBytes {
			pos := bytes.Index(b[i:], re.prefix)
			if pos < 0 {
				break
			}
			if pos > 0 {
				for k := 0; k < pos; k++ {
					mc.history[i+k] = sidx
				}
				i += pos
				prio = i * ir.SearchRestartPenalty
				continue
			}
		}

		mc.history[i] = sidx
		if (state & ir.AcceptingStateFlag) != 0 {
			req := guards[sidx]
			if (ir.CalculateContext(b, i, req) & req) == req {
				p := prio + d.MatchPriority(sidx)
				if p <= bestPriority {
					bestPriority, bestEnd = p, i
					if anchorStart {
						bestStart = 0
					} else {
						bestStart = p / ir.SearchRestartPenalty
					}
					if d.IsBestMatch(sidx) {
						return bestStart, bestEnd, bestPriority
					}
				}
			}
		}

		if i >= numBytes {
			break
		}

		byteVal := b[i]
		if int(sidx) < numStates {
			off := (int(sidx) << 8) | int(byteVal)
			rawNext := trans[off]
			if rawNext != ir.InvalidState {
				if (rawNext & ir.AnchorVerifyFlag) != 0 {
					req := syntax.EmptyOp((rawNext & ir.AnchorMask) >> 22)
					if (ir.CalculateContext(b, i, req) & req) != req {
						rawNext = ir.InvalidState
					}
				}
				if rawNext != ir.InvalidState {
					if (rawNext & ir.TaggedStateFlag) != 0 {
						if off < len(uIndices) {
							uIdx := uIndices[off]
							if int(uIdx) < len(uUpdates) {
								prio += int(uUpdates[uIdx].BasePriority)
							}
						}
					}
					state = rawNext
					if (byteVal < 0x80) || (rawNext&ir.WarpStateFlag) == 0 {
						i++
					} else {
						step := 1 + ir.GetTrailingByteCount(byteVal)
						for k := 1; k < step; k++ {
							mc.history[i+k] = state & ir.StateIDMask
						}
						i += step
					}
					continue
				}
			}
		}
		if anchorStart {
			break
		}
		i++
		prio = i * ir.SearchRestartPenalty
		state = searchState
	}
	return bestStart, bestEnd, bestPriority
}

func (re *Regexp) sparseTDFA_PathSelection(mc *matchContext, b []byte, start, end, prio int) {
	d := re.dfa
	recap := d.RecapTables()[0]
	uIndices, uUpdates := d.TagUpdateIndices(), d.TagUpdates()

	// The winning path's relative priority at the end point.
	currPrio := int16(d.MatchPriority(mc.history[end]))
	mc.pathHistory[end] = int32(currPrio)

	for i := end - 1; i >= start; i-- {
		byteVal := b[i]
		sidx := mc.history[i]
		if sidx == ir.InvalidState {
			mc.pathHistory[i] = int32(currPrio)
			continue
		}
		off := (int(sidx) << 8) | int(byteVal)

		found := false
		bestInputPrio := int16(32767)
		if off < len(recap.Transitions) {
			basePrio := int16(0)
			if off < len(uIndices) {
				uIdx := uIndices[off]
				if int(uIdx) < len(uUpdates) {
					basePrio = int16(uUpdates[uIdx].BasePriority)
				}
			}

			for _, entry := range recap.Transitions[off] {
				if int16(entry.NextPriority) == currPrio {
					p := entry.InputPriority + basePrio
					if p < bestInputPrio {
						bestInputPrio = p
						found = true
					}
				}
			}
		}
		if found {
			currPrio = bestInputPrio
		} else {
			// Stay at current priority if no explicit transition is found (e.g. search restart)
		}
		mc.pathHistory[i] = int32(currPrio)
	}
}

func (re *Regexp) sparseTDFA_Recap(mc *matchContext, b []byte, start, end, prio int, regs []int) {
	d := re.dfa
	recap := d.RecapTables()[0]
	uIndices, uUpdates := d.TagUpdateIndices(), d.TagUpdates()

	// Apply initial tags for the winning path identity at start.
	re.applyEntryTags(regs, d.StartUpdates(), mc.pathHistory[start], start)

	for i := start; i < end; {
		sidx := mc.history[i]
		if sidx == ir.InvalidState {
			i++
			continue
		}
		pathID := mc.pathHistory[i]
		byteVal := b[i]
		off := (int(sidx) << 8) | int(byteVal)

		step := 1
		rawNext := d.Transitions()[off]
		if byteVal >= 0x80 && rawNext != ir.InvalidState && (rawNext&ir.WarpStateFlag) != 0 {
			step = 1 + ir.GetTrailingByteCount(byteVal)
		}

		if off < len(recap.Transitions) {
			basePrio := int16(0)
			if off < len(uIndices) {
				uIdx := uIndices[off]
				if int(uIdx) < len(uUpdates) {
					basePrio = int16(uUpdates[uIdx].BasePriority)
				}
			}

			// Step 2: Forward lick. Determine the next identity to select the unique edge.
			nextPathID := int32(0)
			if i+step <= end {
				nextPathID = mc.pathHistory[i+step]
			}

			// We need to find entry where InputPriority == pathID - basePrio AND NextPriority == nextPathID
			for _, entry := range recap.Transitions[off] {
				if entry.InputPriority == int16(pathID)-basePrio && int32(entry.NextPriority) == nextPathID {
					re.applyRawTags(regs, entry.PreTags, i)
					re.applyRawTags(regs, entry.PostTags, i+step)
					break
				}
			}
		}
		i += step
	}
}

func (re *Regexp) applyRawTags(regs []int, tags uint64, pos int) {
	if tags == 0 {
		return
	}
	for bit := 2; bit < 64; bit++ {
		if (tags & (1 << uint(bit))) != 0 {
			if bit < len(regs) {
				// Go capturing semantics on the winning path:
				// - Start tags (even bits: 2, 4, ...) are fixed once set (leftmost).
				// - End tags (odd bits: 3, 5, ...) are updated to the latest position.
				if (bit%2 != 0) || regs[bit] == -1 {
					regs[bit] = pos
				}
			}
		}
	}
}

func (re *Regexp) applyEntryTags(regs []int, updates []ir.PathTagUpdate, pathID int32, pos int) {
	// Standardize pathID for StartUpdates which are always calculated against Prio 0 or Restart Penalty
	matchID := pathID
	if pathID >= ir.SearchRestartPenalty {
		matchID = pathID % ir.SearchRestartPenalty
	}
	for _, u := range updates {
		if int32(u.NextPriority) == matchID {
			re.applyRawTags(regs, u.Tags, pos)
		}
	}
}

func (re *Regexp) FindStringSubmatchIndex(s string) []int {
	b := unsafe.Slice(unsafe.StringData(s), len(s))
	return re.FindSubmatchIndex(b)
}

func MustCompile(expr string) *Regexp {
	re, err := Compile(expr)
	if err != nil {
		panic(err)
	}
	return re
}

func (re *Regexp) String() string { return re.expr }

func (re *Regexp) LiteralPrefix() (prefix string, complete bool) {
	return string(re.prefix), re.complete
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

type matchContext struct {
	historyBuf     [1024]uint32
	history        []uint32
	pathHistoryBuf [1024]int32
	pathHistory    []int32
}

func (mc *matchContext) prepare(n int) {
	required := n + 1
	if required > len(mc.historyBuf) {
		if cap(mc.history) < required {
			mc.history = make([]uint32, required)
		} else {
			mc.history = mc.history[:required]
		}
		if cap(mc.pathHistory) < required {
			mc.pathHistory = make([]int32, required)
		} else {
			mc.pathHistory = mc.pathHistory[:required]
		}
	} else {
		mc.history = mc.historyBuf[:required]
		mc.pathHistory = mc.pathHistoryBuf[:required]
	}
}

var matchContextPool = sync.Pool{New: func() any { return &matchContext{} }}
