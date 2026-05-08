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

		// --- Search ---
		switch strategy {
		case ir.SearchStrategyLiteral:
			pos := bytes.Index(b[i:], re.primaryAnchor.Anchor)
			if pos >= 0 {
				mLiteralPos = i + pos
				candidatePos = mLiteralPos
				candidateAnchor = re.primaryAnchor
			} else {
				i = numBytes + 1
				continue
			}
		case ir.SearchStrategySearchWarp:
			pos := ir.IndexClass(&re.searchWarp, b[i:])
			if pos >= 0 {
				mLiteralPos = i + pos
				candidatePos = mLiteralPos
			} else {
				i = numBytes + 1
				continue
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
					mLiteralPos = currI
					candidatePos = currI
					foundSDFA = true
					break
				}
				currI++
			}
			if !foundSDFA {
				return -1, -1, nil
			}
		default:
			// StrategyNone or fallback MAP
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
				} else {
					i = numBytes + 1
					continue
				}
			} else if len(re.prefix) > 0 {
				pos := bytes.Index(b[i:], re.prefix)
				if pos >= 0 {
					candidatePos = i + pos
					mLiteralPos = candidatePos
				} else {
					i = numBytes + 1
					continue
				}
			} else {
				candidatePos = i
				mLiteralPos = i
			}
		}

		if candidatePos < 0 {
			return -1, -1, nil
		}

		// --- Gaze ---
		if candidateAnchor != nil && !candidateAnchor.SkipGaze && len(candidateAnchor.Anchor) > 0 {
			rejected := false
			totalAbsPos := in.AbsPos + candidatePos
			if candidateAnchor.HasBeginText && (totalAbsPos != 0) {
				rejected = true
			}
			if !rejected && candidateAnchor.HasBeginLine && re.primaryAugmented == nil {
				if totalAbsPos > 0 && in.OriginalB[totalAbsPos-1] != '\n' {
					rejected = true
				}
			}
			if !rejected && candidateAnchor.HasEndText && (in.TotalBytes-totalAbsPos < candidateAnchor.MinDistToEnd) {
				rejected = true
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
				i = mLiteralPos + 1
				continue
			}
		}

		// --- Snap ---
		j := candidatePos
		if candidateAnchor != nil {
			if candidateAnchor.IsFixed {
				j = candidatePos - candidateAnchor.Distance
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

		return j, mLiteralPos, candidateAnchor
	}

	return -1, -1, nil
}

// Pass 1: Boundary Discovery
// Performs a forward DFA scan from a known start position to find the best match end and its priority.
func pass1_BoundaryDiscovery(re *Regexp, in *ir.Input, start int) (end, prio int) {
	d := re.dfa
	trans := d.Transitions()
	guards := d.AcceptingGuards()
	uIndices := re.uIndices
	uPrioDeltas := re.uPrioDeltas
	b := in.B
	numBytes := len(b)
	matchState := re.matchState
	ccWarps := d.CCWarpTable()

	// Bounds check hints
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

	// Initial accepting check
	if (state & ir.AcceptingStateFlag) != 0 {
		sidx := state & ir.StateIDMask
		req := guards[sidx]
		if req == 0 || ir.Verify(in, i, req) {
			bestEnd = i
			bestPriority = currPrio + d.MatchPriority(sidx)
		}
	}

	for i < numBytes {
		sidx := state & ir.StateIDMask

		// 1. CCWarp (SWAR skip) - Optimization for repeated character classes
		if (state & ir.CCWarpFlag) != 0 {
			info := &ccWarps[sidx]
			skipped := ir.Warp(info, b[i:])
			if skipped > 0 {
				i += skipped
				// Check for acceptance at new position
				if (state & ir.AcceptingStateFlag) != 0 {
					req := guards[sidx]
					if req == 0 || ir.Verify(in, i, req) {
						p := currPrio + d.MatchPriority(sidx)
						if p <= bestPriority {
							bestEnd = i
							bestPriority = p
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

		// SUPER HOT PATH: No side effects, not accepting, and not a multi-byte UTF-8 lead byte.
		// This eliminates almost all branch overhead for simple DFA transitions.
		if (rawNext&(ir.AnchorVerifyFlag|ir.TaggedStateFlag|ir.WarpStateFlag|ir.AcceptingStateFlag)) == 0 && byteVal < 0x80 {
			state = rawNext
			i++
			continue
		}

		// SLOW PATH: Side effects or complex state
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
					bestEnd = i
					bestPriority = p
				}
			}
			if bestPriority == 0 && d.IsBestMatch(state) {
				break
			}
		}
	}

	return bestEnd, bestPriority
}

// Pass 2: Anchored Recording
// Re-runs the DFA strictly over [start, end] to record the execution history for submatch extraction.
func anchoredRecordingLoop(re *Regexp, in *ir.Input, mc *matchContext, start, end int) int {
	d := re.dfa
	trans := d.Transitions()
	uIndices := re.uIndices
	uPrioDeltas := re.uPrioDeltas
	b := in.B
	matchState := re.matchState
	ccWarps := d.CCWarpTable()

	mc.resetForRecording(start, end)

	state, currPrio := matchState, 0
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

		// Optimized restart: If the anchor is Mandatory and Fixed, we can skip past it.
		// Otherwise, we must increment by 1 to ensure leftmost-longest.
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
	return -1, -1, 0
}

func extendedMatchExecLoop(re *Regexp, in ir.Input) (int, int, int) {
	return fastDiscoveryLoop(re, &in)
}

func extendedSubmatchExecLoop(re *Regexp, in ir.Input, mc *matchContext) (int, int, int) {
	return fastDiscoveryLoop(re, &in)
}
