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
	for restartBase := 0; restartBase <= numBytes; restartBase++ {
		if anchorStart && restartBase > 0 {
			break
		}
		i := restartBase
		state, prio := matchState, 0

		// Pass 1.1: Unified Discovery (Iterator)
		if !anchorStart && bestStart < 0 && (matchState&ir.AcceptingStateFlag) == 0 && i < numBytes {
			var pos int = -1

			// 1. Special check for start-of-buffer augmented patterns
			if i == 0 && in.AbsPos == 0 && re.primaryAnchor != nil {
				for j := range re.primaryAnchor.Augmented {
					aug := &re.primaryAnchor.Augmented[j]
					if aug.IsStart && bytes.HasPrefix(b, aug.Pattern) {
						pos = aug.Offset
						break
					}
				}
			}

			// 1.1 Special check for end-of-buffer augmented patterns
			if pos < 0 && in.AbsPos+numBytes == in.TotalBytes && re.primaryAnchor != nil {
				for j := range re.primaryAnchor.Augmented {
					aug := &re.primaryAnchor.Augmented[j]
					if aug.IsEnd && bytes.HasSuffix(b, aug.Pattern) {
						pos = numBytes - len(aug.Pattern) + aug.Offset
						break
					}
				}
			}

			if pos < 0 {
				if re.primaryAugmented != nil {
					// 2. Augmented Pattern Iterator (e.g., "\nRare", "Rare\n")
					pos = bytes.Index(b[i:], re.primaryAugmented.Pattern)
					if pos >= 0 {
						pos += re.primaryAugmented.Offset
					}
				} else if re.primaryAnchor != nil {
					anchor := re.primaryAnchor

					// 2.1 Text-Boundary Optimization: If anchored to ^, only check start
					if anchor.HasBeginText {
						if in.AbsPos+i == 0 {
							// Check if the anchor literal matches at the required distance
							matched := false
							start := anchor.Distance
							end := start + len(anchor.Anchor)
							if anchor.HasClass {
								end = start + 1
							}
							if end <= numBytes {
								if !anchor.HasClass {
									if bytes.Equal(b[start:end], anchor.Anchor) {
										matched = true
									}
								} else {
									if ir.ValidateFixed(anchor.Class, b[start:end]) {
										matched = true
									}
								}
							}
							if matched {
								pos = anchor.Distance
							} else {
								// Impossible to match
								return -1, -1, 1<<30 - 1
							}
						} else {
							// Already past the absolute start
							return -1, -1, 1<<30 - 1
						}
					} else if anchor.Type == ir.AnchorSuffix && (anchor.HasEndText || anchor.HasEndLine) {
						// 3. Suffix-First Strategy: find the boundary (EOF or \n) first.
						boundary := -1
						isEOF := false
						if anchor.HasEndText {
							boundary = numBytes
							isEOF = true
						} else {
							nextNL := bytes.IndexByte(b[i:], '\n')
							if nextNL >= 0 {
								boundary = i + nextNL
							} else {
								boundary = numBytes
								isEOF = true
							}
						}

						anchorLen := len(anchor.Anchor)
						if anchor.HasClass {
							anchorLen = 1
						}

						pos = (boundary - i) - anchorLen - anchor.MinDistToLineEnd
						if pos < 0 {
							if isEOF {
								break
							}
							restartBase = boundary
							continue
						}

						// Verify anchor content
						matched := false
						if !anchor.HasClass {
							if bytes.Equal(b[i+pos:i+pos+anchorLen], anchor.Anchor) {
								matched = true
							}
						} else {
							if ir.ValidateFixed(anchor.Class, b[i+pos:i+pos+anchorLen]) {
								matched = true
							}
						}

						if !matched {
							if isEOF {
								break
							}
							restartBase = boundary
							continue
						}
					} else {
						// 4. Standard Pivot/Prefix Search
						if !anchor.HasClass {
							pos = bytes.Index(b[i:], anchor.Anchor)
						} else {
							if anchor.Class.IndexAny != "" {
								pos = bytes.IndexAny(b[i:], anchor.Class.IndexAny)
							} else {
								pos = ir.IndexClass(anchor.Class, b[i:])
							}
						}
					}
				} else if len(re.prefix) > 0 {
					pos = bytes.Index(b[i:], re.prefix)
				} else if re.searchWarp.Kernel != ir.CCWarpNone {
					if re.searchWarp.IndexAny != "" {
						pos = bytes.IndexAny(b[i:], re.searchWarp.IndexAny)
					} else {
						pos = ir.IndexClass(re.searchWarp, b[i:])
					}
				}
			}

			if pos < 0 {
				break
			}

			// Pass 1.2: Lightweight Unified Propagator (Verification)
			if re.primaryAnchor != nil {
				anchor := re.primaryAnchor
				absPos := i + pos

				// 1. O(1) Boundary Constraints
				if anchor.HasBeginText && (in.AbsPos+absPos != 0) {
					return -1, -1, 1<<30 - 1
				}
				if anchor.HasBeginLine && re.primaryAugmented == nil {
					// Redundant if primaryAugmented was used (since it includes \n)
					if absPos > 0 {
						if b[absPos-1] != '\n' {
							i = absPos + 1
							restartBase = i - 1
							continue
						}
					} else if in.AbsPos > 0 {
						i = absPos + 1
						restartBase = i - 1
						continue
					}
				}
				if anchor.HasEndText {
					remaining := in.TotalBytes - (in.AbsPos + absPos)
					if remaining < anchor.MinDistToEnd || remaining > anchor.MaxDistToEnd {
						i = absPos + 1
						restartBase = i - 1
						continue
					}
				}
				if anchor.HasEndLine && re.primaryAugmented == nil {
					// Simplified check: only check range if anchor is NOT Augmented with \n
					startOffset := anchor.MinDistToLineEnd
					endOffset := anchor.MaxDistToLineEnd
					found := false
					cStart := absPos + startOffset
					cEnd := absPos + endOffset
					if cEnd >= numBytes {
						cEnd = numBytes - 1
						found = true // EOF
					}
					if !found && cStart < numBytes {
						// Only IndexByte if the range is large, otherwise direct compare
						if cEnd-cStart < 4 {
							for k := cStart; k <= cEnd; k++ {
								if b[k] == '\n' {
									found = true
									break
								}
							}
						} else {
							if bytes.IndexByte(b[cStart:cEnd+1], '\n') >= 0 {
								found = true
							}
						}
					}
					if !found {
						i = absPos + 1
						restartBase = i - 1
						continue
					}
				}

				// 2. O(1) Literal Pruning (SimpleBackward)
				pruned := false
				for _, c := range anchor.SimpleBackward {
					idx := absPos + c.Offset
					if idx < 0 || b[idx] != byte(c.Info.V0) {
						pruned = true
						break
					}
				}
				if pruned {
					i = absPos + 1
					restartBase = i - 1
					continue
				}

				// 3. Fixed Offset Alignment & Final Check before DFA
				if anchor.IsFixed {
					candidateStart := absPos - anchor.Distance
					if candidateStart < i {
						i = absPos + 1
						restartBase = i - 1
						continue
					}
					if candidateStart >= numBytes {
						break
					}

					// EARLY HANDOVER: If we made it here, just start the DFA.
					// Exhaustive verification with anchor.Validate is too slow in Pass 0.
					restartBase = candidateStart
					i = restartBase
				} else {
					restartBase = absPos
					i = restartBase
				}
			} else {
				restartBase += pos
				i = restartBase
			}
		}

		currentBestEnd := -1
		currentBestPrio := 1<<30 - 1

		if (state & ir.AcceptingStateFlag) != 0 {
			sidx := state & ir.StateIDMask
			req := guards[sidx]
			if req == 0 || (ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
				currentBestEnd = i
				currentBestPrio = prio + int(d.MatchPriority(sidx))
			}
		}

		for i < numBytes {
			byteVal := b[i]

			// CCWarp Optimization
			if (state & ir.CCWarpFlag) != 0 {
				info := ccWarps[state&ir.StateIDMask]
				skipped := ir.Warp(info, b[i:])
				if skipped > 0 {
					i += skipped
					state &= ^ir.CCWarpFlag
					continue
				}
			}

			off := (int(state&ir.StateIDMask) << 8) | int(byteVal)
			rawNext := trans[off]

			if rawNext == ir.InvalidState {
				break
			}

			if (rawNext & ir.TaggedStateFlag) != 0 {
				uIdx := uIndices[off]
				prio += int(uPrioDeltas[uIdx])
			}

			state = rawNext
			i++

			if (state & ir.AcceptingStateFlag) != 0 {
				sidx := state & ir.StateIDMask
				req := guards[sidx]
				if req == 0 || (ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
					currentBestEnd = i
					currentBestPrio = prio + int(d.MatchPriority(sidx))
				}
			}
		}

		if currentBestEnd >= 0 {
			if restartBase < bestStart || (restartBase == bestStart && currentBestEnd > bestEnd) || (restartBase == bestStart && currentBestEnd == bestEnd && currentBestPrio < bestPriority) {
				bestStart, bestEnd, bestPriority = restartBase, currentBestEnd, currentBestPrio
			}
			return bestStart, bestEnd, bestPriority
		}
	}

	return bestStart, bestEnd, bestPriority
}

func fastMatchExecLoop(re *Regexp, in *ir.Input) (int, int, int) {
	return fastDiscoveryLoop(re, in)
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

	bestStart, bestEnd, bestPriority := -1, -1, int64(1<<60-1)
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

		// Simple prefix/warp skip for extended loop (can be improved similarly to fastDiscoveryLoop)
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
			return restartBase, currentBestEnd, int(currentBestPrio)
		}
		if anchorStart {
			break
		}
	}

	return bestStart, bestEnd, int(bestPriority)
}
