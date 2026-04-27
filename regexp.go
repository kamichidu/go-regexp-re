package regexp

import (
	"bytes"
	"context"
	"encoding/binary"
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
	strategyFast
	strategyExtended
)

type Regexp struct {
	expr           string
	numSubexp      int
	prefix         []byte
	complete       bool
	anchorStart    bool
	hasAnchors     bool
	prog           *syntax.Prog
	dfa            *ir.DFA
	literalMatcher *ir.LiteralMatcher
	subexpNames    []string
	strategy       matchStrategy
	searchState    uint32
	matchState     uint32
	uIndices       []uint32
	uPrioDeltas    []int32
	searchWarp     ir.CCWarpInfo
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
	var uIndices []uint32
	var uPrioDeltas []int32
	var searchWarp ir.CCWarpInfo

	if literalMatcher == nil {
		// Always build the heavy DFA to support correct FindSubmatchIndex results
		// and capture groups, unless forced otherwise.
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

		uIndices = dfa.TagUpdateIndices()
		tagUpdates := dfa.TagUpdates()
		uPrioDeltas = make([]int32, len(tagUpdates))
		for i, update := range tagUpdates {
			uPrioDeltas[i] = update.BasePriority
		}
		searchWarp = dfa.SearchWarp()
	}

	res := &Regexp{
		expr:           expr,
		numSubexp:      numSubexp,
		prefix:         []byte(prefix),
		complete:       complete,
		anchorStart:    anchorStart,
		hasAnchors:     hasAnchors(prog),
		prog:           prog,
		dfa:            dfa,
		literalMatcher: literalMatcher,
		subexpNames:    subexpNames,
		searchState:    searchState,
		matchState:     matchState,
		uIndices:       uIndices,
		uPrioDeltas:    uPrioDeltas,
		searchWarp:     searchWarp,
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

func hasAnchors(prog *syntax.Prog) bool {
	for _, inst := range prog.Inst {
		if inst.Op == syntax.InstEmptyWidth {
			return true
		}
	}
	return false
}

func (re *Regexp) bindMatchStrategy() {
	if re.literalMatcher != nil {
		re.strategy = strategyLiteral
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
		regs := make([]int, (re.numSubexp+1)*2)
		for i := range regs {
			regs[i] = -1
		}
		if !re.literalMatcher.FindSubmatchIndexInto(b, regs) {
			return nil
		}
		return regs
	}

	mc := matchContextPool.Get().(*matchContext)
	defer matchContextPool.Put(mc)
	mc.prepare(len(b), re.numSubexp)

	start, end, prio := re.submatch(b, mc)
	if start < 0 {
		return nil
	}

	regs := mc.regs
	regs[0], regs[1] = start, end
	if re.numSubexp > 0 {
		re.sparseTDFA_PathSelection(mc, b, start, end, prio)
		re.sparseTDFA_Recap(mc, b, start, end, prio, regs)
	}

	// Must return a copy because mc is returned to Pool
	res := make([]int, len(mc.regs))
	copy(res, mc.regs)
	return res
}

func (re *Regexp) findSubmatchIndexInternal(b []byte, mc *matchContext, regs []int) (int, int, int) {
	switch re.strategy {
	case strategyLiteral:
		res := re.literalMatcher.FindSubmatchIndex(b)
		if res == nil {
			return -1, -1, 0
		}
		return res[0], res[1], 0
	case strategyFast, strategyExtended:
		if mc == nil {
			return re.match(b)
		}
		mc.prepare(len(b), re.numSubexp)
		return re.submatch(b, mc)
	}
	return -1, -1, 0
}

func (re *Regexp) match(b []byte) (int, int, int) {
	switch re.strategy {
	case strategyExtended:
		return extendedMatchExecLoop(re, b)
	default:
		return fastMatchExecLoop(re, b)
	}
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

	i := 0
	ccWarps := d.CCWarpTable()
	for i < numBytes {
		sidx := state & ir.StateIDMask

		// Priority 1: SIMD Warp (SearchWarp) - skip noise
		if !anchorStart && (state&ir.StateIDMask) == (searchState&ir.StateIDMask) {
			oldI := i
			if len(re.prefix) > 0 {
				pos := bytes.Index(b[i:], re.prefix)
				if pos < 0 {
					i = numBytes
					break
				}
				i += pos
			} else if re.searchWarp.Kernel != ir.CCWarpNone && i+8 <= numBytes {
				info := re.searchWarp
				switch info.Kernel {
				case ir.CCWarpSingleRange:
					low, high := info.Splats[0], info.Splats[1]
					for i+8 <= numBytes {
						v := binary.LittleEndian.Uint64(b[i:])
						if ((v+0x7f7f7f7f7f7f7f7f-high)|(v-low))&0x8080808080808080 != 0x8080808080808080 {
							break
						}
						i += 8
					}
				case ir.CCWarpNotEqual:
					target := info.Splats[0]
					for i+8 <= numBytes {
						v := binary.LittleEndian.Uint64(b[i:])
						diff := v ^ target
						if (diff-0x0101010101010101)&(^diff)&0x8080808080808080 != 0x8080808080808080 {
							break
						}
						i += 8
					}
				case ir.CCWarpNotEqualSet:
					b0, b1, b2, b3, b4, b5, b6, b7 := info.Splats[0], info.Splats[1], info.Splats[2], info.Splats[3], info.Splats[4], info.Splats[5], info.Splats[6], info.Splats[7]
					m0, m1, m2, m3, m4, m5, m6, m7 := info.Masks[0], info.Masks[1], info.Masks[2], info.Masks[3], info.Masks[4], info.Masks[5], info.Masks[6], info.Masks[7]
					for i+8 <= numBytes {
						v := binary.LittleEndian.Uint64(b[i:])
						res := ((v ^ b0) & ^m0)
						res &= ((v ^ b1) & ^m1)
						res &= ((v ^ b2) & ^m2)
						res &= ((v ^ b3) & ^m3)
						res &= ((v ^ b4) & ^m4)
						res &= ((v ^ b5) & ^m5)
						res &= ((v ^ b6) & ^m6)
						res &= ((v ^ b7) & ^m7)
						// Has any byte become zero?
						if (res-0x0101010101010101)&(^res)&0x8080808080808080 != 0x8080808080808080 {
							break
						}
						i += 8
					}
				case ir.CCWarpNotBitmask:
					mask := info.Mask
					for i+8 <= numBytes {
						v := binary.LittleEndian.Uint64(b[i:])
						if v&0x8080808080808080 != 0 {
							break
						}
						allExcluded := true
						for k := 0; k < 8; k++ {
							bv := byte(v >> (k * 8))
							if (mask[bv>>6] & (1 << (bv & 63))) == 0 {
								allExcluded = false
								break
							}
						}
						if !allExcluded {
							break
						}
						i += 8
					}
				case ir.CCWarpBitmask:
					mask := info.Mask
					for i+8 <= numBytes {
						v := binary.LittleEndian.Uint64(b[i:])
						if v&0x8080808080808080 != 0 {
							break
						}
						noneIncluded := true
						for k := 0; k < 8; k++ {
							bv := byte(v >> (k * 8))
							if (mask[bv>>6] & (1 << (bv & 63))) != 0 {
								noneIncluded = false
								break
							}
						}
						if !noneIncluded {
							break
						}
						i += 8
					}
				}
			}
			if i > oldI {
				prio = i * ir.SearchRestartPenalty
				if i >= numBytes {
					break
				}
				sidx = state & ir.StateIDMask
			}
		}

		// Priority 2: CCWarp (Match continuation skip)
		if (state&ir.CCWarpFlag) != 0 && i+8 <= numBytes {
			info := ccWarps[sidx]
			oldI := i
			switch info.Kernel {
			case ir.CCWarpSingleRange:
				low, high := info.Splats[0], info.Splats[1]
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if ((v+0x7f7f7f7f7f7f7f7f-high)|(v-low))&0x8080808080808080 != 0 {
						break
					}
					i += 8
				}
			case ir.CCWarpAnyExceptNL:
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					diff := v ^ 0x0A0A0A0A0A0A0A0A
					if (diff-0x0101010101010101)&(^diff)&0x8080808080808080 != 0 {
						break
					}
					i += 8
				}
			case ir.CCWarpNotEqual:
				target := info.Splats[0]
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					diff := v ^ target
					if (diff-0x0101010101010101)&(^diff)&0x8080808080808080 != 0 {
						break
					}
					i += 8
				}
			case ir.CCWarpNotEqualSet:
				b0, b1, b2, b3, b4, b5, b6, b7 := info.Splats[0], info.Splats[1], info.Splats[2], info.Splats[3], info.Splats[4], info.Splats[5], info.Splats[6], info.Splats[7]
				m0, m1, m2, m3, m4, m5, m6, m7 := info.Masks[0], info.Masks[1], info.Masks[2], info.Masks[3], info.Masks[4], info.Masks[5], info.Masks[6], info.Masks[7]
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					res := ((v ^ b0) & ^m0)
					res &= ((v ^ b1) & ^m1)
					res &= ((v ^ b2) & ^m2)
					res &= ((v ^ b3) & ^m3)
					res &= ((v ^ b4) & ^m4)
					res &= ((v ^ b5) & ^m5)
					res &= ((v ^ b6) & ^m6)
					res &= ((v ^ b7) & ^m7)
					if (res-0x0101010101010101)&(^res)&0x8080808080808080 != 0 {
						break
					}
					i += 8
				}

			case ir.CCWarpNotBitmask:
				mask := info.Mask
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if v&0x8080808080808080 != 0 {
						break
					}
					noneExcluded := true
					for k := 0; k < 8; k++ {
						bv := byte(v >> (k * 8))
						if (mask[bv>>6] & (1 << (bv & 63))) != 0 {
							noneExcluded = false
							break
						}
					}
					if !noneExcluded {
						break
					}
					i += 8
				}
			case ir.CCWarpBitmask:
				mask := info.Mask
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if v&0x8080808080808080 != 0 {
						break
					}
					allIncluded := true
					for k := 0; k < 8; k++ {
						bv := byte(v >> (k * 8))
						if (mask[bv>>6] & (1 << (bv & 63))) == 0 {
							allIncluded = false
							break
						}
					}
					if !allIncluded {
						break
					}
					i += 8
				}
			}
			if i > oldI {
				if (state & ir.AcceptingStateFlag) != 0 {
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
				continue
			}
		}

		if (state & ir.AcceptingStateFlag) != 0 {
			req := guards[sidx]
			if req == 0 || (ir.VerifyEnd(b, i, numBytes, req) && ir.VerifyBegin(b, i, req) && ir.VerifyWord(b, i, numBytes, req)) {
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
					if !(ir.VerifyEnd(b, i, numBytes, req) && ir.VerifyBegin(b, i, req) && ir.VerifyWord(b, i, numBytes, req)) {
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
			return bestStart, bestEnd, bestPriority
		}
		i++
		prio = i * ir.SearchRestartPenalty
		state = searchState
	}

	// Final EOF check
	sidx := state & ir.StateIDMask
	if (state & ir.AcceptingStateFlag) != 0 {
		req := guards[sidx]
		if req == 0 || (ir.VerifyEnd(b, numBytes, numBytes, req) && ir.VerifyBegin(b, numBytes, req) && ir.VerifyWord(b, numBytes, numBytes, req)) {
			p := prio + d.MatchPriority(sidx)
			if p <= bestPriority {
				bestPriority, bestEnd = p, numBytes
				if anchorStart {
					bestStart = 0
				} else {
					bestStart = p / ir.SearchRestartPenalty
				}
			}
		}
	}
	return bestStart, bestEnd, bestPriority
}

func extendedMatchExecLoop(re *Regexp, b []byte) (int, int, int) {
	d := re.dfa
	trans := d.Transitions()
	guards := d.AcceptingGuards()
	numStates := d.NumStates()
	numBytes := len(b)
	searchState := re.searchState
	matchState := re.matchState
	uIndices := re.uIndices
	uPrioDeltas := re.uPrioDeltas

	anchorStart := re.anchorStart
	bestStart, bestEnd, bestPriority := -1, -1, 1<<60-1
	state, prio := searchState, 0
	if anchorStart {
		state = matchState
	}

	i := 0
	ccWarps := d.CCWarpTable()
	for i < numBytes {
		sidx := state & ir.StateIDMask

		// Priority 1: SIMD Warp (SearchWarp) - skip noise
		if !anchorStart && (state&ir.StateIDMask) == (searchState&ir.StateIDMask) {
			oldI := i
			if len(re.prefix) > 0 {
				pos := bytes.Index(b[i:], re.prefix)
				if pos < 0 {
					i = numBytes
					break
				}
				i += pos
			} else if re.searchWarp.Kernel != ir.CCWarpNone && i+8 <= numBytes {
				info := re.searchWarp
				switch info.Kernel {
				case ir.CCWarpSingleRange:
					low, high := info.Splats[0], info.Splats[1]
					for i+8 <= numBytes {
						v := binary.LittleEndian.Uint64(b[i:])
						if ((v+0x7f7f7f7f7f7f7f7f-high)|(v-low))&0x8080808080808080 != 0x8080808080808080 {
							break
						}
						i += 8
					}
				case ir.CCWarpNotEqual:
					target := info.Splats[0]
					for i+8 <= numBytes {
						v := binary.LittleEndian.Uint64(b[i:])
						diff := v ^ target
						if (diff-0x0101010101010101)&(^diff)&0x8080808080808080 != 0x8080808080808080 {
							break
						}
						i += 8
					}
				case ir.CCWarpNotEqualSet:
					b0, b1, b2, b3, b4, b5, b6, b7 := info.Splats[0], info.Splats[1], info.Splats[2], info.Splats[3], info.Splats[4], info.Splats[5], info.Splats[6], info.Splats[7]
					m0, m1, m2, m3, m4, m5, m6, m7 := info.Masks[0], info.Masks[1], info.Masks[2], info.Masks[3], info.Masks[4], info.Masks[5], info.Masks[6], info.Masks[7]
					for i+8 <= numBytes {
						v := binary.LittleEndian.Uint64(b[i:])
						res := ((v ^ b0) & ^m0)
						res &= ((v ^ b1) & ^m1)
						res &= ((v ^ b2) & ^m2)
						res &= ((v ^ b3) & ^m3)
						res &= ((v ^ b4) & ^m4)
						res &= ((v ^ b5) & ^m5)
						res &= ((v ^ b6) & ^m6)
						res &= ((v ^ b7) & ^m7)
						// Has any byte become zero?
						if (res-0x0101010101010101)&(^res)&0x8080808080808080 != 0x8080808080808080 {
							break
						}
						i += 8
					}
				case ir.CCWarpNotBitmask:
					mask := info.Mask
					for i+8 <= numBytes {
						v := binary.LittleEndian.Uint64(b[i:])
						if v&0x8080808080808080 != 0 {
							break
						}
						allExcluded := true
						for k := 0; k < 8; k++ {
							bv := byte(v >> (k * 8))
							if (mask[bv>>6] & (1 << (bv & 63))) == 0 {
								allExcluded = false
								break
							}
						}
						if !allExcluded {
							break
						}
						i += 8
					}
				case ir.CCWarpBitmask:
					mask := info.Mask
					for i+8 <= numBytes {
						v := binary.LittleEndian.Uint64(b[i:])
						if v&0x8080808080808080 != 0 {
							break
						}
						noneIncluded := true
						for k := 0; k < 8; k++ {
							bv := byte(v >> (k * 8))
							if (mask[bv>>6] & (1 << (bv & 63))) != 0 {
								noneIncluded = false
								break
							}
						}
						if !noneIncluded {
							break
						}
						i += 8
					}
				}
			}
			if i > oldI {
				prio = i * ir.SearchRestartPenalty
				if i >= numBytes {
					break
				}
				sidx = state & ir.StateIDMask
			}
		}

		// Priority 2: CCWarp (Match continuation skip)
		if (state&ir.CCWarpFlag) != 0 && i+8 <= numBytes {
			info := ccWarps[sidx]
			oldI := i
			switch info.Kernel {
			case ir.CCWarpSingleRange:
				low, high := info.Splats[0], info.Splats[1]
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if ((v+0x7f7f7f7f7f7f7f7f-high)|(v-low))&0x8080808080808080 != 0 {
						break
					}
					i += 8
				}
			case ir.CCWarpAnyExceptNL:
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					diff := v ^ 0x0A0A0A0A0A0A0A0A
					if (diff-0x0101010101010101)&(^diff)&0x8080808080808080 != 0 {
						break
					}
					i += 8
				}
			case ir.CCWarpNotEqual:
				target := info.Splats[0]
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					diff := v ^ target
					if (diff-0x0101010101010101)&(^diff)&0x8080808080808080 != 0 {
						break
					}
					i += 8
				}
			case ir.CCWarpNotEqualSet:
				b0, b1, b2, b3, b4, b5, b6, b7 := info.Splats[0], info.Splats[1], info.Splats[2], info.Splats[3], info.Splats[4], info.Splats[5], info.Splats[6], info.Splats[7]
				m0, m1, m2, m3, m4, m5, m6, m7 := info.Masks[0], info.Masks[1], info.Masks[2], info.Masks[3], info.Masks[4], info.Masks[5], info.Masks[6], info.Masks[7]
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					res := ((v ^ b0) & ^m0)
					res &= ((v ^ b1) & ^m1)
					res &= ((v ^ b2) & ^m2)
					res &= ((v ^ b3) & ^m3)
					res &= ((v ^ b4) & ^m4)
					res &= ((v ^ b5) & ^m5)
					res &= ((v ^ b6) & ^m6)
					res &= ((v ^ b7) & ^m7)
					if (res-0x0101010101010101)&(^res)&0x8080808080808080 != 0 {
						break
					}
					i += 8
				}

			case ir.CCWarpNotBitmask:
				mask := info.Mask
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if v&0x8080808080808080 != 0 {
						break
					}
					noneExcluded := true
					for k := 0; k < 8; k++ {
						bv := byte(v >> (k * 8))
						if (mask[bv>>6] & (1 << (bv & 63))) != 0 {
							noneExcluded = false
							break
						}
					}
					if !noneExcluded {
						break
					}
					i += 8
				}
			case ir.CCWarpBitmask:
				mask := info.Mask
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if v&0x8080808080808080 != 0 {
						break
					}
					allIncluded := true
					for k := 0; k < 8; k++ {
						bv := byte(v >> (k * 8))
						if (mask[bv>>6] & (1 << (bv & 63))) == 0 {
							allIncluded = false
							break
						}
					}
					if !allIncluded {
						break
					}
					i += 8
				}
			}
			if i > oldI {
				if (state & ir.AcceptingStateFlag) != 0 {
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
				continue
			}
		}

		if (state & ir.AcceptingStateFlag) != 0 {
			req := guards[sidx]
			if req == 0 || (ir.VerifyEnd(b, i, numBytes, req) && ir.VerifyBegin(b, i, req) && ir.VerifyWord(b, i, numBytes, req)) {
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

		byteVal := b[i]
		if int(sidx) < numStates {
			off := (int(sidx) << 8) | int(byteVal)
			rawNext := trans[off]
			if rawNext != ir.InvalidState {
				// Handle special flags
				if (rawNext & ir.AnchorVerifyFlag) != 0 {
					req := syntax.EmptyOp((rawNext & ir.AnchorMask) >> 22)
					if !(ir.VerifyEnd(b, i, numBytes, req) && ir.VerifyBegin(b, i, req) && ir.VerifyWord(b, i, numBytes, req)) {
						rawNext = ir.InvalidState
					}
				}
				if rawNext != ir.InvalidState {
					if (rawNext & ir.TaggedStateFlag) != 0 {
						if off < len(uIndices) {
							uIdx := uIndices[off]
							if int(uIdx) < len(uPrioDeltas) {
								prio += int(uPrioDeltas[uIdx])
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
			return bestStart, bestEnd, bestPriority
		}
		i++
		prio = i * ir.SearchRestartPenalty
		state = searchState
	}

	// Final EOF check
	sidx := state & ir.StateIDMask
	if (state & ir.AcceptingStateFlag) != 0 {
		req := guards[sidx]
		if req == 0 || (ir.VerifyEnd(b, numBytes, numBytes, req) && ir.VerifyBegin(b, numBytes, req) && ir.VerifyWord(b, numBytes, numBytes, req)) {
			p := prio + d.MatchPriority(sidx)
			if p <= bestPriority {
				bestPriority, bestEnd = p, numBytes
				if anchorStart {
					bestStart = 0
				} else {
					bestStart = p / ir.SearchRestartPenalty
				}
			}
		}
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
	uIndices := re.uIndices
	uPrioDeltas := re.uPrioDeltas
	searchState := re.searchState
	matchState := re.matchState

	anchorStart := re.anchorStart
	bestStart, bestEnd, bestPriority := -1, -1, 1<<60-1
	state, prio := searchState, 0
	if anchorStart {
		state = matchState
	}

	i := 0
	ccWarps := d.CCWarpTable()
	for i < numBytes {
		sidx := state & ir.StateIDMask

		// Priority 1: SIMD Warp (SearchWarp) - skip noise
		if !anchorStart && (state&ir.StateIDMask) == (searchState&ir.StateIDMask) {
			oldI := i
			if len(re.prefix) > 0 {
				pos := bytes.Index(b[i:], re.prefix)
				if pos < 0 {
					i = numBytes
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
			} else if re.searchWarp.Kernel != ir.CCWarpNone && i+8 <= numBytes {
				// Dedicated SWAR Pre-filter
				info := re.searchWarp
				switch info.Kernel {
				case ir.CCWarpSingleRange:
					low, high := info.Splats[0], info.Splats[1]
					for i+8 <= numBytes {
						v := binary.LittleEndian.Uint64(b[i:])
						if ((v+0x7f7f7f7f7f7f7f7f-high)|(v-low))&0x8080808080808080 != 0x8080808080808080 {
							break
						}
						for k := 0; k < 8; k++ {
							mc.history[i+k] = sidx
						}
						i += 8
					}
				case ir.CCWarpNotEqual:
					target := info.Splats[0]
					for i+8 <= numBytes {
						v := binary.LittleEndian.Uint64(b[i:])
						diff := v ^ target
						if (diff-0x0101010101010101)&(^diff)&0x8080808080808080 != 0 {
							break
						}
						for k := 0; k < 8; k++ {
							mc.history[i+k] = sidx
						}
						i += 8
					}
				case ir.CCWarpNotEqualSet:
					b0, b1, b2, b3, b4, b5, b6, b7 := info.Splats[0], info.Splats[1], info.Splats[2], info.Splats[3], info.Splats[4], info.Splats[5], info.Splats[6], info.Splats[7]
					m0, m1, m2, m3, m4, m5, m6, m7 := info.Masks[0], info.Masks[1], info.Masks[2], info.Masks[3], info.Masks[4], info.Masks[5], info.Masks[6], info.Masks[7]
					for i+8 <= numBytes {
						v := binary.LittleEndian.Uint64(b[i:])
						res := ((v ^ b0) & ^m0)
						res &= ((v ^ b1) & ^m1)
						res &= ((v ^ b2) & ^m2)
						res &= ((v ^ b3) & ^m3)
						res &= ((v ^ b4) & ^m4)
						res &= ((v ^ b5) & ^m5)
						res &= ((v ^ b6) & ^m6)
						res &= ((v ^ b7) & ^m7)
						if (res-0x0101010101010101)&(^res)&0x8080808080808080 != 0x8080808080808080 {
							break
						}
						for k := 0; k < 8; k++ {
							mc.history[i+k] = sidx
						}
						i += 8
					}
				case ir.CCWarpNotBitmask:
					mask := info.Mask
					for i+8 <= numBytes {
						v := binary.LittleEndian.Uint64(b[i:])
						if v&0x8080808080808080 != 0 {
							break
						}
						ok := true
						for k := 0; k < 8; k++ {
							bv := byte(v >> (k * 8))
							if (mask[bv>>6] & (1 << (bv & 63))) != 0 {
								ok = false
								break
							}
						}
						if !ok {
							break
						}
						for k := 0; k < 8; k++ {
							mc.history[i+k] = sidx
						}
						i += 8
					}
				case ir.CCWarpBitmask:
					mask := info.Mask
					for i+8 <= numBytes {
						v := binary.LittleEndian.Uint64(b[i:])
						if v&0x8080808080808080 != 0 {
							break
						}
						noneIncluded := true
						for k := 0; k < 8; k++ {
							bv := byte(v >> (k * 8))
							if (mask[bv>>6] & (1 << (bv & 63))) != 0 {
								noneIncluded = false
								break
							}
						}
						if !noneIncluded {
							break
						}
						for k := 0; k < 8; k++ {
							mc.history[i+k] = sidx
						}
						i += 8
					}
				}
				if i > oldI {
					prio = i * ir.SearchRestartPenalty
					continue
				}
			}
		}

		// Priority 2: CCWarp (Match continuation skip)
		if (state&ir.CCWarpFlag) != 0 && i+8 <= numBytes {
			info := ccWarps[sidx]
			oldI := i
			switch info.Kernel {
			case ir.CCWarpSingleRange:
				low, high := info.Splats[0], info.Splats[1]
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if ((v+0x7f7f7f7f7f7f7f7f-high)|(v-low))&0x8080808080808080 != 0 {
						break
					}
					for k := 0; k < 8; k++ {
						mc.history[i+k] = sidx
					}
					i += 8
				}
			case ir.CCWarpAnyExceptNL:
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					diff := v ^ 0x0A0A0A0A0A0A0A0A
					if (diff-0x0101010101010101)&(^diff)&0x8080808080808080 != 0 {
						break
					}
					for k := 0; k < 8; k++ {
						mc.history[i+k] = sidx
					}
					i += 8
				}
			case ir.CCWarpNotEqual:
				target := info.Splats[0]
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					diff := v ^ target
					if (diff-0x0101010101010101)&(^diff)&0x8080808080808080 != 0 {
						break
					}
					for k := 0; k < 8; k++ {
						mc.history[i+k] = sidx
					}
					i += 8
				}
			case ir.CCWarpNotEqualSet:
				s0, s1, s2, s3, s4, s5, s6, s7 := info.Splats[0], info.Splats[1], info.Splats[2], info.Splats[3], info.Splats[4], info.Splats[5], info.Splats[6], info.Splats[7]
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					m := ((v ^ s0) - 0x0101010101010101) & ^(v ^ s0)
					m |= ((v ^ s1) - 0x0101010101010101) & ^(v ^ s1)
					m |= ((v ^ s2) - 0x0101010101010101) & ^(v ^ s2)
					m |= ((v ^ s3) - 0x0101010101010101) & ^(v ^ s3)
					m |= ((v ^ s4) - 0x0101010101010101) & ^(v ^ s4)
					m |= ((v ^ s5) - 0x0101010101010101) & ^(v ^ s5)
					m |= ((v ^ s6) - 0x0101010101010101) & ^(v ^ s6)
					m |= ((v ^ s7) - 0x0101010101010101) & ^(v ^ s7)
					if (m & 0x8080808080808080) != 0 {
						break
					}
					for k := 0; k < 8; k++ {
						mc.history[i+k] = sidx
					}
					i += 8
				}
			case ir.CCWarpNotBitmask:
				mask := info.Mask
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if v&0x8080808080808080 != 0 {
						break
					}
					ok := true
					for k := 0; k < 8; k++ {
						bv := byte(v >> (k * 8))
						if (mask[bv>>6] & (1 << (bv & 63))) != 0 {
							ok = false
							break
						}
					}
					if !ok {
						break
					}
					for k := 0; k < 8; k++ {
						mc.history[i+k] = sidx
					}
					i += 8
				}
			case ir.CCWarpBitmask:
				mask := info.Mask
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if v&0x8080808080808080 != 0 {
						break
					}
					ok := true
					for k := 0; k < 8; k++ {
						bv := byte(v >> (k * 8))
						if (mask[bv>>6] & (1 << (bv & 63))) == 0 {
							ok = false
							break
						}
					}
					if !ok {
						break
					}
					for k := 0; k < 8; k++ {
						mc.history[i+k] = sidx
					}
					i += 8
				}
			}
			if i > oldI {
				if (state & ir.AcceptingStateFlag) != 0 {
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
				continue
			}
		}

		mc.history[i] = sidx
		if (state & ir.AcceptingStateFlag) != 0 {
			req := guards[sidx]
			if req == 0 || (ir.VerifyEnd(b, i, numBytes, req) && ir.VerifyBegin(b, i, req) && ir.VerifyWord(b, i, numBytes, req)) {
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

		byteVal := b[i]
		if int(sidx) < numStates {
			off := (int(sidx) << 8) | int(byteVal)
			rawNext := trans[off]
			if rawNext != ir.InvalidState {
				// Handle special flags
				if (rawNext & ir.AnchorVerifyFlag) != 0 {
					req := syntax.EmptyOp((rawNext & ir.AnchorMask) >> 22)
					if !(ir.VerifyEnd(b, i, numBytes, req) && ir.VerifyBegin(b, i, req) && ir.VerifyWord(b, i, numBytes, req)) {
						rawNext = ir.InvalidState
					}
				}
				if rawNext != ir.InvalidState {
					if (rawNext & ir.TaggedStateFlag) != 0 {
						if off < len(uIndices) {
							uIdx := uIndices[off]
							if int(uIdx) < len(uPrioDeltas) {
								prio += int(uPrioDeltas[uIdx])
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
			return bestStart, bestEnd, bestPriority
		}
		i++
		prio = i * ir.SearchRestartPenalty
		state = searchState
	}

	// Final EOF check
	sidx := state & ir.StateIDMask
	mc.history[numBytes] = sidx
	if (state & ir.AcceptingStateFlag) != 0 {
		req := guards[sidx]
		if req == 0 || (ir.VerifyEnd(b, numBytes, numBytes, req) && ir.VerifyBegin(b, numBytes, req) && ir.VerifyWord(b, numBytes, numBytes, req)) {
			p := prio + d.MatchPriority(sidx)
			if p <= bestPriority {
				bestPriority, bestEnd = p, numBytes
				if anchorStart {
					bestStart = 0
				} else {
					bestStart = p / ir.SearchRestartPenalty
				}
			}
		}
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
	regsBuf        [32]int
	regs           []int
}

func (mc *matchContext) prepare(n int, numSubexp int) {
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

	requiredRegs := (numSubexp + 1) * 2
	if requiredRegs <= len(mc.regsBuf) {
		mc.regs = mc.regsBuf[:requiredRegs]
	} else {
		if cap(mc.regs) < requiredRegs {
			mc.regs = make([]int, requiredRegs)
		} else {
			mc.regs = mc.regs[:requiredRegs]
		}
	}
	for i := range mc.regs {
		mc.regs[i] = -1
	}
}

var matchContextPool = sync.Pool{New: func() any { return &matchContext{} }}
