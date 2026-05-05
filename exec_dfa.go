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

	bestStart, bestEnd, bestPriority := -1, -1, 1<<30-1

	for restartBase := 0; restartBase <= numBytes; restartBase++ {
		if anchorStart && restartBase > 0 {
			break
		}
		i := restartBase
		state, prio := matchState, 0

		// Pass 1.1: Multi-Point Anchor Skip
		if !anchorStart && bestStart < 0 && (matchState&ir.AcceptingStateFlag) == 0 && i < numBytes {
			var absPos int = -1
			var bestAnchor *ir.AnchorInfo

			if i == 0 && in.AbsPos == 0 && re.primaryAnchor != nil {
				for j := range re.primaryAnchor.Augmented {
					aug := &re.primaryAnchor.Augmented[j]
					if aug.IsStart && bytes.HasPrefix(b, aug.Pattern) {
						absPos = aug.Offset
						bestAnchor = re.primaryAnchor
						break
					}
				}
			}
			if absPos < 0 && in.AbsPos+numBytes == in.TotalBytes && re.primaryAnchor != nil {
				for j := range re.primaryAnchor.Augmented {
					aug := &re.primaryAnchor.Augmented[j]
					if aug.IsEnd && bytes.HasSuffix(b, aug.Pattern) {
						absPos = numBytes - len(aug.Pattern) + aug.Offset
						bestAnchor = re.primaryAnchor
						break
					}
				}
			}
			if absPos < 0 {
				if re.primaryAugmented != nil {
					pos := bytes.Index(b[i:], re.primaryAugmented.Pattern)
					if pos >= 0 {
						absPos = i + pos + re.primaryAugmented.Offset
						bestAnchor = re.primaryAnchor
					}
				} else if re.primaryAnchor != nil && re.primaryAnchor.Mandatory && re.primaryAnchor.IsFixed {
					anchor := re.primaryAnchor
					if anchor.HasBeginText {
						if in.AbsPos+i == 0 && anchor.Distance+len(anchor.Anchor) <= numBytes && bytes.HasPrefix(b[anchor.Distance:], anchor.Anchor) {
							absPos = anchor.Distance
							bestAnchor = anchor
						}
						if absPos < 0 {
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
							absPos = i + pos
							bestAnchor = anchor
						}
					} else {
						pos := bytes.Index(b[i:], anchor.Anchor)
						if pos >= 0 {
							absPos = i + pos
							bestAnchor = anchor
						}
					}
				} else if re.searchAny != "" {
					pos := bytes.IndexAny(b[i:], re.searchAny)
					if pos >= 0 {
						trial := i + pos
						fb := b[trial]
						for j := range re.mapAnchors {
							a := &re.mapAnchors[j]
							if (!a.HasClass && len(a.Anchor) > 0 && a.Anchor[0] == fb && bytes.HasPrefix(b[trial:], a.Anchor)) || (a.HasClass && ir.ValidateFixed(a.Class, b[trial:trial+1])) {
								absPos = trial
								bestAnchor = a
								break
							}
						}
					}
				} else if len(re.prefix) > 0 {
					pos := bytes.Index(b[i:], re.prefix)
					if pos >= 0 {
						absPos = i + pos
					}
				}
			}

			if absPos < 0 {
				if (re.primaryAnchor != nil && re.primaryAnchor.Mandatory) || re.searchAny != "" {
					return -1, -1, 1<<30 - 1
				}
			} else {
				if bestAnchor != nil {
					rejected := false
					if bestAnchor.HasBeginText && (in.AbsPos+absPos != 0) {
						rejected = true
					}
					if !rejected && bestAnchor.HasBeginLine && re.primaryAugmented == nil && absPos > 0 && b[absPos-1] != '\n' {
						rejected = true
					}
					if !rejected && bestAnchor.HasEndText && (in.TotalBytes-(in.AbsPos+absPos) < bestAnchor.MinDistToEnd) {
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
						restartBase = i - 1
						continue
					}
					horizon := absPos
					if bestAnchor.IsFixed {
						horizon = absPos - bestAnchor.Distance
					} else {
						for _, c := range bestAnchor.Backward {
							if c.IsRepeat {
								horizon -= ir.WarpBack(c.Info, b[:horizon])
							} else {
								horizon -= c.Length
							}
						}
						if bestAnchor.HasBeginLine {
							if nl := bytes.LastIndexByte(b[:horizon], '\n'); nl >= 0 {
								horizon = nl + 1
							} else {
								horizon = 0
							}
						}
					}
					if horizon < restartBase {
						i = absPos + 1
						restartBase = i - 1
						continue
					}
					restartBase = horizon
					i = restartBase
				} else {
					restartBase = absPos
					i = restartBase
				}
			}
		}

		currentBestEnd := -1
		currentBestPrio := 1<<30 - 1
		if (state & ir.AcceptingStateFlag) != 0 {
			sidx := state & ir.StateIDMask
			req := guards[sidx]
			if req == 0 || (ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, restartBase, req) && ir.VerifyWord(in, i, req) && ir.VerifyWord(in, restartBase, req)) {
				currentBestEnd = i
				currentBestPrio = prio + int(d.MatchPriority(sidx))
			}
		}

		ccWarps := d.CCWarpTable()
		for i < numBytes {
			byteVal := b[i]
			if (state & ir.CCWarpFlag) != 0 {
				sidx := state & ir.StateIDMask
				info := ccWarps[sidx]
				skipped := ir.Warp(info, b[i:])
				if skipped > 0 {
					i += skipped
					state &= ^ir.CCWarpFlag
					if (state & ir.AcceptingStateFlag) != 0 {
						sidx := state & ir.StateIDMask
						req := guards[sidx]
						if req == 0 || (ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, restartBase, req) && ir.VerifyWord(in, i, req) && ir.VerifyWord(in, restartBase, req)) {
							currentBestEnd = i
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
				if !(ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, i, req) && ir.VerifyWord(in, i, req)) {
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
			i++
			if (state & ir.AcceptingStateFlag) != 0 {
				sidx := state & ir.StateIDMask
				req := guards[sidx]
				if req == 0 || (ir.VerifyEnd(in, i, req) && ir.VerifyBegin(in, restartBase, req) && ir.VerifyWord(in, i, req) && ir.VerifyWord(in, restartBase, req)) {
					p := prio + int(d.MatchPriority(sidx))
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
	return bestStart, bestEnd, bestPriority
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
			i++
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
