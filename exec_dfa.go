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
	lineBounded := re.lineBounded

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
		mLiteralPos := -1

		// 1. Mandatory Text Anchor Jump
		// If we have a mandatory anchor with fixed distance to start/end, use it to warp immediately.
		anchor := re.primaryAnchor
		if anchor != nil && anchor.Mandatory && anchor.IsFixed {
			if anchor.HasBeginText {
				absPos := in.AbsPos + i
				if absPos == 0 {
					// We must match exactly at anchor.Distance
					if anchor.Distance < numBytes && (anchor.HasClass || bytes.HasPrefix(b[anchor.Distance:], anchor.Anchor)) {
						mLiteralPos = anchor.Distance
						candidatePos = mLiteralPos
						candidateAnchor = anchor
					} else {
						return -1, -1, nil
					}
				} else {
					return -1, -1, nil
				}
			} else if anchor.Type == ir.AnchorSuffix && anchor.HasEndText {
				// Match must end at absEnd. Anchor is at distance 'Distance' from match start.
				// Total match length is unknown, but anchor is at fixed distance from match start.
				// Wait, AnchorInfo.Distance is distance from match start to anchor start.
				// For suffix anchors, usually we know the distance to the end.
				// MinDistToEnd is distance from anchor start to $.
				pos := (in.TotalBytes - in.AbsPos) - len(anchor.Anchor) - anchor.MinDistToEnd
				if pos >= i && pos+len(anchor.Anchor) <= numBytes && bytes.HasPrefix(b[pos:], anchor.Anchor) {
					mLiteralPos = pos
					candidatePos = mLiteralPos
					candidateAnchor = anchor
				} else {
					return -1, -1, nil
				}
			}
		}

		// 2. Line-start check at current index (especially for index 0)
		if candidatePos < 0 && anchor != nil && anchor.HasBeginLine {
			absPos := in.AbsPos + i
			if (absPos == 0 || (absPos > 0 && in.OriginalB[absPos-1] == '\n')) && bytes.HasPrefix(b[i:], anchor.Anchor) {
				mLiteralPos = i
				candidatePos = i
				candidateAnchor = anchor
			}
		}

		// 3. Main Search
		if candidatePos < 0 {
			switch strategy {
			case ir.SearchStrategyLiteral:
				anchor := re.primaryAnchor
				if anchor.Type == ir.AnchorSuffix && (anchor.HasEndText || anchor.HasEndLine) {
					boundary := numBytes
					if !anchor.HasEndText {
						if nl := bytes.IndexByte(b[i:], '\n'); nl >= 0 {
							boundary = i + nl
						}
					}
					off := (boundary - i) - len(anchor.Anchor) - anchor.MinDistToLineEnd
					if off >= 0 && bytes.HasPrefix(b[i+off:], anchor.Anchor) {
						mLiteralPos = i + off
						candidatePos = mLiteralPos
						candidateAnchor = anchor
					} else {
						// Skip line
						if !anchor.HasEndText {
							if nl := bytes.IndexByte(b[i:], '\n'); nl >= 0 {
								i = i + nl + 1
								continue
							}
						}
						return -1, -1, nil
					}
				} else {
					pos := -1
					if anchor.HasBeginLine && re.primaryAugmented != nil {
						pos = bytes.Index(b[i:], re.primaryAugmented.Pattern)
						if pos >= 0 {
							mLiteralPos = i + pos + re.primaryAugmented.Offset
							candidatePos = mLiteralPos
							candidateAnchor = anchor
						}
					}
					// Fallback for line-start at index 0 or missed augmented
					if pos < 0 {
						pos = bytes.Index(b[i:], anchor.Anchor)
						if pos >= 0 {
							mLiteralPos = i + pos
							candidatePos = mLiteralPos
							candidateAnchor = anchor
						} else if anchor.Mandatory {
							return -1, -1, nil
						}
					}
				}
			case ir.SearchStrategySearchWarp:
				pos := ir.IndexClass(&re.searchWarp, b[i:])
				if pos >= 0 {
					mLiteralPos = i + pos
					candidatePos = mLiteralPos
				} else {
					return -1, -1, nil
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

					st := sd.Transitions[(uint16(sd.StartState)<<8)|uint16(b[currI])]
					if st != sd.DeadState {
						if sd.Accepting[st] {
							mLiteralPos = currI
							candidatePos = currI
							foundSDFA = true
							break
						}
						tempI := currI + 1
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
							mLiteralPos = currI
							candidatePos = currI
							foundSDFA = true
							break
						}
					}
					currI++
				}
				if !foundSDFA {
					return -1, -1, nil
				}
			default:
				// Fallback MAP (Covering Set)
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
						trial := i + pos
						fb := b[trial]
						if (re.searchMask[fb/64] & (1 << (fb % 64))) != 0 {
							for k := range re.mapAnchors {
								a := &re.mapAnchors[k]
								if (!a.HasClass && len(a.Anchor) > 0 && a.Anchor[0] == fb && bytes.HasPrefix(b[trial:], a.Anchor)) || (a.HasClass && ir.ValidateFixed(&a.Class, b[trial:trial+1])) {
									candidatePos, candidateAnchor = trial, a
									break
								}
							}
						}
						if candidatePos < 0 {
							i = trial + 1
							continue
						}
						mLiteralPos = candidatePos
					}
				}

				if candidatePos < 0 && re.primaryAugmented != nil {
					pos := bytes.Index(b[i:], re.primaryAugmented.Pattern)
					if pos >= 0 {
						mLiteralPos = i + pos + re.primaryAugmented.Offset
						candidatePos = mLiteralPos
						candidateAnchor = re.primaryAnchor
					}
				}

				if candidatePos < 0 {
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

				if candidatePos < 0 && re.primaryAnchor != nil {
					anchor := re.primaryAnchor
					if anchor.HasBeginText {
						if in.AbsPos+i == 0 && anchor.Distance+len(anchor.Anchor) <= numBytes && bytes.HasPrefix(b[anchor.Distance:], anchor.Anchor) {
							candidatePos, candidateAnchor = anchor.Distance, anchor
						} else if anchor.Mandatory {
							return -1, -1, nil
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
						} else if anchor.Mandatory {
							return -1, -1, nil
						}
					} else if anchor.Mandatory && anchor.IsFixed {
						pos := -1
						if !anchor.HasClass {
							pos = bytes.Index(b[i:], anchor.Anchor)
						} else {
							pos = ir.IndexClass(&anchor.Class, b[i:])
						}
						if pos >= 0 {
							candidatePos, candidateAnchor = i+pos, anchor
						} else {
							return -1, -1, nil
						}
					}
				}

				if candidatePos < 0 && len(re.prefix) > 0 {
					pos := bytes.Index(b[i:], re.prefix)
					if pos >= 0 {
						candidatePos = i + pos
						mLiteralPos = candidatePos
					}
				}
			}
		}

		if candidatePos < 0 {
			if len(re.searchAny) == 0 && len(re.prefix) == 0 && strategy == ir.SearchStrategyNone {
				candidatePos = i
				mLiteralPos = i
			} else {
				return -1, -1, nil
			}
		}

		// --- Phase 2: Gaze ---
		if candidateAnchor != nil && !candidateAnchor.SkipGaze && len(candidateAnchor.Anchor) > 0 {
			rejected := false
			totalAbsPos := in.AbsPos + candidatePos
			if candidateAnchor.HasBeginText && totalAbsPos != 0 {
				rejected = true
			}
			if !rejected && candidateAnchor.HasBeginLine && re.primaryAugmented == nil {
				if totalAbsPos > 0 && in.OriginalB[totalAbsPos-1] != '\n' {
					rejected = true
				}
			}
			if !rejected && candidateAnchor.HasEndText {
				distToEnd := in.TotalBytes - (totalAbsPos + len(candidateAnchor.Anchor))
				if distToEnd < candidateAnchor.MinDistToEnd || (candidateAnchor.MaxDistToEnd >= 0 && distToEnd > candidateAnchor.MaxDistToEnd) {
					rejected = true
				}
			}
			if !rejected && candidateAnchor.HasEndLine {
				boundary := numBytes
				if nl := bytes.IndexByte(b[candidatePos:], '\n'); nl >= 0 {
					boundary = candidatePos + nl
				}
				distToLineEnd := boundary - (candidatePos + len(candidateAnchor.Anchor))
				if distToLineEnd < candidateAnchor.MinDistToLineEnd || (candidateAnchor.MaxDistToLineEnd >= 0 && distToLineEnd > candidateAnchor.MaxDistToLineEnd) {
					rejected = true
				}
			}
			if !rejected {
				for _, c := range candidateAnchor.SimpleBackward {
					idx := candidatePos + c.Offset
					if idx < 0 || b[idx] != byte(c.Info.V0) {
						rejected = true
						break
					}
				}
			}
			if rejected {
				// If it's a mandatory fixed anchor, failing Gaze means we can give up or jump.
				if candidateAnchor.Mandatory && candidateAnchor.IsFixed {
					if candidateAnchor.HasBeginText || candidateAnchor.HasEndText {
						return -1, -1, nil
					}
				}
				i = mLiteralPos + 1
				continue
			}
		}

		// --- Phase 3: Snap ---
		j := candidatePos
		if candidateAnchor != nil {
			if candidateAnchor.IsFixed {
				j = candidatePos - candidateAnchor.Distance
				if j < i && strategy != ir.SearchStrategyNone {
					i = mLiteralPos + 1
					continue
				}
			} else {
				for _, c := range candidateAnchor.Backward {
					if c.IsRepeat {
						j -= ir.WarpBack(&c.Info, b[:j])
					} else {
						j -= c.Length
					}
				}
				if candidateAnchor.HasBeginLine {
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

		// MANDATE: restartBase must be on the SAME line if lineBounded
		if lineBounded {
			if nl := bytes.LastIndexByte(b[j:candidatePos], '\n'); nl >= 0 {
				i = mLiteralPos + 1
				continue
			}
		}

		return j, mLiteralPos, candidateAnchor
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
