package regexp

import (
	"bytes"
	"encoding/binary"

	"github.com/kamichidu/go-regexp-re/internal/ir"
	"github.com/kamichidu/go-regexp-re/syntax"
)

func fastMatchExecLoop(re *Regexp, in ir.Input) (int, int, int) {
	d := re.dfa
	trans := d.Transitions()
	guards := d.AcceptingGuards()
	b := in.B
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

	i := in.SearchStart
	prio = i * ir.SearchRestartPenalty
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
			if req == 0 || (ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
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
				if !(ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
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
		if req == 0 || (ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
			p := prio + d.MatchPriority(sidx)
			if p <= bestPriority {
				bestPriority, bestEnd = p, numBytes
				if anchorStart {
					bestStart = 0
				} else {
					bestStart = prio / ir.SearchRestartPenalty
				}
			}
		}
	}
	return bestStart, bestEnd, bestPriority
}

func extendedMatchExecLoop(re *Regexp, in ir.Input) (int, int, int) {
	d := re.dfa
	trans := d.Transitions()
	guards := d.AcceptingGuards()
	b := in.B
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

	i := in.SearchStart
	prio = i * ir.SearchRestartPenalty
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
			if req == 0 || (ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
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
				if !(ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
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
		if req == 0 || (ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
			p := prio + d.MatchPriority(sidx)
			if p <= bestPriority {
				bestPriority, bestEnd = p, numBytes
				if anchorStart {
					bestStart = 0
				} else {
					bestStart = prio / ir.SearchRestartPenalty
				}
			}
		}
	}
	return bestStart, bestEnd, bestPriority
}

func extendedSubmatchExecLoop(re *Regexp, in ir.Input, mc *matchContext) (int, int, int) {
	d := re.dfa
	trans := d.Transitions()
	guards := d.AcceptingGuards()
	b := in.B
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

	i := in.SearchStart
	prio = i * ir.SearchRestartPenalty
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
			if req == 0 || (ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
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
				if !(ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
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
		if req == 0 || (ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
			p := prio + d.MatchPriority(sidx)
			if p <= bestPriority {
				bestPriority, bestEnd = p, numBytes
				if anchorStart {
					bestStart = 0
				} else {
					bestStart = prio / ir.SearchRestartPenalty
				}
			}
		}
	}
	return bestStart, bestEnd, bestPriority
}
