package regexp

import (
	"bytes"

	"github.com/kamichidu/go-regexp-re/internal/ir"
	"github.com/kamichidu/go-regexp-re/syntax"
)

// Pass 0: Discovery
// Identifies a candidate start position (Horizon) using MAP pre-filters, Gaze verification, and Snap back-scanning.
func pass0_DiscoveryLoop(re *Regexp, in *ir.Input, searchStart int) (restartBase, matchLiteralPos int, bestAnchor *ir.AnchorInfo) {
	d := re.dfa
	b := in.B
	numBytes := len(b)
	matchState := re.matchState
	anchorStart := re.anchorStart
	strategy := d.SearchStrategy()

	// MANDATE: strictly anchored patterns must give up if past index 0
	if anchorStart && (in.AbsPos > 0 || searchStart > 0) {
		return -1, -1, nil
	}

	for i := searchStart; i <= numBytes; {
		if anchorStart && i > 0 {
			break
		}

		// Initial matchState check (for nullable patterns)
		if (matchState & ir.AcceptingStateFlag) != 0 {
			guards := d.AcceptingGuards()
			req := guards[matchState&ir.StateIDMask]
			if req == 0 || ir.Verify(in, i, req) {
				return i, i, nil
			}
		}

		candidatePos := -1
		var candidateAnchor *ir.AnchorInfo
		matchLiteralPos = -1

		// 1. Mandatory Text Anchor Jump (Prefix/Suffix with Fixed Distance)
		anchor := re.primaryAnchor
		if anchor != nil && anchor.Mandatory && anchor.IsFixed {
			if anchor.HasBeginText {
				absPos := in.AbsPos + i
				if absPos == 0 {
					if anchor.Validate(in, anchor.Distance) {
						candidatePos = 0
						matchLiteralPos = anchor.Distance
						candidateAnchor = anchor
						goto end_of_loop
					}
				}
				return -1, -1, nil
			} else if anchor.HasEndText {
				// Standard $ matches at EOF or before a trailing newline.
				posEOF := (in.TotalBytes - in.AbsPos) - len(anchor.Anchor) - anchor.MinDistToEnd
				if anchor.HasClass {
					posEOF = (in.TotalBytes - in.AbsPos) - 1 - anchor.MinDistToEnd
				}
				if posEOF >= i && anchor.Validate(in, posEOF) {
					candidatePos = posEOF - anchor.Distance
					matchLiteralPos = posEOF
					candidateAnchor = anchor
					goto end_of_loop
				}
				// Case B: Before trailing newline
				if in.TotalBytes > 0 && in.OriginalB[in.TotalBytes-1] == '\n' {
					posNL := posEOF - 1
					if posNL >= i && anchor.Validate(in, posNL) {
						candidatePos = posNL - anchor.Distance
						matchLiteralPos = posNL
						candidateAnchor = anchor
						goto end_of_loop
					}
				}
				return -1, -1, nil
			}
		}

		// 2. Main Search
		switch strategy {
		case ir.SearchStrategyLiteral:
			anchor := re.primaryAnchor
			aug := re.primaryAugmented

			// Special handling for pos 0 if using augmented pattern
			if i == 0 && in.AbsPos == 0 && aug != nil && (anchor != nil && (anchor.HasBeginLine || anchor.HasBeginText)) {
				if anchor.Validate(in, anchor.Distance) {
					candidatePos = 0
					matchLiteralPos = anchor.Distance
					candidateAnchor = anchor
					goto end_of_loop
				}
			}

			if aug != nil {
				pos := bytes.Index(b[i:], aug.Pattern)
				if pos >= 0 {
					absLitPos := i + pos + aug.Offset
					// Correct anchor for this augmented pattern
					var ownerAnchor *ir.AnchorInfo
					if aug.AnchorIdx >= 0 && aug.AnchorIdx < len(re.mapAnchors) {
						ownerAnchor = &re.mapAnchors[aug.AnchorIdx]
					} else {
						ownerAnchor = anchor
					}

					if ownerAnchor != nil && ownerAnchor.Validate(in, absLitPos) {
						candidatePos = absLitPos - ownerAnchor.Distance
						matchLiteralPos = absLitPos
						candidateAnchor = ownerAnchor
						goto end_of_loop
					}
					i = i + pos + 1
					continue
				}
				// End of Input Safety: if main search failed, check if the anchor matches at the very end
				for j := range re.mapAnchors {
					a := &re.mapAnchors[j]
					for k := range a.Augmented {
						augEnd := &a.Augmented[k]
						if augEnd.IsEnd {
							posEnd := numBytes - len(a.Anchor)
							if a.HasClass {
								posEnd = numBytes - 1
							}
							if posEnd >= i && a.Validate(in, posEnd) {
								candidatePos = posEnd - a.Distance
								matchLiteralPos = posEnd
								candidateAnchor = a
								goto end_of_loop
							}
						}
					}
				}
				return -1, -1, nil
			} else if anchor != nil {
				pos := bytes.Index(b[i:], anchor.Anchor)
				if pos >= 0 {
					absLitPos := i + pos
					if anchor.Validate(in, absLitPos) {
						candidatePos = absLitPos - anchor.Distance
						matchLiteralPos = absLitPos
						candidateAnchor = anchor
						goto end_of_loop
					}
					i = i + pos + 1
					continue
				}
				if anchor.Mandatory {
					return -1, -1, nil
				}
			}
		case ir.SearchStrategySearchWarp:
			pos := ir.IndexClass(&re.searchWarp, b[i:])
			if pos >= 0 {
				matchLiteralPos = i + pos
				candidatePos = matchLiteralPos
				goto end_of_loop
			}
			return -1, -1, nil
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

				st := sd.Transitions[(uint16(sd.StartState)<<8)|uint16(b[currI])]
				if st != sd.DeadState {
					if sd.Accepting[st] {
						candidatePos = currI
						matchLiteralPos = currI
						foundSDFA = true
						break
					}
					tempI := currI + 1
					for tempI < numBytes {
						st = sd.Transitions[(uint16(st)<<8)|uint16(b[tempI])]
						if st == sd.DeadState {
							break
						}
						if sd.Accepting[st] {
							candidatePos = currI
							matchLiteralPos = tempI
							foundSDFA = true
							break
						}
						tempI++
					}
					if foundSDFA {
						break
					}
				}
				currI++
			}
			if foundSDFA {
				goto end_of_loop
			}
			return -1, -1, nil
		default:
			candidatePos, matchLiteralPos = i, i
		}

	end_of_loop:
		if candidatePos >= 0 {
			j := candidatePos
			// 3. Candidate Snapping (Variable Distance / WarpBack)
			// If the anchor is not at fixed distance 0, we need to find the true leftmost match start.
			if candidateAnchor != nil && !candidateAnchor.IsFixed {
				j = matchLiteralPos
				prevOffset := 0
				for k := 0; k < len(candidateAnchor.Backward); k++ {
					c := &candidateAnchor.Backward[k]
					// Offset is distance from anchor start (negative or zero)
					// Delta is distance from the start of the subsequent block
					delta := c.Offset - prevOffset
					j += delta
					if c.IsRepeat {
						if j > 0 {
							skipped := ir.WarpBack(&c.Info, b[:j])
							j -= skipped
						}
					}
					prevOffset = c.Offset
				}
			}

			if anchorStart && in.AbsPos+j > 0 {
				return -1, -1, nil
			}
			bestAnchor = candidateAnchor
			return j, matchLiteralPos, bestAnchor
		}

		i++
	}
	return -1, -1, nil
}

func pass1_BoundaryDiscovery(re *Regexp, in *ir.Input, start int) (end, prio int) {
	d := re.dfa
	trans, guards := d.Transitions(), d.AcceptingGuards()
	uIndices, uPrioDeltas := re.uIndices, re.uPrioDeltas
	b, numBytes := in.B, len(in.B)
	matchState, ccWarps := re.matchState, d.CCWarpTable()

	if len(trans) > 0 {
		_ = trans[len(trans)-1]
	}
	if len(guards) > 0 {
		_ = guards[len(guards)-1]
	}
	if len(b) > 0 {
		_ = b[len(b)-1]
	}

	state, currPrio := matchState, 0
	bestEnd, bestPriority := -1, 1<<30-1
	i := start

	if (state & ir.AcceptingStateFlag) != 0 {
		sidx := state & ir.StateIDMask
		req := guards[sidx]
		if req == 0 || ir.Verify(in, start, req) {
			bestEnd, bestPriority = start, currPrio+d.MatchPriority(sidx)
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
						p := currPrio + d.MatchPriority(sidx)
						if p <= bestPriority {
							bestEnd, bestPriority = i, p
						}
					}
				}
				if i >= numBytes {
					break
				}
			}
		}

		byteVal := b[i]
		off := (int(sidx) << 8) | int(byteVal)
		rawNext := trans[off]
		if rawNext == ir.InvalidState {
			break
		}

		// SUPER HOT PATH
		if (rawNext&(ir.AnchorVerifyFlag|ir.TaggedStateFlag|ir.WarpStateFlag|ir.AcceptingStateFlag)) == 0 && byteVal < 0x80 {
			state, i = rawNext, i+1
			continue
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
				currPrio += int(uPrioDeltas[uIdx])
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
				p := currPrio + d.MatchPriority(nsidx)
				if p <= bestPriority {
					bestEnd, bestPriority = i, p
				}
			}
			if bestPriority == 0 && d.IsBestMatch(state) {
				break
			}
		}
	}
	return bestEnd, bestPriority
}

func pass1_BoundaryDiscoveryFast(re *Regexp, in *ir.Input, start int) int {
	d := re.dfa
	trans, guards := d.Transitions(), d.AcceptingGuards()
	b, numBytes := in.B, len(in.B)
	matchState, ccWarps := re.matchState, d.CCWarpTable()

	if len(trans) > 0 {
		_ = trans[len(trans)-1]
	}
	if len(guards) > 0 {
		_ = guards[len(guards)-1]
	}
	if len(b) > 0 {
		_ = b[len(b)-1]
	}

	state, bestEnd, i := matchState, -1, start

	if (state & ir.AcceptingStateFlag) != 0 {
		sidx := state & ir.StateIDMask
		req := guards[sidx]
		if req == 0 || ir.Verify(in, start, req) {
			bestEnd = start
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
						bestEnd = i
					}
				}
				if i >= numBytes {
					break
				}
			}
		}

		byteVal := b[i]
		off := (int(sidx) << 8) | int(byteVal)
		rawNext := trans[off]
		if rawNext == ir.InvalidState {
			break
		}

		// SUPER HOT PATH
		if (rawNext&(ir.AnchorVerifyFlag|ir.WarpStateFlag|ir.AcceptingStateFlag)) == 0 && byteVal < 0x80 {
			state, i = rawNext, i+1
			continue
		}

		if (rawNext & ir.AnchorVerifyFlag) != 0 {
			req := syntax.EmptyOp((rawNext & ir.AnchorMask) >> 22)
			if req != 0 && !ir.Verify(in, i, req) {
				break
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
				bestEnd = i
			}
			if bestEnd >= 0 && d.IsBestMatch(state) {
				break
			}
		}
	}
	return bestEnd
}

func pass2_RecordingLoop(re *Regexp, in *ir.Input, mc *matchContext, start, end int) int {
	d := re.dfa
	trans, uIndices, uPrioDeltas := d.Transitions(), re.uIndices, re.uPrioDeltas
	b, matchState, ccWarps := in.B, re.matchState, d.CCWarpTable()

	mc.resetForRecording(start, end)
	state, currPrio, i := matchState, 0, start

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

		if (rawNext&(ir.AnchorVerifyFlag|ir.TaggedStateFlag|ir.WarpStateFlag)) == 0 && byteVal < 0x80 {
			state, i = rawNext, i+1
			continue
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
				currPrio += int(uPrioDeltas[uIdx])
			}
		}

		state = rawNext
		step := 1
		if byteVal >= 0x80 && (state&ir.WarpStateFlag) != 0 {
			step += ir.GetTrailingByteCount(byteVal)
		}
		if step > 1 {
			mc.appendWarp(state&ir.StateIDMask, step-1)
		}
		i += step
	}
	return currPrio + d.MatchPriority(state&ir.StateIDMask)
}

func anchoredRecordingLoop(re *Regexp, in *ir.Input, mc *matchContext, start, end int) int {
	return pass2_RecordingLoop(re, in, mc, start, end)
}

func fastDiscoveryLoop(re *Regexp, in *ir.Input) (int, int, int) {
	numBytes := len(in.B)
	for i := 0; i <= numBytes; {
		start, literalPos, anchor := pass0_DiscoveryLoop(re, in, i)
		if start < 0 {
			break
		}

		end, prio := pass1_BoundaryDiscovery(re, in, start)
		if end >= 0 {
			return start, end, prio
		}

		if anchor != nil && anchor.Mandatory && anchor.IsFixed {
			i = literalPos + 1
		} else {
			i++
		}
	}
	return -1, -1, 1<<30 - 1
}

func fastMatchExecLoop(re *Regexp, in *ir.Input) (int, int, int) {
	numBytes := len(in.B)
	for i := 0; i <= numBytes; {
		start, literalPos, anchor := pass0_DiscoveryLoop(re, in, i)
		if start < 0 {
			break
		}

		end := pass1_BoundaryDiscoveryFast(re, in, start)
		if end >= 0 {
			return start, end, 0
		}

		if anchor != nil && anchor.Mandatory && anchor.IsFixed {
			i = literalPos + 1
		} else {
			i++
		}
	}
	return -1, -1, 0
}

func extendedMatchExecLoop(re *Regexp, in *ir.Input) (int, int, int) {
	return fastDiscoveryLoop(re, in)
}

func extendedSubmatchExecLoop(re *Regexp, in *ir.Input, mc *matchContext) (int, int, int) {
	return fastDiscoveryLoop(re, in)
}
