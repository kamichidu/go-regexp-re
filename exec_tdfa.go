package regexp

import (
	"github.com/kamichidu/go-regexp-re/internal/ir"
)

func (re *Regexp) sparseTDFA_PathSelection(mc *matchContext, b []byte, start, end, prio int) {
	d := re.dfa
	recap := d.RecapTables()[0]
	uIndices, uUpdates := d.TagUpdateIndices(), d.TagUpdates()

	// 1. Find the history entry that covers the 'end' position.
	histIdx := -1
	byteOffset := 0
	for i, entry := range mc.history {
		length := 1
		if (entry & histWarpMarker) != 0 {
			length = int((entry & histLengthMask) >> histLengthShift)
		}
		if byteOffset+length > end {
			histIdx = i
			break
		}
		byteOffset += length
	}

	if histIdx == -1 {
		return
	}

	// Initial priority from the state at 'end'.
	entry := mc.history[histIdx]
	currPrio := int16(d.MatchPriority(entry & histStateMask))
	mc.pathHistory[end] = int32(currPrio)

	currPos := end
	// 2. Trace backward from end to start.
	// We need to use the transition: State(pos-1) --(byte at pos-1)--> State(pos)
	for i := histIdx; i >= 0; i-- {
		if currPos <= start {
			break
		}

		entry := mc.history[i]
		length := 1
		if (entry & histWarpMarker) != 0 {
			length = int((entry & histLengthMask) >> histLengthShift)
		}

		// Calculate how many bytes of this segment are within [start, currPos]
		segStart := byteOffset
		segEnd := byteOffset + length

		inSegEnd := currPos
		if segEnd < inSegEnd {
			// This should not happen in the first iteration because histIdx covers 'end'
			// In subsequent iterations, segEnd is always currPos.
			inSegEnd = segEnd
		}
		inSegStart := segStart
		if inSegStart < start {
			inSegStart = start
		}

		inSegLen := inSegEnd - inSegStart

		if (entry & histWarpMarker) != 0 {
			// Warp jump: Priority remains constant because SWAR is tag-free.
			for k := 0; k < inSegLen; k++ {
				currPos--
				mc.pathHistory[currPos] = int32(currPrio)
			}
		} else {
			// Normal entry: perform priority transition.
			currPos--
			if currPos < start {
				break
			}
			byteVal := b[currPos]

			// Source state is in the PREVIOUS entry (mc.history[i-1]).
			// Wait, what if currPos is in the middle of a warp?
			// That's handled by the Warp block above.
			// So here length is 1, and the source state is indeed mc.history[i-1].
			if i == 0 {
				break // Should not happen as we checked currPos > start
			}
			prevEntry := mc.history[i-1]
			sidx := prevEntry & histStateMask

			off := (int(sidx) << 8) | int(byteVal)
			found := false
			bestInputPrio := int16(32767)
			if off < len(recap.Transitions) {
				basePrio := int16(0)
				if off < len(uIndices) {
					uIdx := uIndices[off]
					if int(uIdx) < len(uUpdates) {
						basePrio = int16(uUpdates[uIdx].BasePriority)
					}
				}
				for _, entry := range recap.Transitions[off] {
					if int16(entry.NextPriority) == currPrio {
						p := entry.InputPriority + basePrio
						if p < bestInputPrio {
							bestInputPrio = p
							found = true
						}
					}
				}
			}
			if found {
				currPrio = bestInputPrio
			}
			mc.pathHistory[currPos] = int32(currPrio)
		}

		// Update byteOffset for the segment BEFORE this one.
		if i > 0 {
			prevEntry := mc.history[i-1]
			prevLen := 1
			if (prevEntry & histWarpMarker) != 0 {
				prevLen = int((prevEntry & histLengthMask) >> histLengthShift)
			}
			byteOffset -= prevLen
		}
	}
}

func (re *Regexp) sparseTDFA_Recap(mc *matchContext, b []byte, start, end, prio int, regs []int) {
	d := re.dfa
	recap := d.RecapTables()[0]
	uIndices, uUpdates := d.TagUpdateIndices(), d.TagUpdates()

	re.applyEntryTags(mc, regs, d.StartUpdates(), mc.pathHistory[start], start)

	// 1. Find the starting history entry
	histIdx := 0
	byteOffset := 0
	for i, entry := range mc.history {
		length := 1
		if (entry & histWarpMarker) != 0 {
			length = int((entry & histLengthMask) >> histLengthShift)
		}
		if byteOffset+length > start {
			histIdx = i
			break
		}
		byteOffset += length
	}

	currPos := start
	for i := histIdx; i < len(mc.history); i++ {
		entry := mc.history[i]
		if currPos >= end {
			break
		}

		length := 1
		if (entry & histWarpMarker) != 0 {
			length = int((entry & histLengthMask) >> histLengthShift)
		}

		if (entry & histWarpMarker) != 0 {
			// Warp jump: No tag updates in this segment.
			// The first warp might be partially covered if it starts before 'start'
			warpStart := byteOffset
			warpEnd := byteOffset + length

			actualLength := length
			if warpStart < start {
				actualLength = warpEnd - start
			}
			if currPos+actualLength > end {
				actualLength = end - currPos
			}

			currPos += actualLength
			byteOffset += length
			continue
		}

		// Normal entry: process tags
		sidx := entry & histStateMask
		pathID := mc.pathHistory[currPos]
		byteVal := b[currPos]
		off := (int(sidx) << 8) | int(byteVal)

		step := 1
		rawNext := d.Transitions()[off]
		if byteVal >= 0x80 && rawNext != ir.InvalidState && (rawNext&ir.WarpStateFlag) != 0 {
			step = 1 + ir.GetTrailingByteCount(byteVal)
		}

		if off < len(recap.Transitions) {
			basePrio := int16(0)
			if off < len(uIndices) {
				uIdx := uIndices[off]
				if int(uIdx) < len(uUpdates) {
					basePrio = int16(uUpdates[uIdx].BasePriority)
				}
			}

			nextPathID := int32(0)
			if currPos+step <= end {
				nextPathID = mc.pathHistory[currPos+step]
			}

			for _, entry := range recap.Transitions[off] {
				if entry.InputPriority == int16(pathID)-basePrio && int32(entry.NextPriority) == nextPathID {
					re.applyRawTags(mc, regs, entry.PreTags, currPos)
					re.applyRawTags(mc, regs, entry.PostTags, currPos+step)
					break
				}
			}
		}
		currPos += step
		byteOffset += length
	}
}

func (re *Regexp) applyRawTags(mc *matchContext, regs []int, tags uint64, pos int) {
	if tags == 0 {
		return
	}
	for bit := 2; bit < 64; bit++ {
		if (tags & (1 << uint(bit))) != 0 {
			if bit < len(regs) {
				if (bit%2 != 0) || regs[bit] == -1 {
					regs[bit] = pos
				}
			}
		}
	}
}

func (re *Regexp) applyEntryTags(mc *matchContext, regs []int, updates []ir.PathTagUpdate, pathID int32, pos int) {
	matchID := pathID
	if pathID >= ir.SearchRestartPenalty {
		matchID = pathID % ir.SearchRestartPenalty
	}
	for _, u := range updates {
		if int32(u.NextPriority) == matchID {
			re.applyRawTags(mc, regs, u.Tags, pos)
		}
	}
}
