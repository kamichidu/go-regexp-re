package regexp

import (
	"context"
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
	complete       bool
	anchorStart    bool
	prog           *syntax.Prog
	dfa            *ir.DFA
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

func CompileContextWithOptions(ctx context.Context, expr string, opts CompileOptions) (*Regexp, error) {
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
	prefix, complete := calculateLiteralPrefix(s)

	anchorStart := false
	if s.Op == syntax.OpConcat && len(s.Sub) > 0 && s.Sub[0].Op == syntax.OpBeginText {
		anchorStart = true
	} else if s.Op == syntax.OpBeginText {
		anchorStart = true
	}

	var dfa *ir.DFA
	if literalMatcher == nil {
		dfa, err = ir.NewDFAWithMemoryLimit(ctx, prog, opts.MaxMemory, true)
		if err != nil {
			return nil, err
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
	}
	res.bindMatchStrategy()
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
		if len(re.Rune) == 2 && re.Rune[0] == re.Rune[1] {
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
	case syntax.OpBeginText:
		return "", false // Not a literal
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

	regs := make([]int, (re.numSubexp+1)*2)
	for i := range regs {
		regs[i] = -1
	}

	start, end, prio := submatchExecLoop(extendedMatchTrait{}, re, b, mc)
	if start < 0 {
		return nil
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
		trait := extendedMatchTrait{}
		if re.strategy == strategyFast {
			return matchExecLoop(fastMatchTrait{}, re, b)
		}
		if mc == nil {
			return matchExecLoop(trait, re, b)
		}
		return submatchExecLoop(trait, re, b, mc)
	}
	return -1, -1, 0
}

type loopTrait interface{ HasAnchors() bool }
type extendedMatchTrait struct{}

func (extendedMatchTrait) HasAnchors() bool { return true }

type fastMatchTrait struct{}

func (fastMatchTrait) HasAnchors() bool { return false }

func submatchExecLoop[T loopTrait](trait T, re *Regexp, b []byte, mc *matchContext) (int, int, int) {
	d := re.dfa
	trans, accepting, guards, numStates, numBytes := d.Transitions(), d.Accepting(), d.AcceptingGuards(), d.NumStates(), len(b)
	anchorStart := re.anchorStart
	bestStart, bestEnd, bestPriority := -1, -1, 1<<30-1
	state, prio := uint32(d.SearchState()), 0
	if anchorStart {
		state = uint32(d.MatchState())
	}

	for i := 0; i <= numBytes; {
		sidx := state & ir.StateIDMask
		mc.history[i] = sidx

		if int(sidx) < len(accepting) && accepting[sidx] {
			req := guards[sidx]
			if (ir.CalculateContext(b, i) & req) == req {
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
		if i < numBytes {
			byteVal := b[i]
			if int(sidx) < numStates {
				off := (int(sidx) << 8) | int(byteVal)
				rawNext := trans[off]
				if rawNext != ir.InvalidState {
					if (rawNext & ir.AnchorVerifyFlag) != 0 {
						req := syntax.EmptyOp((rawNext & ir.AnchorMask) >> 22)
						if (ir.CalculateContext(b, i) & req) != req {
							rawNext = ir.InvalidState
						}
					}
					if rawNext != ir.InvalidState {
						if (rawNext & ir.TaggedStateFlag) != 0 {
							uIndices, uUpdates := d.TagUpdateIndices(), d.TagUpdates()
							if off < len(uIndices) {
								uIdx := uIndices[off]
								if int(uIdx) < len(uUpdates) {
									prio += int(uUpdates[uIdx].BasePriority)
								}
							}
						}
						state = rawNext
						if byteVal < 0x80 || (rawNext&ir.WarpStateFlag) == 0 {
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
			state = uint32(d.SearchState())
		} else {
			break
		}
	}
	return bestStart, bestEnd, bestPriority
}

func matchExecLoop[T loopTrait](trait T, re *Regexp, b []byte) (int, int, int) {
	d := re.dfa
	trans, accepting, guards, numStates, numBytes := d.Transitions(), d.Accepting(), d.AcceptingGuards(), d.NumStates(), len(b)
	anchorStart := re.anchorStart
	bestStart, bestEnd, bestPriority := -1, -1, 1<<30-1
	state, prio := uint32(d.SearchState()), 0
	if anchorStart {
		state = uint32(d.MatchState())
	}

	for i := 0; i <= numBytes; {
		sidx := state & ir.StateIDMask
		if int(sidx) < len(accepting) && accepting[sidx] {
			req := guards[sidx]
			if (ir.CalculateContext(b, i) & req) == req {
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
		if i < numBytes {
			byteVal := b[i]
			if int(sidx) < numStates {
				off := (int(sidx) << 8) | int(byteVal)
				rawNext := trans[off]
				if rawNext != ir.InvalidState {
					if (rawNext & ir.AnchorVerifyFlag) != 0 {
						req := syntax.EmptyOp((rawNext & ir.AnchorMask) >> 22)
						if (ir.CalculateContext(b, i) & req) != req {
							rawNext = ir.InvalidState
						}
					}
					if rawNext != ir.InvalidState {
						if (rawNext & ir.TaggedStateFlag) != 0 {
							uIndices, uUpdates := d.TagUpdateIndices(), d.TagUpdates()
							if off < len(uIndices) {
								uIdx := uIndices[off]
								if int(uIdx) < len(uUpdates) {
									prio += int(uUpdates[uIdx].BasePriority)
								}
							}
						}
						state = rawNext
						if byteVal < 0x80 || (rawNext&ir.WarpStateFlag) == 0 {
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
			state = uint32(d.SearchState())
		} else {
			break
		}
	}
	return bestStart, bestEnd, bestPriority
}

func (re *Regexp) sparseTDFA_PathSelection(mc *matchContext, b []byte, start, end, prio int) {
	d := re.dfa
	recap := d.RecapTables()[0]

	// The winning path's relative priority at the end point.
	// We MUST use the MatchPriority of the final state to find which path reached InstMatch.
	currPrio := int16(d.MatchPriority(mc.history[end]))
	mc.pathHistory[end] = int32(currPrio)

	for i := end - 1; i >= start; i-- {
		sidx := mc.history[i]
		if sidx == ir.InvalidState {
			// This index was skipped by a SIMD warp, carry over the path identity.
			mc.pathHistory[i] = int32(currPrio)
			continue
		}
		byteVal := b[i]
		off := (int(sidx) << 8) | int(byteVal)

		found := false
		bestInputPrio := int16(32767)
		if off < len(recap.Transitions) {
			for _, entry := range recap.Transitions[off] {
				// We search backward: which InputPriority leads to the current NextPriority?
				// To ensure leftmost-first semantics, we must choose the smallest InputPriority
				// that connects to our current (winning) path identity.
				if int16(entry.NextPriority) == currPrio {
					if entry.InputPriority < bestInputPrio {
						bestInputPrio = entry.InputPriority
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
		if byteVal >= 0x80 && (rawNext&ir.WarpStateFlag) != 0 {
			step = 1 + ir.GetTrailingByteCount(byteVal)
		}

		if off < len(recap.Transitions) {
			// Step 2: Forward lick. Determine the next identity to select the unique edge.
			nextPathID := int32(0)
			if i+step <= end {
				nextPathID = mc.pathHistory[i+step]
			}

			for _, entry := range recap.Transitions[off] {
				if int32(entry.InputPriority) == pathID && int32(entry.NextPriority) == nextPathID {
					// Purely apply delta tags on the winning path identity.
					// PreTags belong to position i (before byte),
					// PostTags belong to position i+step (after byte).
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
	if n+1 > len(mc.historyBuf) {
		mc.history = make([]uint32, n+1)
		mc.pathHistory = make([]int32, n+1)
	} else {
		mc.history = mc.historyBuf[:n+1]
		mc.pathHistory = mc.pathHistoryBuf[:n+1]
	}
	for i := range mc.history {
		mc.history[i] = ir.InvalidState
	}
}

var matchContextPool = sync.Pool{New: func() any { return &matchContext{} }}
