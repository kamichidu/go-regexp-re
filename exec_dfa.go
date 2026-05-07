package regexp

import (
	"bytes"
	"fmt"

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

	lastI := -1
	// i: Progress pointer. Points to the next possible position of interest.
	for i := 0; i <= numBytes; {
		if i <= lastI {
			panic(fmt.Sprintf("infinite loop detected in fastDiscoveryLoop: i=%d lastI=%d pattern=%q prefix=%q searchAny=%v", i, lastI, re.expr, re.prefix, re.searchAny))
		}
		lastI = i

		if anchorStart && i > 0 {
			break
		}

		var absPos int = -1
		var bestAnchor *ir.AnchorInfo

		// --- Phase 1: Search ---
		if !anchorStart && (matchState&ir.AcceptingStateFlag) == 0 && (re.primaryAnchor != nil || len(re.searchAny) > 0 || len(re.prefix) > 0 || re.searchWarp.Kernel != ir.CCWarpNone) {
			// Find the next potential candidate starting from i
			candidatePos := -1
			var candidateAnchor *ir.AnchorInfo

			// Check if any anchor could match at i (e.g. ^ at pos 0)
			for k := range re.mapAnchors {
				a := &re.mapAnchors[k]
				if a.HasBeginText && in.AbsPos+i == 0 {
					candidatePos = i
					candidateAnchor = a
					break
				}
				if a.HasBeginLine && (in.AbsPos+i == 0 || (in.AbsPos+i > 0 && in.OriginalB[in.AbsPos+i-1] == '\n')) {
					candidatePos = i
					candidateAnchor = a
					break
				}
			}

			if candidatePos < 0 && i == 0 && in.AbsPos == 0 && re.primaryAnchor != nil {
				for k := range re.primaryAnchor.Augmented {
					aug := &re.primaryAnchor.Augmented[k]
					if aug.IsStart && bytes.HasPrefix(b, aug.Pattern) {
						candidatePos = aug.Offset
						candidateAnchor = re.primaryAnchor
						break
					}
				}
			}
			if candidatePos < 0 && in.AbsPos+numBytes == in.TotalBytes && re.primaryAnchor != nil {
				for k := range re.primaryAnchor.Augmented {
					aug := &re.primaryAnchor.Augmented[k]
					if aug.IsEnd && bytes.HasSuffix(b, aug.Pattern) {
						pos := numBytes - len(aug.Pattern) + aug.Offset
						if pos >= i {
							candidatePos = pos
							candidateAnchor = re.primaryAnchor
							break
						}
					}
				}
			}
			if candidatePos < 0 {
				if re.primaryAugmented != nil {
					pos := bytes.Index(b[i:], re.primaryAugmented.Pattern)
					if pos >= 0 {
						candidatePos = i + pos + re.primaryAugmented.Offset
						candidateAnchor = re.primaryAnchor
					}
				} else if re.primaryAnchor != nil && re.primaryAnchor.Mandatory && re.primaryAnchor.IsFixed {
					anchor := re.primaryAnchor
					if anchor.HasBeginText {
						if in.AbsPos+i == 0 && anchor.Distance+len(anchor.Anchor) <= numBytes && bytes.HasPrefix(b[anchor.Distance:], anchor.Anchor) {
							candidatePos = anchor.Distance
							candidateAnchor = anchor
						} else {
							return -1, -1, 1<<30 - 1
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
							candidatePos = i + pos
							candidateAnchor = anchor
						}
					} else {
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
						if pos >= 0 {
							candidatePos = i + pos
							candidateAnchor = anchor
						}
					}
				} else if len(re.searchAny) > 0 {
					var pos int = -1
					if len(re.searchAny) == 1 {
						pos = bytes.IndexByte(b[i:], re.searchAny[0])
					} else {
						// For sets, use a loop over bytes.IndexByte to find the EARLIEST occurrence.
						// We don't use IndexAny because it interprets its argument as a UTF-8 string.
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
						// Fast verification using searchMask
						if (re.searchMask[fb/64] & (1 << (fb % 64))) != 0 {
							for k := range re.mapAnchors {
								a := &re.mapAnchors[k]
								if (!a.HasClass && len(a.Anchor) > 0 && a.Anchor[0] == fb && bytes.HasPrefix(b[trial:], a.Anchor)) || (a.HasClass && ir.ValidateFixed(a.Class, b[trial:trial+1])) {
									candidatePos = trial
									candidateAnchor = a
									break
								}
							}
						}
						if candidatePos < 0 {
							// Found a character in searchAny, but not a valid anchor. Skip it.
							i = trial + 1
							continue
						}
					}
				} else if len(re.prefix) > 0 {
					pos := bytes.Index(b[i:], re.prefix)
					if pos >= 0 {
						candidatePos = i + pos
					}
				} else if re.searchWarp.Kernel != ir.CCWarpNone {
					pos := -1
					if re.searchWarp.IndexAny != "" {
						pos = bytes.IndexAny(b[i:], re.searchWarp.IndexAny)
					} else {
						pos = ir.IndexClass(re.searchWarp, b[i:])
					}
					if pos >= 0 {
						candidatePos = i + pos
					}
				}
			}

			if candidatePos < 0 {
				return -1, -1, 1<<30 - 1
			}
			absPos = candidatePos
			bestAnchor = candidateAnchor
		} else {
			absPos = i
		}

		if absPos < 0 {
			i++
			continue
		}

		// --- Phase 2: Gaze (Verify O(1) constraints) ---
		if bestAnchor != nil && len(bestAnchor.Anchor) > 0 {
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
				i = absPos + 1
				continue
			}
		}

		// --- Phase 3: Snap (Horizon) ---
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
			if (rawNext & ir.TaggedStateFlag) != 0 {
				uIdx := uIndices[off]
				if uIdx != 0xFFFFFFFF {
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
				}
			}
		}

		if currentBestEnd >= 0 {
			return j, currentBestEnd, currentBestPrio
		}

		// Progress Guarantee
		i = absPos + 1
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
