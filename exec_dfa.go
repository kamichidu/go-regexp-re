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
	matchState := re.matchState
	anchorStart := re.anchorStart

	bestStart, bestEnd, bestPriority := -1, -1, 1<<60-1
	if len(trans) > 0 {
		_ = trans[len(trans)-1]
	}
	if len(guards) > 0 {
		_ = guards[len(guards)-1]
	}

	ccWarps := d.CCWarpTable()

	for restartBase := 0; restartBase <= numBytes; restartBase++ {
		i := restartBase
		state := matchState

		// Skip optimization
		if !anchorStart && bestStart < 0 && i < numBytes {
			if len(re.prefix) > 0 {
				pos := bytes.Index(b[i:], re.prefix)
				if pos < 0 {
					break
				}
				restartBase += pos
				i = restartBase
			} else if re.searchWarp.Kernel != ir.CCWarpNone {
				info := re.searchWarp
				switch info.Kernel {
				case ir.CCWarpEqual:
					pos := bytes.IndexByte(b[i:], byte(info.V0))
					if pos < 0 {
						restartBase = numBytes
					} else {
						restartBase += pos
					}
				case ir.CCWarpSingleRange:
					low, high := byte(info.V0), byte(info.V1)
					for restartBase < numBytes && (b[restartBase] < low || b[restartBase] > high) {
						restartBase++
					}
				}
				i = restartBase
			}
		}

		// Match at current restartBase (could be length 0)
		sidx := state & ir.StateIDMask
		if (state & ir.AcceptingStateFlag) != 0 {
			req := guards[sidx]
			if req == 0 || (ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
				p := d.MatchPriority(sidx)
				if p < bestPriority {
					bestPriority, bestStart, bestEnd = p, restartBase, i
					if d.IsBestMatch(sidx) {
						return bestStart, bestEnd, bestPriority
					}
				}
			}
		}

		// Inner matching loop
		for i < numBytes {
			sidx = state & ir.StateIDMask
			if (state&ir.CCWarpFlag) != 0 && i+8 <= numBytes {
				info := ccWarps[sidx]
				oldI := i
				switch info.Kernel {
				case ir.CCWarpEqual:
					target := info.V0
					for i+8 <= numBytes && binary.LittleEndian.Uint64(b[i:]) == target {
						i += 8
					}
				case ir.CCWarpAnyChar:
					for i+8 <= numBytes && binary.LittleEndian.Uint64(b[i:])&0x8080808080808080 == 0 {
						i += 8
					}
				}
				if i > oldI {
					if (state & ir.AcceptingStateFlag) != 0 {
						req := guards[sidx]
						if req == 0 || (ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
							p := d.MatchPriority(sidx)
							if p < bestPriority {
								bestPriority, bestStart, bestEnd = p, restartBase, i
							} else if p == bestPriority && i > bestEnd {
								bestEnd = i
							}
						}
					}
					continue
				}
			}

			byteVal := b[i]
			off := (int(sidx) << 8) | int(byteVal)
			rawNext := trans[off]
			if rawNext == ir.InvalidState {
				break
			}
			if (rawNext & ir.AnchorVerifyFlag) != 0 {
				req := syntax.EmptyOp((rawNext & ir.AnchorMask) >> 22)
				if !(ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
					break
				}
			}
			state = rawNext
			i += 1
			if byteVal >= 0x80 && (rawNext&ir.WarpStateFlag) != 0 {
				i += ir.GetTrailingByteCount(byteVal)
			}
			// Record match after every step
			sidx = state & ir.StateIDMask
			if (state & ir.AcceptingStateFlag) != 0 {
				req := guards[sidx]
				if req == 0 || (ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
					p := d.MatchPriority(sidx)
					if p < bestPriority {
						bestPriority, bestStart, bestEnd = p, restartBase, i
						if d.IsBestMatch(sidx) {
							return bestStart, bestEnd, bestPriority
						}
					} else if p == bestPriority && i > bestEnd {
						bestEnd = i
					}
				}
			}
		}

		if bestStart >= 0 {
			// Found leftmost match
			return bestStart, bestEnd, bestPriority
		}
		if anchorStart {
			break
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
	uIndices := re.uIndices
	uPrioDeltas := re.uPrioDeltas
	matchState := re.matchState
	anchorStart := re.anchorStart

	bestStart, bestEnd, bestPriority := -1, -1, 1<<60-1
	if len(trans) > 0 {
		_ = trans[len(trans)-1]
	}
	if len(guards) > 0 {
		_ = guards[len(guards)-1]
	}

	for restartBase := 0; restartBase <= numBytes; restartBase++ {
		i := restartBase
		state, prio := matchState, 0

		// Skip optimization
		if !anchorStart && bestStart < 0 && i < numBytes {
			if len(re.prefix) > 0 {
				pos := bytes.Index(b[i:], re.prefix)
				if pos < 0 {
					break
				}
				restartBase += pos
				i = restartBase
			} else if re.searchWarp.Kernel != ir.CCWarpNone {
				info := re.searchWarp
				switch info.Kernel {
				case ir.CCWarpEqual:
					pos := bytes.IndexByte(b[i:], byte(info.V0))
					if pos < 0 {
						restartBase = numBytes
					} else {
						restartBase += pos
					}
				}
				i = restartBase
			}
		}

		sidx := state & ir.StateIDMask
		if (state & ir.AcceptingStateFlag) != 0 {
			req := guards[sidx]
			if req == 0 || (ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
				p := prio + d.MatchPriority(sidx)
				if p < bestPriority {
					bestPriority, bestStart, bestEnd = p, restartBase, i
					if d.IsBestMatch(sidx) {
						return bestStart, bestEnd, bestPriority
					}
				}
			}
		}

		for i < numBytes {
			sidx = state & ir.StateIDMask
			byteVal := b[i]
			off := (int(sidx) << 8) | int(byteVal)
			rawNext := trans[off]
			if rawNext == ir.InvalidState {
				break
			}
			if (rawNext & ir.AnchorVerifyFlag) != 0 {
				req := syntax.EmptyOp((rawNext & ir.AnchorMask) >> 22)
				if !(ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
					break
				}
			}
			if (rawNext&ir.TaggedStateFlag) != 0 && off < len(uIndices) {
				uIdx := uIndices[off]
				if int(uIdx) < len(uPrioDeltas) {
					prio += int(uPrioDeltas[uIdx])
				}
			}
			state = rawNext
			i += 1
			if byteVal >= 0x80 && (rawNext&ir.WarpStateFlag) != 0 {
				i += ir.GetTrailingByteCount(byteVal)
			}
			sidx = state & ir.StateIDMask
			if (state & ir.AcceptingStateFlag) != 0 {
				req := guards[sidx]
				if req == 0 || (ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
					p := prio + d.MatchPriority(sidx)
					if p < bestPriority {
						bestPriority, bestStart, bestEnd = p, restartBase, i
						if d.IsBestMatch(sidx) {
							return bestStart, bestEnd, bestPriority
						}
					} else if p == bestPriority && i > bestEnd {
						bestEnd = i
					}
				}
			}
		}

		if bestStart >= 0 {
			return bestStart, bestEnd, bestPriority
		}
		if anchorStart {
			break
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
	matchState := re.matchState
	anchorStart := re.anchorStart

	bestStart, bestEnd, bestPriority := -1, -1, 1<<60-1
	if len(trans) > 0 {
		_ = trans[len(trans)-1]
	}
	if len(guards) > 0 {
		_ = guards[len(guards)-1]
	}

	for restartBase := 0; restartBase <= numBytes; restartBase++ {
		i := restartBase
		state, prio := matchState, 0

		// Skip optimization
		if !anchorStart && bestStart < 0 && i < numBytes {
			if len(re.prefix) > 0 {
				pos := bytes.Index(b[i:], re.prefix)
				if pos < 0 {
					break
				}
				restartBase += pos
				i = restartBase
			} else if re.searchWarp.Kernel != ir.CCWarpNone {
				info := re.searchWarp
				switch info.Kernel {
				case ir.CCWarpEqual:
					pos := bytes.IndexByte(b[i:], byte(info.V0))
					if pos < 0 {
						restartBase = numBytes
					} else {
						restartBase += pos
					}
				}
				i = restartBase
			}
		}

		mc.clearHistory()
		mc.appendRaw(state & ir.StateIDMask)

		sidx := state & ir.StateIDMask
		if (state & ir.AcceptingStateFlag) != 0 {
			req := guards[sidx]
			if req == 0 || (ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
				p := prio + d.MatchPriority(sidx)
				if p < bestPriority {
					bestPriority, bestStart, bestEnd = p, restartBase, i
					if d.IsBestMatch(sidx) {
						return bestStart, bestEnd, bestPriority
					}
				}
			}
		}

		for i < numBytes {
			sidx = state & ir.StateIDMask
			byteVal := b[i]
			off := (int(sidx) << 8) | int(byteVal)
			rawNext := trans[off]
			if rawNext == ir.InvalidState {
				break
			}
			if (rawNext & ir.AnchorVerifyFlag) != 0 {
				req := syntax.EmptyOp((rawNext & ir.AnchorMask) >> 22)
				if !(ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
					break
				}
			}
			if (rawNext&ir.TaggedStateFlag) != 0 && off < len(uIndices) {
				uIdx := uIndices[off]
				if int(uIdx) < len(uPrioDeltas) {
					prio += int(uPrioDeltas[uIdx])
				}
			}
			state = rawNext
			step := 1
			if byteVal >= 0x80 && (rawNext&ir.WarpStateFlag) != 0 {
				step += ir.GetTrailingByteCount(byteVal)
			}
			if step > 1 {
				mc.appendWarp(state&ir.StateIDMask, step-1)
			}
			i += step
			mc.appendRaw(state & ir.StateIDMask)

			sidx = state & ir.StateIDMask
			if (state & ir.AcceptingStateFlag) != 0 {
				req := guards[sidx]
				if req == 0 || (ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
					p := prio + d.MatchPriority(sidx)
					if p < bestPriority {
						bestPriority, bestStart, bestEnd = p, restartBase, i
						if d.IsBestMatch(sidx) {
							return bestStart, bestEnd, bestPriority
						}
					} else if p == bestPriority && i > bestEnd {
						bestEnd = i
					}
				}
			}
		}

		if bestStart >= 0 {
			return bestStart, bestEnd, bestPriority
		}
		if anchorStart {
			break
		}
	}

	return bestStart, bestEnd, bestPriority
}
