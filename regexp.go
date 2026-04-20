package regexp

import (
	"context"
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

	anchorStart := false
	if s.Op == syntax.OpConcat && len(s.Sub) > 0 && s.Sub[0].Op == syntax.OpBeginText {
		anchorStart = true
	} else if s.Op == syntax.OpBeginText {
		anchorStart = true
	}

	dfa, err := ir.NewDFAWithMemoryLimit(ctx, prog, opts.MaxMemory, true)
	if err != nil {
		return nil, err
	}

	res := &Regexp{
		expr:           expr,
		numSubexp:      numSubexp,
		anchorStart:    anchorStart,
		prog:           prog,
		dfa:            dfa,
		literalMatcher: literalMatcher,
		subexpNames:    subexpNames,
	}
	res.bindMatchStrategy()
	return res, nil
}

func (re *Regexp) bindMatchStrategy() {
	if re.literalMatcher != nil {
		re.strategy = strategyLiteral
	} else {
		re.strategy = strategyExtended
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
	mc := matchContextPool.Get().(*matchContext)
	defer matchContextPool.Put(mc)
	mc.prepare(len(b))
	regs := make([]int, (re.numSubexp+1)*2)
	for i := range regs {
		regs[i] = -1
	}
	start, end, prio := re.findSubmatchIndexInternal(b, mc, regs)
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
	case strategyExtended:
		if mc == nil {
			return matchExecLoop(extendedMatchTrait{}, re, b)
		}
		return submatchExecLoop(extendedMatchTrait{}, re, b, mc)
	}
	return -1, -1, 0
}

type loopTrait interface{ HasAnchors() bool }
type extendedMatchTrait struct{}

func (extendedMatchTrait) HasAnchors() bool { return true }

func submatchExecLoop[T loopTrait](trait T, re *Regexp, b []byte, mc *matchContext) (int, int, int) {
	d := re.dfa
	trans, accepting, guards, numStates, numBytes := d.Transitions(), d.Accepting(), d.AcceptingGuards(), d.NumStates(), len(b)
	anchorStart := re.anchorStart
	bestStart, bestEnd, bestPriority := -1, -1, 1<<30-1
	state, prio := d.SearchState(), 0
	if anchorStart {
		state = d.MatchState()
	}

	for i := 0; i <= numBytes; {
		mc.history[i] = state
		sidx := int(state & ir.StateIDMask)
		if sidx >= 0 && sidx < len(accepting) && accepting[sidx] {
			req := guards[sidx]
			if (ir.CalculateContext(b, i) & req) == req {
				p := prio + d.MatchPriority(state)
				if p <= bestPriority {
					bestPriority, bestEnd = p, i
					if anchorStart {
						bestStart = 0
					} else {
						bestStart = p / ir.SearchRestartPenalty
					}
					if d.IsBestMatch(state) {
						return bestStart, bestEnd, bestPriority
					}
				}
			}
		}
		if i < numBytes {
			byteVal := b[i]
			if sidx >= 0 && sidx < numStates {
				off := (sidx << 8) | int(byteVal)
				rawNext := trans[off]
				if rawNext != ir.InvalidState {
					if (rawNext & ir.AnchorVerifyFlag) != 0 {
						req := syntax.EmptyOp((rawNext & ir.AnchorMask) >> 24)
						if (ir.CalculateContext(b, i) & req) != req {
							rawNext = ir.InvalidState
						}
					}
					if rawNext != ir.InvalidState {
						state = rawNext & (ir.StateIDMask | ir.WarpStateFlag)
						if rawNext < 0 {
							uIndices, uUpdates := d.TagUpdateIndices(), d.TagUpdates()
							if off < len(uIndices) {
								uIdx := uIndices[off]
								if int(uIdx) < len(uUpdates) {
									prio += int(uUpdates[uIdx].BasePriority)
								}
							}
						}
						if byteVal < 0x80 || (rawNext&ir.WarpStateFlag) == 0 {
							i++
						} else {
							i += 1 + (bits.LeadingZeros8(^byteVal) - 1)
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
			state = d.SearchState()
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
	state, prio := d.SearchState(), 0
	if anchorStart {
		state = d.MatchState()
	}

	for i := 0; i <= numBytes; {
		sidx := int(state & ir.StateIDMask)
		if sidx >= 0 && sidx < len(accepting) && accepting[sidx] {
			req := guards[sidx]
			if (ir.CalculateContext(b, i) & req) == req {
				p := prio + d.MatchPriority(state)
				if p <= bestPriority {
					bestPriority, bestEnd = p, i
					if anchorStart {
						bestStart = 0
					} else {
						bestStart = p / ir.SearchRestartPenalty
					}
					if d.IsBestMatch(state) {
						return bestStart, bestEnd, bestPriority
					}
				}
			}
		}
		if i < numBytes {
			byteVal := b[i]
			if sidx >= 0 && sidx < numStates {
				off := (sidx << 8) | int(byteVal)
				rawNext := trans[off]
				if rawNext != ir.InvalidState {
					if (rawNext & ir.AnchorVerifyFlag) != 0 {
						req := syntax.EmptyOp((rawNext & ir.AnchorMask) >> 24)
						if (ir.CalculateContext(b, i) & req) != req {
							rawNext = ir.InvalidState
						}
					}
					if rawNext != ir.InvalidState {
						state = rawNext & (ir.StateIDMask | ir.WarpStateFlag)
						if rawNext < 0 {
							uIndices, uUpdates := d.TagUpdateIndices(), d.TagUpdates()
							if off < len(uIndices) {
								uIdx := uIndices[off]
								if int(uIdx) < len(uUpdates) {
									prio += int(uUpdates[uIdx].BasePriority)
								}
							}
						}
						if byteVal < 0x80 || (rawNext&ir.WarpStateFlag) == 0 {
							i++
						} else {
							i += 1 + (bits.LeadingZeros8(^byteVal) - 1)
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
			state = d.SearchState()
		} else {
			break
		}
	}
	return bestStart, bestEnd, bestPriority
}

// Pass 2: Identifies the unique "winning NFA path" from start to end.
func (re *Regexp) sparseTDFA_PathSelection(mc *matchContext, b []byte, start, end, prio int) {
	d := re.dfa
	recap := d.RecapTables()[0]
	currPrio := int16(0) // Start priority for winning path at start position
	for i := start; i < end; {
		state := mc.history[i]
		sidx := int(state & ir.StateIDMask)
		byteVal := b[i]
		off := (sidx << 8) | int(byteVal)
		mc.pathHistory[i] = int32(currPrio)

		entries := recap.Transitions[off]
		found := false
		for _, entry := range entries {
			if entry.InputPriority == currPrio {
				currPrio = entry.NextPriority
				found = true
				break
			}
		}
		if !found {
			break
		}

		rawNext := d.Transitions()[off]
		if byteVal < 0x80 || (rawNext&ir.WarpStateFlag) == 0 {
			i++
		} else {
			i += 1 + ir.GetTrailingByteCount(byteVal)
		}
	}
	mc.pathHistory[end] = int32(currPrio)
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
	return string(re.prefix), false
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
	historyBuf     [1024]ir.StateID
	history        []ir.StateID
	pathHistoryBuf [1024]int32
	pathHistory    []int32
}

func (mc *matchContext) prepare(n int) {
	if n+1 > len(mc.historyBuf) {
		mc.history = make([]ir.StateID, n+1)
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
