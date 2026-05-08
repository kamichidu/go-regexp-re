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
			info := &ccWarps[sidx]
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

		if rawNext == ir.InvalidState {
			break
		}

		if (rawNext & ir.AnchorVerifyFlag) != 0 {
			req := syntax.EmptyOp((rawNext & ir.AnchorMask) >> 22)
			if req != 0 && !ir.Verify(in, i, req) {
				break
			}
		}

		if (rawNext&ir.TaggedStateFlag) != 0 && int(off) < len(uIndices) {
			uIdx := uIndices[off]
			if uIdx != 0xFFFFFFFF && int(uIdx) < len(uPrioDeltas) {
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
	strategy := d.SearchStrategy()

	bestStart, bestEnd, bestPriority := -1, -1, 1<<30-1

	for restartBase := 0; restartBase <= numBytes; restartBase++ {
		i := restartBase
		state, prio := matchState, 0

		if !anchorStart && bestStart < 0 && (matchState&ir.AcceptingStateFlag) == 0 && i < numBytes {
			switch strategy {
			case ir.SearchStrategyLiteral:
				pos := bytes.Index(b[i:], re.primaryAnchor.Anchor)
				if pos >= 0 {
					restartBase += pos
					i = restartBase
				} else {
					return -1, -1, 1<<30 - 1
				}
			case ir.SearchStrategySearchWarp:
				pos := ir.IndexClass(&re.searchWarp, b[i:])
				if pos >= 0 {
					restartBase += pos
					i = restartBase
				} else {
					return -1, -1, 1<<30 - 1
				}
			case ir.SearchStrategySDFA:
				sd := d.SearchDFA()
				foundSDFA := false
				currI := i
				for currI < numBytes {
					idx := ir.IndexClass(&sd.Trigger, b[currI:])
					if idx < 0 {
						break
					}
					currI += idx

					st := sd.StartState
					tempI := currI
					found := false
					for tempI < numBytes {
						st = sd.Transitions[(uint16(st)<<8)|uint16(b[tempI])]
						if st == sd.DeadState {
							break
						}
						if sd.Accepting[st] {
							found = true
							break
						}
						tempI++
					}

					if found {
						restartBase = currI
						i = restartBase
						foundSDFA = true
						break
					}
					currI++
				}
				if !foundSDFA {
					return -1, -1, 1<<30 - 1
				}
			default:
				if len(re.searchAny) > 0 {
					var pos int = -1
					if len(re.searchAny) == 1 {
						pos = bytes.IndexByte(b[i:], re.searchAny[0])
					} else {
						for _, target := range re.searchAny {
							p := bytes.IndexByte(b[i:], target)
							if p >= 0 && (pos < 0 || p < pos) {
								pos = p
							}
						}
					}
					if pos >= 0 {
						restartBase += pos
						i = restartBase
					} else {
						return -1, -1, 1<<30 - 1
					}
				} else if len(re.prefix) > 0 {
					pos := bytes.Index(b[i:], re.prefix)
					if pos >= 0 {
						restartBase += pos
						i = restartBase
					} else {
						return -1, -1, 1<<30 - 1
					}
				}
			}
		}

		currentBestEnd := -1
		currentBestPrio := 1<<30 - 1

		if (state & ir.AcceptingStateFlag) != 0 {
			sidx := state & ir.StateIDMask
			req := guards[sidx]
			if req == 0 || ir.Verify(in, i, req) {
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
				info := &ccWarps[sidx]
				skipped := ir.Warp(info, b[i:])
				if skipped > 0 {
					i += skipped
					if (state & ir.AcceptingStateFlag) != 0 {
						req := guards[sidx]
						if req == 0 || ir.Verify(in, i, req) {
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
				if req != 0 && !ir.Verify(in, i, req) {
					break
				}
			}

			if (rawNext&ir.TaggedStateFlag) != 0 && int(off) < len(uIndices) {
				uIdx := uIndices[off]
				if uIdx != 0xFFFFFFFF && int(uIdx) < len(uPrioDeltas) {
					prio += int(uPrioDeltas[uIdx])
				}
			}

			state = rawNext
			i++
			if byteVal >= 0x80 && (state&ir.WarpStateFlag) != 0 {
				i += ir.GetTrailingByteCount(byteVal)
			}

			if (state & ir.AcceptingStateFlag) != 0 {
				nsidx := state & ir.StateIDMask
				req := guards[nsidx]
				if req == 0 || ir.Verify(in, i, req) {
					p := prio + d.MatchPriority(nsidx)
					if p <= currentBestPrio {
						currentBestEnd = i
						currentBestPrio = p
					}
				}
				if currentBestEnd >= 0 && d.IsBestMatch(state) && prio == 0 {
					return restartBase, currentBestEnd, currentBestPrio
				}
			}
		}

		if currentBestEnd >= 0 {
			if currentBestPrio < bestPriority {
				bestStart, bestEnd, bestPriority = restartBase, currentBestEnd, currentBestPrio
			}
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
	ccWarps := d.CCWarpTable()
	strategy := d.SearchStrategy()

	if len(trans) > 0 {
		_ = trans[len(trans)-1]
	}
	if len(guards) > 0 {
		_ = guards[len(guards)-1]
	}
	if len(b) > 0 {
		_ = b[len(b)-1]
	}

	bestStart, bestEnd, bestPriority := -1, -1, 1<<30-1
	for restartBase := 0; restartBase <= numBytes; restartBase++ {
		i := restartBase
		state := matchState

		if !anchorStart && bestStart < 0 && (matchState&ir.AcceptingStateFlag) == 0 && i < numBytes {
			switch strategy {
			case ir.SearchStrategyLiteral:
				pos := bytes.Index(b[i:], re.primaryAnchor.Anchor)
				if pos >= 0 {
					restartBase += pos
					i = restartBase
				} else {
					return -1, -1, 1<<30 - 1
				}
			case ir.SearchStrategySearchWarp:
				pos := ir.IndexClass(&re.searchWarp, b[i:])
				if pos >= 0 {
					restartBase += pos
					i = restartBase
				} else {
					return -1, -1, 1<<30 - 1
				}
			case ir.SearchStrategySDFA:
				sd := d.SearchDFA()
				foundSDFA := false
				currI := i
				for currI < numBytes {
					idx := ir.IndexClass(&sd.Trigger, b[currI:])
					if idx < 0 {
						break
					}
					currI += idx
					st := sd.StartState
					tempI := currI
					found := false
					for tempI < numBytes {
						st = sd.Transitions[(uint16(st)<<8)|uint16(b[tempI])]
						if st == sd.DeadState {
							break
						}
						if sd.Accepting[st] {
							found = true
							break
						}
						tempI++
					}
					if found {
						restartBase = currI
						i = restartBase
						foundSDFA = true
						break
					}
					currI++
				}
				if !foundSDFA {
					return -1, -1, 1<<30 - 1
				}
			default:
				if len(re.searchAny) > 0 {
					var pos int = -1
					if len(re.searchAny) == 1 {
						pos = bytes.IndexByte(b[i:], re.searchAny[0])
					} else {
						for _, target := range re.searchAny {
							p := bytes.IndexByte(b[i:], target)
							if p >= 0 && (pos < 0 || p < pos) {
								pos = p
							}
						}
					}
					if pos >= 0 {
						restartBase += pos
						i = restartBase
					} else {
						return -1, -1, 1<<30 - 1
					}
				} else if len(re.prefix) > 0 {
					pos := bytes.Index(b[i:], re.prefix)
					if pos >= 0 {
						restartBase += pos
						i = restartBase
					} else {
						return -1, -1, 1<<30 - 1
					}
				}
			}
		}

		currentBestEnd := -1
		if (state & ir.AcceptingStateFlag) != 0 {
			sidx := state & ir.StateIDMask
			req := guards[sidx]
			if req == 0 || ir.Verify(in, i, req) {
				currentBestEnd = i
			}
		}

		for i < numBytes {
			sidx := state & ir.StateIDMask
			if (state & ir.CCWarpFlag) != 0 {
				info := &ccWarps[sidx]
				skipped := ir.Warp(info, b[i:])
				if skipped > 0 {
					i += skipped
					if (state & ir.AcceptingStateFlag) != 0 {
						req := guards[sidx]
						if req == 0 || ir.Verify(in, i, req) {
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

			state = rawNext
			i++
			if byteVal >= 0x80 && (state&ir.WarpStateFlag) != 0 {
				i += ir.GetTrailingByteCount(byteVal)
			}

			if (state & ir.AcceptingStateFlag) != 0 {
				nsidx := state & ir.StateIDMask
				req := guards[nsidx]
				if req == 0 || ir.Verify(in, i, req) {
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
	ccWarps := d.CCWarpTable()
	strategy := d.SearchStrategy()

	bestStart, bestEnd, bestPriority := -1, -1, 1<<60-1

	for restartBase := 0; restartBase <= numBytes; restartBase++ {
		i := restartBase
		state, prio := matchState, 0

		if !anchorStart && bestStart < 0 && (matchState&ir.AcceptingStateFlag) == 0 && i < numBytes {
			switch strategy {
			case ir.SearchStrategyLiteral:
				pos := bytes.Index(b[i:], re.primaryAnchor.Anchor)
				if pos >= 0 {
					restartBase += pos
					i = restartBase
				} else {
					return -1, -1, 0
				}
			case ir.SearchStrategySearchWarp:
				pos := ir.IndexClass(&re.searchWarp, b[i:])
				if pos >= 0 {
					restartBase += pos
					i = restartBase
				} else {
					return -1, -1, 0
				}
			default:
				if len(re.searchAny) > 0 {
					var pos int = -1
					if len(re.searchAny) == 1 {
						pos = bytes.IndexByte(b[i:], re.searchAny[0])
					} else {
						for _, target := range re.searchAny {
							p := bytes.IndexByte(b[i:], target)
							if p >= 0 && (pos < 0 || p < pos) {
								pos = p
							}
						}
					}
					if pos >= 0 {
						restartBase += pos
						i = restartBase
					} else {
						return -1, -1, 0
					}
				} else if len(re.prefix) > 0 {
					pos := bytes.Index(b[i:], re.prefix)
					if pos >= 0 {
						restartBase += pos
						i = restartBase
					} else {
						return -1, -1, 0
					}
				}
			}
		}

		currentBestEnd := -1
		currentBestPrio := int64(1<<60 - 1)

		if (state & ir.AcceptingStateFlag) != 0 {
			sidx := state & ir.StateIDMask
			req := guards[sidx]
			if req == 0 || ir.Verify(&in, i, req) {
				currentBestEnd = i
				currentBestPrio = int64(prio) + int64(d.MatchPriority(sidx))
			}
		}

		for i < numBytes {
			sidx := state & ir.StateIDMask

			if (state & ir.CCWarpFlag) != 0 {
				info := &ccWarps[sidx]
				skipped := ir.Warp(info, b[i:])
				if skipped > 0 {
					i += skipped
					if (state & ir.AcceptingStateFlag) != 0 {
						req := guards[sidx]
						if req == 0 || ir.Verify(&in, i, req) {
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
				if req != 0 && !ir.Verify(&in, i, req) {
					break
				}
			}

			if (rawNext&ir.TaggedStateFlag) != 0 && int(off) < len(uIndices) {
				uIdx := uIndices[off]
				if uIdx != 0xFFFFFFFF && int(uIdx) < len(uPrioDeltas) {
					prio += int(uPrioDeltas[uIdx])
				}
			}

			state = rawNext
			i++
			if byteVal >= 0x80 && (rawNext&ir.WarpStateFlag) != 0 {
				i += ir.GetTrailingByteCount(byteVal)
			}

			if (state & ir.AcceptingStateFlag) != 0 {
				sidx = state & ir.StateIDMask
				req := guards[sidx]
				if req == 0 || ir.Verify(&in, i, req) {
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
	return bestStart, bestEnd, int(bestPriority)
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
	ccWarps := d.CCWarpTable()

	bestStart, bestEnd, bestPriority := -1, -1, 1<<30-1

	for restartBase := 0; restartBase <= numBytes; restartBase++ {
		if anchorStart && restartBase > 0 {
			break
		}
		i := restartBase
		state, prio := matchState, 0
		currentBestEnd := -1
		currentBestPrio := 1<<30 - 1
		mc.clearHistory()

		if (state & ir.AcceptingStateFlag) != 0 {
			sidx := state & ir.StateIDMask
			req := guards[sidx]
			if req == 0 || ir.Verify(&in, i, req) {
				currentBestEnd = i
				currentBestPrio = prio + d.MatchPriority(sidx)
			}
		}

		for {
			sidx := state & ir.StateIDMask
			mc.appendRaw(sidx)

			if i >= numBytes {
				break
			}

			if (state & ir.CCWarpFlag) != 0 {
				info := &ccWarps[sidx]
				skipped := ir.Warp(info, b[i:])
				if skipped > 0 {
					mc.appendWarp(sidx, skipped)
					i += skipped
					if (state & ir.AcceptingStateFlag) != 0 {
						req := guards[sidx]
						if req == 0 || ir.Verify(&in, i, req) {
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
				if req != 0 && !ir.Verify(&in, i, req) {
					break
				}
			}

			if (rawNext&ir.TaggedStateFlag) != 0 && int(off) < len(uIndices) {
				uIdx := uIndices[off]
				if uIdx != 0xFFFFFFFF && int(uIdx) < len(uPrioDeltas) {
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
				nsidx := state & ir.StateIDMask
				req := guards[nsidx]
				if req == 0 || ir.Verify(&in, i, req) {
					p := prio + d.MatchPriority(nsidx)
					if p <= currentBestPrio {
						currentBestEnd = i
						currentBestPrio = p
					}
				}
			}
		}

		if currentBestEnd >= 0 {
			return restartBase, currentBestEnd, currentBestPrio
		}
	}
	return bestStart, bestEnd, bestPriority
}
