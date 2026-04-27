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

	i := 0
	ccWarps := d.CCWarpTable()
	for i < numBytes {
		sidx := state & ir.StateIDMask

		// Priority 1: SIMD Warp (SearchWarp) - skip noise
		if !anchorStart && sidx == (searchState&ir.StateIDMask) {
			oldI := i
			if len(re.prefix) > 0 {
				pos := bytes.Index(b[i:], re.prefix)
				if pos < 0 {
					i = numBytes
					break
				}
				i += pos
			} else if re.searchWarp.Kernel != ir.CCWarpNone && i < numBytes {
				info := re.searchWarp
				switch info.Kernel {
				case ir.CCWarpEqual:
					target := byte(info.V0)
					pos := bytes.IndexByte(b[i:], target)
					if pos < 0 {
						i = numBytes
					} else {
						i += pos
					}
				case ir.CCWarpSingleRange:
					if info.IndexAny != "" {
						pos := bytes.IndexAny(b[i:], info.IndexAny)
						if pos < 0 {
							i = numBytes
						} else {
							i += pos
						}
						break
					}
					low, high := info.V0, info.V1
					for i+8 <= numBytes {
						v := binary.LittleEndian.Uint64(b[i:])
						if ((v+0x7f7f7f7f7f7f7f7f-high)|(v-low))&0x8080808080808080 != 0x8080808080808080 {
							break
						}
						i += 8
					}
					for i < numBytes {
						bv := b[i]
						if bv >= byte(low) && bv <= byte(high) {
							break
						}
						i++
					}
				case ir.CCWarpNotSingleRange:
					low, high := info.V0, info.V1
					for i+8 <= numBytes {
						v := binary.LittleEndian.Uint64(b[i:])
						if ((v+0x7f7f7f7f7f7f7f7f-high)|(v-low))&0x8080808080808080 == 0x8080808080808080 {
							break
						}
						i += 8
					}
					for i < numBytes {
						bv := b[i]
						if bv < byte(low) || bv > byte(high) {
							break
						}
						i++
					}
				case ir.CCWarpNotEqual:
					target := byte(info.V0)
					for i < numBytes && b[i] == target {
						i++
					}
				case ir.CCWarpBitmask:
					if info.IndexAny != "" {
						pos := bytes.IndexAny(b[i:], info.IndexAny)
						if pos < 0 {
							i = numBytes
						} else {
							i += pos
						}
						break
					}
					m0, m1 := info.Extra[0], info.Extra[1]
					for i < numBytes {
						bv := b[i]
						if bv >= 128 {
							break
						}
						if bv < 64 {
							if (m0 & (1 << bv)) == 0 {
								break
							}
						} else {
							if (m1 & (1 << (bv - 64))) == 0 {
								break
							}
						}
						i++
					}
				}
			}
			if i > oldI {
				prio = i * ir.SearchRestartPenalty
				if i >= numBytes {
					break
				}
				continue
			}
		}

		// Priority 2: CCWarp (Match continuation skip)
		if (state&ir.CCWarpFlag) != 0 && i+8 <= numBytes {
			info := ccWarps[sidx]
			oldI := i
			switch info.Kernel {
			case ir.CCWarpEqual:
				target := info.V0
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if v != target {
						break
					}
					i += 8
				}
			case ir.CCWarpSingleRange:
				low, high := info.V0, info.V1
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if ((v+0x7f7f7f7f7f7f7f7f-high)|(v-low))&0x8080808080808080 != 0 {
						break
					}
					i += 8
				}
			case ir.CCWarpNotSingleRange:
				low, high := info.V0, info.V1
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if ((v+0x7f7f7f7f7f7f7f7f-high)|(v-low))&0x8080808080808080 != 0x8080808080808080 {
						break
					}
					i += 8
				}
			case ir.CCWarpAnyChar:
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if v&0x8080808080808080 != 0 {
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
				target := info.V0
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					diff := v ^ target
					if (diff-0x0101010101010101)&(^diff)&0x8080808080808080 != 0 {
						break
					}
					i += 8
				}
			case ir.CCWarpNotEqualSet:
				extra := info.Extra
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					match := uint64(0)
					for k := 0; k < 8; k++ {
						res := (v ^ extra[k]) & ^extra[k+8]
						match |= (res - 0x0101010101010101) & (^res)
					}
					if (match & 0x8080808080808080) != 0 {
						break
					}
					i += 8
				}
			case ir.CCWarpEqualSet:
				extra := info.Extra
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					match := uint64(0)
					for k := 0; k < 8; k++ {
						res := (v ^ extra[k]) & ^extra[k+8]
						match |= (res - 0x0101010101010101) & (^res)
					}
					if (match & 0x8080808080808080) != 0x8080808080808080 {
						break
					}
					i += 8
				}
			case ir.CCWarpNotBitmask:
				m0, m1 := info.Extra[0], info.Extra[1]
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if v&0x8080808080808080 != 0 {
						break
					}
					noneIncluded := true
					for k := 0; k < 8; k++ {
						bv := byte(v >> (k * 8))
						if (bv < 64 && (m0&(1<<bv)) != 0) || (bv >= 64 && (m1&(1<<(bv-64))) != 0) {
							noneIncluded = false
							break
						}
					}
					if !noneIncluded {
						break
					}
					i += 8
				}
			case ir.CCWarpBitmask:
				m0, m1 := info.Extra[0], info.Extra[1]
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if v&0x8080808080808080 != 0 {
						break
					}
					allIncluded := true
					for k := 0; k < 8; k++ {
						bv := byte(v >> (k * 8))
						if bv < 64 {
							if (m0 & (1 << bv)) == 0 {
								allIncluded = false
								break
							}
						} else {
							if (m1 & (1 << (bv - 64))) == 0 {
								allIncluded = false
								break
							}
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
				state = rawNext
				if (byteVal < 0x80) || (rawNext&ir.WarpStateFlag) == 0 {
					i++
				} else {
					i += 1 + ir.GetTrailingByteCount(byteVal)
				}
				continue
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

	// BCE hint
	if len(trans) > 0 {
		_ = trans[len(trans)-1]
	}
	if len(guards) > 0 {
		_ = guards[len(guards)-1]
	}

	i := 0
	ccWarps := d.CCWarpTable()
	for i < numBytes {
		sidx := state & ir.StateIDMask

		// Priority 1: SIMD Warp (SearchWarp) - skip noise
		if !anchorStart && sidx == (searchState&ir.StateIDMask) {
			oldI := i
			if len(re.prefix) > 0 {
				pos := bytes.Index(b[i:], re.prefix)
				if pos < 0 {
					i = numBytes
					break
				}
				i += pos
			} else if re.searchWarp.Kernel != ir.CCWarpNone && i < numBytes {
				info := re.searchWarp
				switch info.Kernel {
				case ir.CCWarpEqual:
					target := byte(info.V0)
					pos := bytes.IndexByte(b[i:], target)
					if pos < 0 {
						i = numBytes
					} else {
						i += pos
					}
				case ir.CCWarpSingleRange:
					if info.IndexAny != "" {
						pos := bytes.IndexAny(b[i:], info.IndexAny)
						if pos < 0 {
							i = numBytes
						} else {
							i += pos
						}
						break
					}
					low, high := info.V0, info.V1
					for i+8 <= numBytes {
						v := binary.LittleEndian.Uint64(b[i:])
						if ((v+0x7f7f7f7f7f7f7f7f-high)|(v-low))&0x8080808080808080 != 0x8080808080808080 {
							break
						}
						i += 8
					}
					for i < numBytes {
						bv := b[i]
						if bv >= byte(low) && bv <= byte(high) {
							break
						}
						i++
					}
				case ir.CCWarpNotSingleRange:
					low, high := info.V0, info.V1
					for i+8 <= numBytes {
						v := binary.LittleEndian.Uint64(b[i:])
						if ((v+0x7f7f7f7f7f7f7f7f-high)|(v-low))&0x8080808080808080 == 0x8080808080808080 {
							break
						}
						i += 8
					}
					for i < numBytes {
						bv := b[i]
						if bv < byte(low) || bv > byte(high) {
							break
						}
						i++
					}
				case ir.CCWarpNotEqual:
					target := byte(info.V0)
					for i < numBytes && b[i] == target {
						i++
					}
				case ir.CCWarpBitmask:
					if info.IndexAny != "" {
						pos := bytes.IndexAny(b[i:], info.IndexAny)
						if pos < 0 {
							i = numBytes
						} else {
							i += pos
						}
						break
					}
					m0, m1 := info.Extra[0], info.Extra[1]
					for i < numBytes {
						bv := b[i]
						if bv >= 128 {
							break
						}
						if bv < 64 {
							if (m0 & (1 << bv)) == 0 {
								break
							}
						} else {
							if (m1 & (1 << (bv - 64))) == 0 {
								break
							}
						}
						i++
					}
				}
			}
			if i > oldI {
				prio = i * ir.SearchRestartPenalty
				if i >= numBytes {
					break
				}
				continue
			}
		}

		// Priority 2: CCWarp (Match continuation skip)
		if (state&ir.CCWarpFlag) != 0 && i+8 <= numBytes {
			info := ccWarps[sidx]
			oldI := i
			switch info.Kernel {
			case ir.CCWarpEqual:
				target := info.V0
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if v != target {
						break
					}
					i += 8
				}
			case ir.CCWarpSingleRange:
				low, high := info.V0, info.V1
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if ((v+0x7f7f7f7f7f7f7f7f-high)|(v-low))&0x8080808080808080 != 0 {
						break
					}
					i += 8
				}
			case ir.CCWarpNotSingleRange:
				low, high := info.V0, info.V1
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if ((v+0x7f7f7f7f7f7f7f7f-high)|(v-low))&0x8080808080808080 != 0x8080808080808080 {
						break
					}
					i += 8
				}
			case ir.CCWarpAnyChar:
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if v&0x8080808080808080 != 0 {
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
				target := info.V0
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					diff := v ^ target
					if (diff-0x0101010101010101)&(^diff)&0x8080808080808080 != 0 {
						break
					}
					i += 8
				}
			case ir.CCWarpNotEqualSet:
				extra := info.Extra
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					match := uint64(0)
					for k := 0; k < 8; k++ {
						res := (v ^ extra[k]) & ^extra[k+8]
						match |= (res - 0x0101010101010101) & (^res)
					}
					if (match & 0x8080808080808080) != 0 {
						break
					}
					i += 8
				}
			case ir.CCWarpEqualSet:
				extra := info.Extra
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					match := uint64(0)
					for k := 0; k < 8; k++ {
						res := (v ^ extra[k]) & ^extra[k+8]
						match |= (res - 0x0101010101010101) & (^res)
					}
					if (match & 0x8080808080808080) != 0x8080808080808080 {
						break
					}
					i += 8
				}
			case ir.CCWarpNotBitmask:
				m0, m1 := info.Extra[0], info.Extra[1]
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if v&0x8080808080808080 != 0 {
						break
					}
					noneIncluded := true
					for k := 0; k < 8; k++ {
						bv := byte(v >> (k * 8))
						if (bv < 64 && (m0&(1<<bv)) != 0) || (bv >= 64 && (m1&(1<<(bv-64))) != 0) {
							noneIncluded = false
							break
						}
					}
					if !noneIncluded {
						break
					}
					i += 8
				}
			case ir.CCWarpBitmask:
				m0, m1 := info.Extra[0], info.Extra[1]
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if v&0x8080808080808080 != 0 {
						break
					}
					allIncluded := true
					for k := 0; k < 8; k++ {
						bv := byte(v >> (k * 8))
						if bv < 64 {
							if (m0 & (1 << bv)) == 0 {
								allIncluded = false
								break
							}
						} else {
							if (m1 & (1 << (bv - 64))) == 0 {
								allIncluded = false
								break
							}
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

	// BCE hint
	if len(trans) > 0 {
		_ = trans[len(trans)-1]
	}
	if len(guards) > 0 {
		_ = guards[len(guards)-1]
	}

	i := 0
	ccWarps := d.CCWarpTable()
	mc.appendRaw(state & ir.StateIDMask) // Initial state at pos 0

	for i < numBytes {
		sidx := state & ir.StateIDMask

		// Priority 1: SIMD Warp (SearchWarp) - skip noise
		if !anchorStart && sidx == (searchState&ir.StateIDMask) {
			oldI := i
			if len(re.prefix) > 0 {
				pos := bytes.Index(b[i:], re.prefix)
				if pos < 0 {
					mc.appendWarp(sidx, numBytes-i)
					i = numBytes
					break
				}
				if pos > 0 {
					mc.appendWarp(sidx, pos)
					i += pos
				}
			} else if re.searchWarp.Kernel != ir.CCWarpNone && i < numBytes {
				info := re.searchWarp
				switch info.Kernel {
				case ir.CCWarpEqual:
					target := byte(info.V0)
					pos := bytes.IndexByte(b[i:], target)
					if pos < 0 {
						mc.appendWarp(sidx, numBytes-i)
						i = numBytes
					} else {
						if pos > 0 {
							mc.appendWarp(sidx, pos)
						}
						i += pos
					}
				case ir.CCWarpSingleRange:
					if info.IndexAny != "" {
						pos := bytes.IndexAny(b[i:], info.IndexAny)
						if pos < 0 {
							mc.appendWarp(sidx, numBytes-i)
							i = numBytes
						} else {
							if pos > 0 {
								mc.appendWarp(sidx, pos)
							}
							i += pos
						}
						break
					}
					low, high := info.V0, info.V1
					oldWarpI := i
					for i < numBytes {
						bv := b[i]
						if bv >= byte(low) && bv <= byte(high) {
							break
						}
						i++
					}
					if i > oldWarpI {
						mc.appendWarp(sidx, i-oldWarpI)
					}
				case ir.CCWarpNotSingleRange:
					low, high := info.V0, info.V1
					oldWarpI := i
					for i < numBytes {
						bv := b[i]
						if bv < byte(low) || bv > byte(high) {
							break
						}
						i++
					}
					if i > oldWarpI {
						mc.appendWarp(sidx, i-oldWarpI)
					}
				case ir.CCWarpNotEqual:
					target := byte(info.V0)
					oldWarpI := i
					for i < numBytes && b[i] == target {
						i++
					}
					if i > oldWarpI {
						mc.appendWarp(sidx, i-oldWarpI)
					}
				case ir.CCWarpBitmask:
					if info.IndexAny != "" {
						pos := bytes.IndexAny(b[i:], info.IndexAny)
						if pos < 0 {
							mc.appendWarp(sidx, numBytes-i)
							i = numBytes
						} else {
							if pos > 0 {
								mc.appendWarp(sidx, pos)
							}
							i += pos
						}
						break
					}
					m0, m1 := info.Extra[0], info.Extra[1]
					oldWarpI := i
					for i < numBytes {
						bv := b[i]
						if bv >= 128 {
							break
						}
						if bv < 64 {
							if (m0 & (1 << bv)) == 0 {
								break
							}
						} else {
							if (m1 & (1 << (bv - 64))) == 0 {
								break
							}
						}
						i++
					}
					if i > oldWarpI {
						mc.appendWarp(sidx, i-oldWarpI)
					}
				}
			}
			if i > oldI {
				prio = i * ir.SearchRestartPenalty
				if i >= numBytes {
					break
				}
				continue
			}
		}

		// Priority 2: CCWarp (Match continuation skip)
		if (state&ir.CCWarpFlag) != 0 && i+8 <= numBytes {
			info := ccWarps[sidx]
			oldI := i
			switch info.Kernel {
			case ir.CCWarpEqual:
				target := info.V0
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if v != target {
						break
					}
					mc.appendWarp(sidx, 8)
					i += 8
				}
			case ir.CCWarpSingleRange:
				low, high := info.V0, info.V1
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if ((v+0x7f7f7f7f7f7f7f7f-high)|(v-low))&0x8080808080808080 != 0 {
						break
					}
					mc.appendWarp(sidx, 8)
					i += 8
				}
			case ir.CCWarpNotSingleRange:
				low, high := info.V0, info.V1
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if ((v+0x7f7f7f7f7f7f7f7f-high)|(v-low))&0x8080808080808080 != 0x8080808080808080 {
						break
					}
					mc.appendWarp(sidx, 8)
					i += 8
				}
			case ir.CCWarpAnyChar:
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if v&0x8080808080808080 != 0 {
						break
					}
					mc.appendWarp(sidx, 8)
					i += 8
				}
			case ir.CCWarpAnyExceptNL:
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					diff := v ^ 0x0A0A0A0A0A0A0A0A
					if (diff-0x0101010101010101)&(^diff)&0x8080808080808080 != 0 {
						break
					}
					mc.appendWarp(sidx, 8)
					i += 8
				}
			case ir.CCWarpNotEqual:
				target := info.V0
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					diff := v ^ target
					if (diff-0x0101010101010101)&(^diff)&0x8080808080808080 != 0 {
						break
					}
					mc.appendWarp(sidx, 8)
					i += 8
				}
			case ir.CCWarpNotEqualSet:
				extra := info.Extra
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					match := uint64(0)
					for k := 0; k < 8; k++ {
						res := (v ^ extra[k]) & ^extra[k+8]
						match |= (res - 0x0101010101010101) & (^res)
					}
					if (match & 0x8080808080808080) != 0 {
						break
					}
					mc.appendWarp(sidx, 8)
					i += 8
				}
			case ir.CCWarpEqualSet:
				extra := info.Extra
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					match := uint64(0)
					for k := 0; k < 8; k++ {
						res := (v ^ extra[k]) & ^extra[k+8]
						match |= (res - 0x0101010101010101) & (^res)
					}
					if (match & 0x8080808080808080) != 0x8080808080808080 {
						break
					}
					mc.appendWarp(sidx, 8)
					i += 8
				}
			case ir.CCWarpNotBitmask:
				m0, m1 := info.Extra[0], info.Extra[1]
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if v&0x8080808080808080 != 0 {
						break
					}
					noneIncluded := true
					for k := 0; k < 8; k++ {
						bv := byte(v >> (k * 8))
						if (bv < 64 && (m0&(1<<bv)) != 0) || (bv >= 64 && (m1&(1<<(bv-64))) != 0) {
							noneIncluded = false
							break
						}
					}
					if !noneIncluded {
						break
					}
					mc.appendWarp(sidx, 8)
					i += 8
				}
			case ir.CCWarpBitmask:
				m0, m1 := info.Extra[0], info.Extra[1]
				for i+8 <= numBytes {
					v := binary.LittleEndian.Uint64(b[i:])
					if v&0x8080808080808080 != 0 {
						break
					}
					allIncluded := true
					for k := 0; k < 8; k++ {
						bv := byte(v >> (k * 8))
						if bv < 64 {
							if (m0 & (1 << bv)) == 0 {
								allIncluded = false
								break
							}
						} else {
							if (m1 & (1 << (bv - 64))) == 0 {
								allIncluded = false
								break
							}
						}
					}
					if !allIncluded {
						break
					}
					mc.appendWarp(sidx, 8)
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
					if step > 1 {
						mc.appendWarp(state&ir.StateIDMask, step-1)
					}
					i += step
				}
				mc.appendRaw(state & ir.StateIDMask)
				continue
			}
		}
		if anchorStart {
			return bestStart, bestEnd, bestPriority
		}
		i++
		prio = i * ir.SearchRestartPenalty
		state = searchState
		mc.appendRaw(state & ir.StateIDMask)
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

func (re *Regexp) sparseTDFA_PathSelection(mc *matchContext, b []byte, start, end, prio int) {
	d := re.dfa
	recap := d.RecapTables()[0]
	uIndices, uUpdates := d.TagUpdateIndices(), d.TagUpdates()

	// 1. Find the history entry that covers the 'end' position.
	histIdx := -1
	byteOffset := 0
	for i, entry := range mc.history {
		length := 1
		if (entry & histWarpMarker) != 0 {
			length = int((entry & histLengthMask) >> histLengthShift)
		}
		if byteOffset+length > end {
			histIdx = i
			break
		}
		byteOffset += length
	}

	if histIdx == -1 {
		return
	}

	// Initial priority from the state at 'end'.
	entry := mc.history[histIdx]
	currPrio := int16(d.MatchPriority(entry & histStateMask))
	mc.pathHistory[end] = int32(currPrio)

	currPos := end
	// 2. Trace backward from end to start.
	// We need to use the transition: State(pos-1) --(byte at pos-1)--> State(pos)
	for i := histIdx; i >= 0; i-- {
		if currPos <= start {
			break
		}

		entry := mc.history[i]
		length := 1
		if (entry & histWarpMarker) != 0 {
			length = int((entry & histLengthMask) >> histLengthShift)
		}

		// Calculate how many bytes of this segment are within [start, currPos]
		segStart := byteOffset
		segEnd := byteOffset + length

		inSegEnd := currPos
		if segEnd < inSegEnd {
			// This should not happen in the first iteration because histIdx covers 'end'
			// In subsequent iterations, segEnd is always currPos.
			inSegEnd = segEnd
		}
		inSegStart := segStart
		if inSegStart < start {
			inSegStart = start
		}

		inSegLen := inSegEnd - inSegStart

		if (entry & histWarpMarker) != 0 {
			// Warp jump: Priority remains constant because SWAR is tag-free.
			for k := 0; k < inSegLen; k++ {
				currPos--
				mc.pathHistory[currPos] = int32(currPrio)
			}
		} else {
			// Normal entry: perform priority transition.
			currPos--
			if currPos < start {
				break
			}
			byteVal := b[currPos]

			// Source state is in the PREVIOUS entry (mc.history[i-1]).
			// Wait, what if currPos is in the middle of a warp?
			// That's handled by the Warp block above.
			// So here length is 1, and the source state is indeed mc.history[i-1].
			if i == 0 {
				break // Should not happen as we checked currPos > start
			}
			prevEntry := mc.history[i-1]
			sidx := prevEntry & histStateMask

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
			}
			mc.pathHistory[currPos] = int32(currPrio)
		}

		// Update byteOffset for the segment BEFORE this one.
		if i > 0 {
			prevEntry := mc.history[i-1]
			prevLen := 1
			if (prevEntry & histWarpMarker) != 0 {
				prevLen = int((prevEntry & histLengthMask) >> histLengthShift)
			}
			byteOffset -= prevLen
		}
	}
}

func (re *Regexp) sparseTDFA_Recap(mc *matchContext, b []byte, start, end, prio int, regs []int) {
	d := re.dfa
	recap := d.RecapTables()[0]
	uIndices, uUpdates := d.TagUpdateIndices(), d.TagUpdates()

	re.applyEntryTags(regs, d.StartUpdates(), mc.pathHistory[start], start)

	// 1. Find the starting history entry
	histIdx := 0
	byteOffset := 0
	for i, entry := range mc.history {
		length := 1
		if (entry & histWarpMarker) != 0 {
			length = int((entry & histLengthMask) >> histLengthShift)
		}
		if byteOffset+length > start {
			histIdx = i
			break
		}
		byteOffset += length
	}

	currPos := start
	for i := histIdx; i < len(mc.history); i++ {
		entry := mc.history[i]
		if currPos >= end {
			break
		}

		length := 1
		if (entry & histWarpMarker) != 0 {
			length = int((entry & histLengthMask) >> histLengthShift)
		}

		if (entry & histWarpMarker) != 0 {
			// Warp jump: No tag updates in this segment.
			// The first warp might be partially covered if it starts before 'start'
			warpStart := byteOffset
			warpEnd := byteOffset + length

			actualLength := length
			if warpStart < start {
				actualLength = warpEnd - start
			}
			if currPos+actualLength > end {
				actualLength = end - currPos
			}

			currPos += actualLength
			byteOffset += length
			continue
		}

		// Normal entry: process tags
		sidx := entry & histStateMask
		pathID := mc.pathHistory[currPos]
		byteVal := b[currPos]
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

			nextPathID := int32(0)
			if currPos+step <= end {
				nextPathID = mc.pathHistory[currPos+step]
			}

			for _, entry := range recap.Transitions[off] {
				if entry.InputPriority == int16(pathID)-basePrio && int32(entry.NextPriority) == nextPathID {
					re.applyRawTags(regs, entry.PreTags, currPos)
					re.applyRawTags(regs, entry.PostTags, currPos+step)
					break
				}
			}
		}
		currPos += step
		byteOffset += length
	}
}

func (re *Regexp) applyRawTags(regs []int, tags uint64, pos int) {
	if tags == 0 {
		return
	}
	for bit := 2; bit < 64; bit++ {
		if (tags & (1 << uint(bit))) != 0 {
			if bit < len(regs) {
				if (bit%2 != 0) || regs[bit] == -1 {
					regs[bit] = pos
				}
			}
		}
	}
}

func (re *Regexp) applyEntryTags(regs []int, updates []ir.PathTagUpdate, pathID int32, pos int) {
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

const (
	histWarpMarker  uint32 = 0x80000000
	histLengthMask  uint32 = 0x7FF00000
	histStateMask   uint32 = 0x000FFFFF
	histLengthShift        = 20
	histMaxLength          = 2047
)

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
			mc.history = make([]uint32, 0, required)
		} else {
			mc.history = mc.history[:0]
		}
		if cap(mc.pathHistory) < required {
			mc.pathHistory = make([]int32, required)
		} else {
			mc.pathHistory = mc.pathHistory[:required]
		}
	} else {
		mc.history = mc.historyBuf[:0]
		mc.pathHistory = mc.pathHistoryBuf[:required]
	}

	for i := range mc.pathHistory {
		mc.pathHistory[i] = -1
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

func (mc *matchContext) appendRaw(sidx uint32) {
	mc.history = append(mc.history, sidx&histStateMask)
}

func (mc *matchContext) appendWarp(sidx uint32, n int) {
	sidx &= histStateMask
	if len(mc.history) > 0 {
		last := mc.history[len(mc.history)-1]
		if (last&histWarpMarker) != 0 && (last&histStateMask) == sidx {
			lenVal := (last & histLengthMask) >> histLengthShift
			if int(lenVal)+n <= histMaxLength {
				mc.history[len(mc.history)-1] = histWarpMarker | ((lenVal + uint32(n)) << histLengthShift) | sidx
				return
			}
		}
	}
	mc.history = append(mc.history, histWarpMarker|((uint32(n))<<histLengthShift)|sidx)
}

var matchContextPool = sync.Pool{New: func() any { return &matchContext{} }}
