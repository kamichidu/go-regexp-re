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
	state := matchState
	if len(trans) > 0 {
		_ = trans[len(trans)-1]
	}
	if len(guards) > 0 {
		_ = guards[len(guards)-1]
	}

	i := 0
	restartBase := i
	ccWarps := d.CCWarpTable()

	// Initial match check at start
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

	for i <= numBytes {
		sidx = state & ir.StateIDMask

		if !anchorStart && bestStart < 0 && sidx == (matchState&ir.StateIDMask) {
			oldI := i
			if len(re.prefix) > 0 {
				pos := bytes.Index(b[i:], re.prefix)
				if pos < 0 {
					i = numBytes
				} else {
					i += pos
				}
			} else if re.searchWarp.Kernel != ir.CCWarpNone && i < numBytes {
				info := re.searchWarp
				switch info.Kernel {
				case ir.CCWarpEqual:
					pos := bytes.IndexByte(b[i:], byte(info.V0))
					if pos < 0 {
						i = numBytes
					} else {
						i += pos
					}
				case ir.CCWarpSingleRange:
					low, high := byte(info.V0), byte(info.V1)
					for i < numBytes && (b[i] < low || b[i] > high) {
						i++
					}
				case ir.CCWarpNotSingleRange:
					low, high := info.V0, info.V1
					for i < numBytes && (b[i] >= byte(low) && b[i] <= byte(high)) {
						i++
					}
				}
			}
			if i > oldI {
				restartBase = i
				// Re-check initial state match at the NEW search start
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
						}
					}
				}
			}
		}

		if i >= numBytes {
			break
		}

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
				// CCWarp might skip over accepting points.
				// But CCWarp is restricted to self-loops with no tag/priority changes.
				// So if the state is accepting, it remains accepting with same priority.
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
		if rawNext != ir.InvalidState {
			if (rawNext & ir.AnchorVerifyFlag) != 0 {
				req := syntax.EmptyOp((rawNext & ir.AnchorMask) >> 22)
				if !(ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
					rawNext = ir.InvalidState
				}
			}
			if rawNext != ir.InvalidState {
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
				continue
			}
		}

		if bestStart >= 0 {
			return bestStart, bestEnd, bestPriority
		}
		if anchorStart {
			return -1, -1, 0
		}
		i++
		restartBase = i
		state = matchState
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
	uIndices := re.uIndices
	uPrioDeltas := re.uPrioDeltas
	matchState := re.matchState
	anchorStart := re.anchorStart

	bestStart, bestEnd, bestPriority := -1, -1, 1<<60-1
	state, prio := matchState, 0
	if len(trans) > 0 {
		_ = trans[len(trans)-1]
	}
	if len(guards) > 0 {
		_ = guards[len(guards)-1]
	}

	i := 0
	restartBase := i

	// Initial match check
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

	for i <= numBytes {
		sidx = state & ir.StateIDMask

		if !anchorStart && bestStart < 0 && sidx == (matchState&ir.StateIDMask) {
			oldI := i
			if len(re.prefix) > 0 {
				pos := bytes.Index(b[i:], re.prefix)
				if pos < 0 {
					i = numBytes
				} else {
					i += pos
				}
			} else if re.searchWarp.Kernel != ir.CCWarpNone && i < numBytes {
				info := re.searchWarp
				switch info.Kernel {
				case ir.CCWarpEqual:
					pos := bytes.IndexByte(b[i:], byte(info.V0))
					if pos < 0 {
						i = numBytes
					} else {
						i += pos
					}
				}
			}
			if i > oldI {
				restartBase = i
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
						}
					}
				}
			}
		}

		if i >= numBytes {
			break
		}

		byteVal := b[i]
		off := (int(sidx) << 8) | int(byteVal)
		rawNext := trans[off]
		if rawNext != ir.InvalidState {
			if (rawNext & ir.AnchorVerifyFlag) != 0 {
				req := syntax.EmptyOp((rawNext & ir.AnchorMask) >> 22)
				if !(ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
					rawNext = ir.InvalidState
				}
			}
			if rawNext != ir.InvalidState {
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
				continue
			}
		}

		if bestStart >= 0 {
			return bestStart, bestEnd, bestPriority
		}
		if anchorStart {
			return -1, -1, 0
		}
		i++
		restartBase = i
		state = matchState
		prio = 0
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
	matchState := re.matchState
	anchorStart := re.anchorStart

	bestStart, bestEnd, bestPriority := -1, -1, 1<<60-1
	state, prio := matchState, 0
	if len(trans) > 0 {
		_ = trans[len(trans)-1]
	}
	if len(guards) > 0 {
		_ = guards[len(guards)-1]
	}

	i := 0
	restartBase := i
	mc.appendRaw(state & ir.StateIDMask)

	// Initial match check
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

	for i <= numBytes {
		sidx = state & ir.StateIDMask

		if !anchorStart && bestStart < 0 && sidx == (matchState&ir.StateIDMask) {
			oldI := i
			if len(re.prefix) > 0 {
				pos := bytes.Index(b[i:], re.prefix)
				if pos < 0 {
					mc.appendWarp(sidx, numBytes-i)
					i = numBytes
				} else {
					if pos > 0 {
						mc.appendWarp(sidx, pos)
					}
					i += pos
				}
			} else if re.searchWarp.Kernel != ir.CCWarpNone && i < numBytes {
				info := re.searchWarp
				switch info.Kernel {
				case ir.CCWarpEqual:
					pos := bytes.IndexByte(b[i:], byte(info.V0))
					if pos < 0 {
						mc.appendWarp(sidx, numBytes-i)
						i = numBytes
					} else {
						if pos > 0 {
							mc.appendWarp(sidx, pos)
						}
						i += pos
					}
				}
			}
			if i > oldI {
				restartBase = i
				mc.clearHistory()
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
						}
					}
				}
			}
		}

		if i >= numBytes {
			break
		}

		byteVal := b[i]
		off := (int(sidx) << 8) | int(byteVal)
		rawNext := trans[off]
		if rawNext != ir.InvalidState {
			if (rawNext & ir.AnchorVerifyFlag) != 0 {
				req := syntax.EmptyOp((rawNext & ir.AnchorMask) >> 22)
				if !(ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
					rawNext = ir.InvalidState
				}
			}
			if rawNext != ir.InvalidState {
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
				continue
			}
		}

		if bestStart >= 0 {
			return bestStart, bestEnd, bestPriority
		}
		if anchorStart {
			return -1, -1, 0
		}
		i++
		restartBase = i
		state = matchState
		prio = 0
		mc.clearHistory()
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
				}
			}
		}
	}

	return bestStart, bestEnd, bestPriority
}
