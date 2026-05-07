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
			sidx := state & ir.StateIDMask
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
		if rawNext == ir.InvalidState {
			break
		}

		if (rawNext & ir.AnchorVerifyFlag) != 0 {
			req := syntax.EmptyOp((rawNext & ir.AnchorMask) >> 22)
			if !(ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
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

	lastI := -1
	for i := 0; i <= numBytes; {
		if lastI != -1 && i <= lastI {
			i = lastI + 1
		}
		if i > numBytes {
			break
		}
		lastI = i

		if anchorStart && i > 0 {
			break
		}

		var absPos int = -1
		var bestAnchor *ir.AnchorInfo
		matchLiteralPos := -1

		// --- Phase 1: Search (Optimized) ---
		if !anchorStart && (matchState&ir.AcceptingStateFlag) == 0 {
			candidatePos := -1
			var candidateAnchor *ir.AnchorInfo

			switch strategy {
			case ir.SearchStrategyLiteral:
				pos := bytes.Index(b[i:], re.primaryAnchor.Anchor)
				if pos >= 0 {
					matchLiteralPos = i + pos
					candidatePos = matchLiteralPos
					candidateAnchor = re.primaryAnchor
				} else {
					i = numBytes
				}
			case ir.SearchStrategySearchWarp:
				pos := ir.IndexClass(re.searchWarp, b[i:])
				if pos >= 0 {
					matchLiteralPos = i + pos
					candidatePos = matchLiteralPos
				} else {
					i = numBytes
				}
			case ir.SearchStrategySDFA:
				sd := d.SearchDFA()
				foundSDFA := false
				for i < numBytes {
					// Use IndexClass for Trigger if it looks like a simple set
					// For now, keep IndexAny but we know it's a bottleneck.
					idx := bytes.IndexAny(b[i:], sd.Trigger)
					if idx < 0 {
						break
					}
					i += idx

					state := sd.StartState
					currI := i
					found := false
					for currI < numBytes {
						state = sd.Transitions[(uint16(state)<<8)|uint16(b[currI])]
						if state == sd.DeadState {
							break
						}
						if sd.Accepting[state] {
							found = true
							break
						}
						currI++
					}

					if found {
						matchLiteralPos = i
						candidatePos = i
						foundSDFA = true
						break
					}
					i++
				}
				if !foundSDFA {
					return -1, -1, 1<<30 - 1
				}
			default:
				// StrategyNone or multi-anchor search
				if re.primaryAugmented != nil {
					pos := bytes.Index(b[i:], re.primaryAugmented.Pattern)
					if pos >= 0 {
						matchLiteralPos = i + pos + re.primaryAugmented.Offset
						candidatePos = matchLiteralPos
						candidateAnchor = re.primaryAnchor
					}
				}
				if candidatePos < 0 {
					// Check explicit anchors (e.g. ^ at pos 0)
					for k := range re.mapAnchors {
						a := &re.mapAnchors[k]
						if a.HasBeginText && in.AbsPos+i == 0 {
							candidatePos, candidateAnchor = i, a
							break
						}
						if a.HasBeginLine && (in.AbsPos+i == 0 || (in.AbsPos+i > 0 && in.OriginalB[in.AbsPos+i-1] == '\n')) {
							candidatePos, candidateAnchor = i, a
							break
						}
					}
				}
				if candidatePos < 0 && re.primaryAnchor != nil && re.primaryAnchor.Mandatory && re.primaryAnchor.IsFixed {
					anchor := re.primaryAnchor
					if anchor.HasBeginText {
						if in.AbsPos+i == 0 && anchor.Distance+len(anchor.Anchor) <= numBytes && bytes.HasPrefix(b[anchor.Distance:], anchor.Anchor) {
							candidatePos, candidateAnchor = anchor.Distance, anchor
						} else {
							i = numBytes
						}
					} else if anchor.Type == ir.AnchorSuffix && (anchor.HasEndText || anchor.HasEndLine) {
						boundary := numBytes
						if !anchor.HasEndText {
							if nl := bytes.IndexByte(b[i:], '\n'); nl >= 0 {
								boundary = i + nl
							}
						}
						pos := (boundary - i) - len(anchor.Anchor) - anchor.MinDistToLineEnd
						if pos >= 0 && bytes.HasPrefix(b[i+pos:], anchor.Anchor) {
							candidatePos, candidateAnchor = i+pos, anchor
						}
					} else {
						pos := -1
						if !anchor.HasClass {
							pos = bytes.Index(b[i:], anchor.Anchor)
						} else {
							pos = ir.IndexClass(anchor.Class, b[i:])
						}
						if pos >= 0 {
							candidatePos, candidateAnchor = i+pos, anchor
						}
					}
				} else if candidatePos < 0 && len(re.searchAny) > 0 {
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
						trial := i + pos
						fb := b[trial]
						if (re.searchMask[fb/64] & (1 << (fb % 64))) != 0 {
							for k := range re.mapAnchors {
								a := &re.mapAnchors[k]
								if (!a.HasClass && len(a.Anchor) > 0 && a.Anchor[0] == fb && bytes.HasPrefix(b[trial:], a.Anchor)) || (a.HasClass && ir.ValidateFixed(a.Class, b[trial:trial+1])) {
									candidatePos, candidateAnchor = trial, a
									break
								}
							}
						}
						if candidatePos < 0 {
							i = trial + 1
							continue
						}
					} else {
						i = numBytes
					}
				} else if candidatePos < 0 && len(re.prefix) > 0 {
					pos := bytes.Index(b[i:], re.prefix)
					if pos >= 0 {
						candidatePos = i + pos
					}
				} else if candidatePos < 0 {
					candidatePos = i
				}
			}

			if candidatePos < 0 {
				if i > lastI {
					continue
				}
				return -1, -1, 1<<30 - 1
			}
			absPos = candidatePos
			bestAnchor = candidateAnchor
			if matchLiteralPos >= 0 {
				i = matchLiteralPos + 1
			} else {
				i = absPos + 1
			}
		} else {
			absPos = i
			matchLiteralPos = i
		}

		if absPos < 0 {
			i++
			continue
		}

		// --- Phase 2: Gaze ---
		if bestAnchor != nil && !bestAnchor.SkipGaze && len(bestAnchor.Anchor) > 0 {
			rejected := false
			totalAbsPos := in.AbsPos + absPos
			if bestAnchor.HasBeginText && (totalAbsPos != 0) {
				rejected = true
			}
			if !rejected && bestAnchor.HasBeginLine && re.primaryAugmented == nil {
				if totalAbsPos > 0 && in.OriginalB[totalAbsPos-1] != '\n' {
					rejected = true
				}
			}
			if !rejected && bestAnchor.HasEndText && (in.TotalBytes-totalAbsPos < bestAnchor.MinDistToEnd) {
				rejected = true
			}
			if !rejected {
				for _, c := range bestAnchor.SimpleBackward {
					idx := absPos + c.Offset
					if idx < 0 || b[idx] != byte(c.Info.V0) {
						rejected = true
						break
					}
				}
			}
			if rejected {
				i = matchLiteralPos + 1
				continue
			}
		}

		// --- Phase 3: Snap ---
		j := absPos
		if bestAnchor != nil {
			if bestAnchor.IsFixed {
				j = absPos - bestAnchor.Distance
			} else {
				for _, c := range bestAnchor.Backward {
					if c.IsRepeat {
						j -= ir.WarpBack(c.Info, b[:j])
					} else {
						j -= c.Length
					}
				}
				if bestAnchor.HasBeginLine {
					if nl := bytes.LastIndexByte(b[:j], '\n'); nl >= 0 {
						j = nl + 1
					} else {
						j = 0
					}
				}
			}
		}
		if j < 0 {
			j = 0
		}

		// --- Phase 4: DFA Execution ---
		state, prio := matchState, 0
		scanPos := j
		currentBestEnd := -1
		currentBestPrio := 1<<30 - 1

		if (state & ir.AcceptingStateFlag) != 0 {
			sidx := state & ir.StateIDMask
			req := guards[sidx]
			if req == 0 || (ir.VerifyEnd(in, scanPos, req) && ir.VerifyBegin(in, scanPos, req) && ir.VerifyWord(in, scanPos, req)) {
				currentBestEnd = scanPos
				currentBestPrio = prio + int(d.MatchPriority(sidx))
				if currentBestPrio == 0 && d.IsBestMatch(state) {
					return j, currentBestEnd, currentBestPrio
				}
			}
		}

		for scanPos < numBytes {
			byteVal := b[scanPos]
			if (state & ir.CCWarpFlag) != 0 {
				sidx := state & ir.StateIDMask
				info := ccWarps[sidx]
				skipped := ir.Warp(info, b[scanPos:])
				if skipped > 0 {
					scanPos += skipped
					state &= ^ir.CCWarpFlag
					if (state & ir.AcceptingStateFlag) != 0 {
						sidx := state & ir.StateIDMask
						req := guards[sidx]
						if req == 0 || (ir.VerifyEnd(in, scanPos, req) && ir.VerifyBegin(in, scanPos, req) && ir.VerifyWord(in, scanPos, req)) {
							currentBestEnd = scanPos
							currentBestPrio = prio + int(d.MatchPriority(sidx))
							if currentBestPrio == 0 && d.IsBestMatch(state) {
								return j, currentBestEnd, currentBestPrio
							}
						}
					}
					continue
				}
			}

			off := (int(state&ir.StateIDMask) << 8) | int(byteVal)
			rawNext := trans[off]
			if rawNext == ir.InvalidState {
				break
			}
			if (rawNext & ir.AnchorVerifyFlag) != 0 {
				req := syntax.EmptyOp((rawNext & ir.AnchorMask) >> 22)
				if !(ir.VerifyEnd(in, scanPos, req) && ir.VerifyBegin(in, scanPos, req) && ir.VerifyWord(in, scanPos, req)) {
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
			scanPos += step
			if (state & ir.AcceptingStateFlag) != 0 {
				sidx := state & ir.StateIDMask
				req := guards[sidx]
				if req == 0 || (ir.VerifyEnd(in, scanPos, req) && ir.VerifyBegin(in, scanPos, req) && ir.VerifyWord(in, scanPos, req)) {
					p := prio + int(d.MatchPriority(sidx))
					if p < currentBestPrio {
						currentBestEnd = scanPos
						currentBestPrio = p
					} else if p == currentBestPrio {
						currentBestEnd = scanPos
					}
					if currentBestPrio == 0 && d.IsBestMatch(state) {
						break
					}
				}
			}
		}
		if currentBestEnd >= 0 {
			return j, currentBestEnd, currentBestPrio
		}
		i = matchLiteralPos + 1
	}
	return -1, -1, 1<<30 - 1
}

func fastMatchExecLoop(re *Regexp, in *ir.Input) (int, int, int) { return fastDiscoveryLoop(re, in) }

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
	ccWarps := d.CCWarpTable()

	for restartBase := 0; restartBase <= numBytes; restartBase++ {
		if anchorStart && restartBase > 0 {
			break
		}
		i := restartBase
		state, prio := matchState, 0
		currentBestEnd := -1
		currentBestPrio := int64(1<<60 - 1)
		mc.clearHistory()

		if (state & ir.AcceptingStateFlag) != 0 {
			sidx := state & ir.StateIDMask
			req := guards[sidx]
			if req == 0 || (ir.VerifyEnd(&in, i, req) && ir.VerifyBegin(&in, restartBase, req) && ir.VerifyWord(&in, i, req) && ir.VerifyWord(&in, restartBase, req)) {
				currentBestEnd = i
				currentBestPrio = int64(prio) + int64(d.MatchPriority(sidx))
			}
		}
		for i < numBytes {
			byteVal := b[i]
			sidx := state & ir.StateIDMask
			mc.appendRaw(sidx)

			if (state & ir.CCWarpFlag) != 0 {
				info := ccWarps[sidx]
				skipped := ir.Warp(info, b[i:])
				if skipped > 0 {
					mc.appendWarp(sidx, skipped)
					i += skipped
					state &= ^ir.CCWarpFlag
					if (state & ir.AcceptingStateFlag) != 0 {
						sidx := state & ir.StateIDMask
						req := guards[sidx]
						if req == 0 || (ir.VerifyEnd(&in, i, req) && ir.VerifyBegin(&in, restartBase, req) && ir.VerifyWord(&in, i, req) && ir.VerifyWord(&in, restartBase, req)) {
							currentBestEnd = i
							currentBestPrio = int64(prio) + int64(d.MatchPriority(sidx))
						}
					}
					continue
				}
			}

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
				sidx := state & ir.StateIDMask
				req := guards[sidx]
				if req == 0 || (ir.VerifyEnd(&in, i, req) && ir.VerifyBegin(&in, restartBase, req) && ir.VerifyWord(&in, i, req) && ir.VerifyWord(&in, restartBase, req)) {
					p := int64(prio) + int64(d.MatchPriority(sidx))
					if p < currentBestPrio {
						currentBestEnd = i
						currentBestPrio = p
					} else if p == currentBestPrio {
						currentBestEnd = i
					}
				}
			}
		}
		if currentBestEnd >= 0 {
			return restartBase, currentBestEnd, int(currentBestPrio)
		}
	}
	return bestStart, bestEnd, int(bestPriority)
}
