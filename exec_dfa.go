package regexp

import (
	"bytes"

	"github.com/kamichidu/go-regexp-re/internal/ir"
	"github.com/kamichidu/go-regexp-re/syntax"
)

func anchoredRecordingLoop(re *Regexp, in *ir.Input, mc *matchContext, start, end int) int {
	d := re.dfa
	trans := d.Transitions()
	uIndices := re.uIndices
	uPrioDeltas := re.uPrioDeltas
	b := in.B
	matchState := re.matchState
	ccWarps := d.CCWarpTable()

	mc.resetForRecording(start, end)

	state, prio := matchState, 0
	i := start

	for {
		sidx := state & ir.StateIDMask
		mc.appendRaw(sidx)

		if i >= end {
			break
		}

		if (state & ir.CCWarpFlag) != 0 {
			info := ccWarps[sidx]
			skipped := ir.Warp(info, b[i:end])
			if skipped > 0 {
				mc.appendWarp(sidx, skipped)
				i += skipped
				continue
			}
		}

		byteVal := b[i]
		off := (int(sidx) << 8) | int(byteVal)
		rawNext := trans[off]

		// In anchored recording, we expect to follow a valid path to 'end'
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
	}

	return prio + d.MatchPriority(state&ir.StateIDMask)
}

func fastDiscoveryLoop(re *Regexp, in *ir.Input) (int, int, int) {
	d := re.dfa
	trans := d.Transitions()
	guards := d.AcceptingGuards()
	uIndices := re.uIndices
	uPrioDeltas := re.uPrioDeltas
	b := in.B
	numBytes := len(b)
	matchState := re.matchState
	anchorStart := re.anchorStart
	ccWarps := d.CCWarpTable()

	bestStart, bestEnd, bestPriority := -1, -1, 1<<30-1

	// Pass 1: High-Speed Discovery.
	// We iterate through potential start positions using SearchWarp/SIMD skips.
	for restartBase := 0; restartBase <= numBytes; restartBase++ {
		i := restartBase
		state, prio := matchState, 0

		// Pass 1.1: MAP (Multi-Point Anchor) Skip
		if !anchorStart && bestStart < 0 && (matchState&ir.AcceptingStateFlag) == 0 && i < numBytes {
			if re.primaryAnchor != nil {
				anchor := re.primaryAnchor
				var pos int = -1
				if !anchor.HasClass {
					pos = bytes.Index(b[i:], anchor.Anchor)
				} else {
					if anchor.Class.IndexAny != "" {
						pos = bytes.IndexAny(b[i:], anchor.Class.IndexAny)
					} else {
						pos = ir.IndexClass(anchor.Class, b[i:])
					}
				}
				if pos < 0 {
					break
				}

				if anchor.IsFixed {
					candidateStart := i + pos - anchor.Distance
					if candidateStart < i {
						i = i + pos + 1
						restartBase = i - 1
						continue
					}
					if candidateStart >= numBytes {
						break
					}

					// Pass 0: Pre-validation
					if _, ok := anchor.Validate(b, i+pos); !ok {
						i = i + pos + 1
						restartBase = i - 1
						continue
					}

					restartBase = candidateStart
					i = restartBase
				} else {
					// Variable distance anchor: we must start from restartBase
					// but we know a match is only possible if this anchor exists.
				}
			} else if len(re.prefix) > 0 {
				pos := bytes.Index(b[i:], re.prefix)
				if pos < 0 {
					break
				}
				restartBase += pos
				i = restartBase
			} else if re.searchWarp.Kernel != ir.CCWarpNone {
				info := re.searchWarp
				var pos int = -1
				if info.IndexAny != "" {
					pos = bytes.IndexAny(b[i:], info.IndexAny)
				} else {
					pos = ir.IndexClass(info, b[i:])
				}
				if pos < 0 {
					break
				}
				restartBase += pos
				i = restartBase
			}
		}

		currentBestEnd := -1
		currentBestPrio := 1<<30 - 1

		// Pass 1.5: Leftmost-Longest Validation.
		// From each candidate start, perform an anchored scan to find the best match.
		if (state & ir.AcceptingStateFlag) != 0 {
			sidx := state & ir.StateIDMask
			req := guards[sidx]
			if req == 0 || (ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
				currentBestEnd = i
				currentBestPrio = prio + d.MatchPriority(sidx)
			}
			if currentBestEnd >= 0 && d.IsBestMatch(state) && prio == 0 {
				return restartBase, currentBestEnd, currentBestPrio
			}
		}

		for i < numBytes {
			sidx := state & ir.StateIDMask

			if (state & ir.CCWarpFlag) != 0 {
				info := ccWarps[sidx]
				skipped := ir.Warp(info, b[i:])
				if skipped > 0 {
					i += skipped
					if (state & ir.AcceptingStateFlag) != 0 {
						req := guards[sidx]
						if req == 0 || (ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
							currentBestEnd = i
							currentBestPrio = prio + d.MatchPriority(sidx)
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

			if (state & ir.AcceptingStateFlag) != 0 {
				sidx = state & ir.StateIDMask
				req := guards[sidx]
				if req == 0 || (ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
					p := prio + d.MatchPriority(sidx)
					if p <= currentBestPrio {
						currentBestEnd = i
						currentBestPrio = p
					}
				}
				if currentBestEnd >= 0 && d.IsBestMatch(state) && prio == 0 {
					// Unbeatable match found
					return restartBase, currentBestEnd, currentBestPrio
				}
			}
		}

		if currentBestEnd >= 0 {
			if currentBestPrio < bestPriority {
				bestStart, bestEnd, bestPriority = restartBase, currentBestEnd, currentBestPrio
			}
			// Since we found a match at restartBase, any match starting at restartBase+1
			// would be lower priority (Go's leftmost-first).
			return bestStart, bestEnd, bestPriority
		}
		if anchorStart {
			break
		}
	}
	return bestStart, bestEnd, bestPriority
}

func fastMatchExecLoop(re *Regexp, in *ir.Input) (int, int, int) {
	d := re.dfa
	trans := d.Transitions()
	guards := d.AcceptingGuards()
	b := in.B
	numBytes := len(b)
	matchState := re.matchState
	anchorStart := re.anchorStart

	bestStart, bestEnd, bestPriority := -1, -1, 1<<30-1
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

		// 1. MAP (Multi-Point Anchor) Skip
		if !anchorStart && bestStart < 0 && (matchState&ir.AcceptingStateFlag) == 0 && i < numBytes {
			if re.primaryAnchor != nil {
				anchor := re.primaryAnchor
				var pos int = -1
				if !anchor.HasClass {
					pos = bytes.Index(b[i:], anchor.Anchor)
				} else {
					if anchor.Class.IndexAny != "" {
						pos = bytes.IndexAny(b[i:], anchor.Class.IndexAny)
					} else {
						pos = ir.IndexClass(anchor.Class, b[i:])
					}
				}
				if pos < 0 {
					break
				}

				if anchor.IsFixed {
					candidateStart := i + pos - anchor.Distance
					if candidateStart < i {
						i = i + pos + 1
						restartBase = i - 1
						continue
					}
					if candidateStart >= numBytes {
						break
					}

					// Pass 0: Pre-validation
					if _, ok := anchor.Validate(b, i+pos); !ok {
						i = i + pos + 1
						restartBase = i - 1
						continue
					}

					restartBase = candidateStart
					i = restartBase
				} else {
					// Variable distance anchor: we must start from restartBase
					// but we know a match is only possible if this anchor exists.
				}
			} else if len(re.prefix) > 0 {
				pos := bytes.Index(b[i:], re.prefix)
				if pos < 0 {
					break
				}
				restartBase += pos
				i = restartBase
			} else if re.searchWarp.Kernel != ir.CCWarpNone {
				info := re.searchWarp
				var pos int = -1
				if info.IndexAny != "" {
					pos = bytes.IndexAny(b[i:], info.IndexAny)
				} else {
					pos = ir.IndexClass(info, b[i:])
				}
				if pos < 0 {
					break
				}
				restartBase += pos
				i = restartBase
			}
		}

		currentBestEnd := -1

		if (state & ir.AcceptingStateFlag) != 0 {
			sidx := state & ir.StateIDMask
			req := guards[sidx]
			if req == 0 || (ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
				currentBestEnd = i
			}
		}

		for i < numBytes {
			sidx := state & ir.StateIDMask

			if (state & ir.CCWarpFlag) != 0 {
				info := ccWarps[sidx]
				skipped := ir.Warp(info, b[i:])
				if skipped > 0 {
					i += skipped
					if (state & ir.AcceptingStateFlag) != 0 {
						req := guards[sidx]
						if req == 0 || (ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
							currentBestEnd = i
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

			if (state & ir.AcceptingStateFlag) != 0 {
				sidx = state & ir.StateIDMask
				req := guards[sidx]
				if req == 0 || (ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
					currentBestEnd = i
				}
			}
		}

		if currentBestEnd >= 0 {
			return restartBase, currentBestEnd, 0
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
	uIndices := re.uIndices
	uPrioDeltas := re.uPrioDeltas
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
		state, prio := matchState, 0

		if !anchorStart && bestStart < 0 && (matchState&ir.AcceptingStateFlag) == 0 && i < numBytes {
			if len(re.prefix) > 0 {
				pos := bytes.Index(b[i:], re.prefix)
				if pos < 0 {
					break
				}
				restartBase += pos
				i = restartBase
			} else if re.searchWarp.Kernel != ir.CCWarpNone {
				info := re.searchWarp
				var pos int = -1
				if info.IndexAny != "" {
					pos = bytes.IndexAny(b[i:], info.IndexAny)
				} else {
					pos = ir.IndexClass(info, b[i:])
				}
				if pos < 0 {
					break
				}
				restartBase += pos
				i = restartBase
			}
		}

		currentBestEnd := -1
		currentBestPrio := int64(1<<60 - 1)

		if (state & ir.AcceptingStateFlag) != 0 {
			sidx := state & ir.StateIDMask
			req := guards[sidx]
			if req == 0 || (ir.VerifyEnd(&in, i, req) && ir.VerifyBegin(&in, i, req) && ir.VerifyWord(&in, i, req)) {
				currentBestEnd = i
				currentBestPrio = int64(prio) + int64(d.MatchPriority(sidx))
			}
		}

		for i < numBytes {
			sidx := state & ir.StateIDMask

			if (state & ir.CCWarpFlag) != 0 {
				info := ccWarps[sidx]
				skipped := ir.Warp(info, b[i:])
				if skipped > 0 {
					i += skipped
					if (state & ir.AcceptingStateFlag) != 0 {
						req := guards[sidx]
						if req == 0 || (ir.VerifyEnd(&in, i, req) && ir.VerifyBegin(&in, i, req) && ir.VerifyWord(&in, i, req)) {
							currentBestEnd = i
							currentBestPrio = int64(prio) + int64(d.MatchPriority(sidx))
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
				if !(ir.VerifyEnd(&in, i, req) && ir.VerifyBegin(&in, i, req) && ir.VerifyWord(&in, i, req)) {
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

			if (state & ir.AcceptingStateFlag) != 0 {
				sidx = state & ir.StateIDMask
				req := guards[sidx]
				if req == 0 || (ir.VerifyEnd(&in, i, req) && ir.VerifyBegin(&in, i, req) && ir.VerifyWord(&in, i, req)) {
					p := int64(prio) + int64(d.MatchPriority(sidx))
					if p <= currentBestPrio {
						currentBestEnd = i
						currentBestPrio = p
					}
				}
			}
		}

		if currentBestEnd >= 0 {
			return restartBase, currentBestEnd, int(currentBestPrio)
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
	uIndices := re.uIndices
	uPrioDeltas := re.uPrioDeltas
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
		state, prio := matchState, 0

		if !anchorStart && bestStart < 0 && (matchState&ir.AcceptingStateFlag) == 0 && i < numBytes {
			if len(re.prefix) > 0 {
				pos := bytes.Index(b[i:], re.prefix)
				if pos < 0 {
					break
				}
				restartBase += pos
				i = restartBase
			} else if re.searchWarp.Kernel != ir.CCWarpNone {
				info := re.searchWarp
				var pos int = -1
				if info.IndexAny != "" {
					pos = bytes.IndexAny(b[i:], info.IndexAny)
				} else {
					pos = ir.IndexClass(info, b[i:])
				}
				if pos < 0 {
					break
				}
				restartBase += pos
				i = restartBase
			}
		}

		currentBestEnd := -1
		currentBestPrio := int64(1<<60 - 1)
		mc.clearHistory()

		if (state & ir.AcceptingStateFlag) != 0 {
			sidx := state & ir.StateIDMask
			req := guards[sidx]
			if req == 0 || (ir.VerifyEnd(&in, i, req) && ir.VerifyBegin(&in, i, req) && ir.VerifyWord(&in, i, req)) {
				currentBestEnd = i
				currentBestPrio = int64(prio) + int64(d.MatchPriority(sidx))
			}
		}

		for {
			sidx := state & ir.StateIDMask
			mc.appendRaw(sidx)

			if i >= numBytes {
				break
			}

			if (state & ir.CCWarpFlag) != 0 {
				info := ccWarps[sidx]
				skipped := ir.Warp(info, b[i:])
				if skipped > 0 {
					mc.appendWarp(sidx, skipped)
					i += skipped
					if (state & ir.AcceptingStateFlag) != 0 {
						req := guards[sidx]
						if req == 0 || (ir.VerifyEnd(&in, i, req) && ir.VerifyBegin(&in, i, req) && ir.VerifyWord(&in, i, req)) {
							currentBestEnd = i
							currentBestPrio = int64(prio) + int64(d.MatchPriority(sidx))
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
				if !(ir.VerifyEnd(&in, i, req) && ir.VerifyBegin(&in, i, req) && ir.VerifyWord(&in, i, req)) {
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

			if (state & ir.AcceptingStateFlag) != 0 {
				sidx = state & ir.StateIDMask
				req := guards[sidx]
				if req == 0 || (ir.VerifyEnd(&in, i, req) && ir.VerifyBegin(&in, i, req) && ir.VerifyWord(&in, i, req)) {
					p := int64(prio) + int64(d.MatchPriority(sidx))
					if p <= currentBestPrio {
						currentBestEnd = i
						currentBestPrio = p
					}
				}
			}
		}

		if currentBestEnd >= 0 {
			// TRICKY: The history must be correct for currentBestEnd.
			// Since we record BEFORE byte consumption, history is always fine.
			return restartBase, currentBestEnd, int(currentBestPrio)
		}
		if anchorStart {
			break
		}
	}
	return bestStart, bestEnd, bestPriority
}
