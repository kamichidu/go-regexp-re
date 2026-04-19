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
	} else if re.bpDfa != nil {
		re.strategy = strategyBitParallel
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
	case strategyExtended, strategyFast:
		if mc == nil {
			return matchExecLoop(extendedMatchTrait{}, re, b)
		}
		return submatchExecLoop(extendedMatchTrait{}, re, b, mc)
	}
	return -1, -1, 0
}

type loopTrait interface{ HasAnchors() bool }
type fastMatchTrait struct{}

func (fastMatchTrait) HasAnchors() bool { return false }

type extendedMatchTrait struct{}

func (extendedMatchTrait) HasAnchors() bool { return true }

func submatchExecLoop[T loopTrait](trait T, re *Regexp, b []byte, mc *matchContext) (int, int, int) {
	d := re.dfa
	trans, accepting, numStates, numBytes := d.Transitions(), d.Accepting(), d.NumStates(), len(b)
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
		if i < numBytes {
			byteVal := b[i]
			if sidx >= 0 && sidx < numStates {
				off := (sidx << 8) | int(byteVal)
				rawNext := trans[off]
				if rawNext != ir.InvalidState && (rawNext&ir.AnchorVerifyFlag) != 0 {
					req := syntax.EmptyOp((rawNext & ir.AnchorMask) >> 24)
					if ir.CalculateContext(b, i)&req != req {
						rawNext = ir.InvalidState
					}
				}
				if rawNext != ir.InvalidState {
					state = rawNext & (ir.StateIDMask | ir.WarpStateFlag)
					if byteVal < 0x80 || (rawNext&ir.WarpStateFlag) == 0 {
						i++
					} else {
						skip := bits.LeadingZeros8(^byteVal) - 1
						if skip < 0 {
							skip = 0
						}
						for j := 1; j <= skip; j++ {
							if i+j < len(mc.history) {
								mc.history[i+j] = ir.StateID(sidx)
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

func matchExecLoop[T loopTrait](trait T, re *Regexp, b []byte) (int, int, int) {
	d := re.dfa
	trans, accepting, numStates, numBytes := d.Transitions(), d.Accepting(), d.NumStates(), len(b)
	anchorStart := re.anchorStart
	bestStart, bestEnd, bestPriority := -1, -1, 1<<30-1
	state, prio := d.SearchState(), 0
	if anchorStart {
		state = d.MatchState()
	}

	for i := 0; i <= numBytes; {
		sidx := int(state & ir.StateIDMask)
		if sidx >= 0 && sidx < len(accepting) && accepting[sidx] {
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
		if i < numBytes {
			byteVal := b[i]
			if sidx >= 0 && sidx < numStates {
				off := (sidx << 8) | int(byteVal)
				rawNext := trans[off]
				if rawNext != ir.InvalidState && (rawNext&ir.AnchorVerifyFlag) != 0 {
					req := syntax.EmptyOp((rawNext & ir.AnchorMask) >> 24)
					if ir.CalculateContext(b, i)&req != req {
						rawNext = ir.InvalidState
					}
				}
				if rawNext != ir.InvalidState {
					state = rawNext & (ir.StateIDMask | ir.WarpStateFlag)
					if byteVal < 0x80 || (rawNext&ir.WarpStateFlag) == 0 {
						i++
					} else {
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
}

var matchContextPool = sync.Pool{New: func() any { return &matchContext{} }}
