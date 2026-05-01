package regexp

import (
	"github.com/kamichidu/go-regexp-re/internal/ir"
)

func (re *Regexp) sparseTDFA_PathSelection(mc *matchContext, b []byte, start, end, prio int) {
	d := re.dfa
	recap := d.RecapTables()[0]

	hIdx := len(mc.history) - 1
	if hIdx < 0 {
		return
	}

	lastSidx := mc.history[hIdx] & histStateMask
	currPrio := int32(d.MatchPriority(lastSidx))
	mc.pathHistory[end] = currPrio

	curr := end
	for hIdx > 0 {
		hIdx--

		// The state BEFORE the current byte(s)
		prevH := mc.history[hIdx]
		sidx := prevH & histStateMask

		if (prevH & histWarpMarker) != 0 {
			// TRICKY: If prevH is a warp, it means the state at hIdx-1
			// transitioned via this warp to the state at hIdx.
			// But wait, anchoredRecordingLoop records [S_i, Warp(S_i, n), S_{i+n}].
			// So if history[hIdx] is a Warp, the state BEFORE it is history[hIdx-1].
			length := int((prevH & histLengthMask) >> histLengthShift)
			for j := 0; j < length; j++ {
				curr--
				mc.pathHistory[curr] = currPrio
			}
			// Move hIdx to the state before the warp
			hIdx--
			continue
		}

		curr--
		byteVal := byte(0)
		if curr < len(b) {
			byteVal = b[curr]
		}

		off := (int(sidx) << 8) | int(byteVal)
		isLast := curr == end-1

		found := false
		if off < len(recap.Transitions) {
			for _, entry := range recap.Transitions[off] {
				if entry.NextPriority == currPrio && entry.IsMatch == isLast {
					currPrio = entry.InputPriority
					mc.pathHistory[curr] = currPrio
					found = true
					break
				}
			}
		}

		if !found {
			mc.pathHistory[curr] = currPrio
		}
	}
}

func (re *Regexp) sparseTDFA_Recap(mc *matchContext, b []byte, start, end, prio int, regs []int) {
	d := re.dfa
	recap := d.RecapTables()[0]

	for i := range regs {
		regs[i] = -1
	}

	re.applyEntryTags(regs, d.StartUpdates(), mc.pathHistory[start], start)

	hIdx := 0
	curr := start
	for hIdx < len(mc.history)-1 {
		h := mc.history[hIdx]
		hIdx++

		sidx := h & histStateMask

		// If the next entry is a warp, it covers transitions from 'h'
		nextH := mc.history[hIdx]
		if (nextH & histWarpMarker) != 0 {
			length := int((nextH & histLengthMask) >> histLengthShift)
			curr += length
			hIdx++ // Skip the state recorded after the warp
			continue
		}

		byteVal := byte(0)
		if curr < len(b) {
			byteVal = b[curr]
		}

		pathID := mc.pathHistory[curr]
		nextPathID := mc.pathHistory[curr+1]

		off := (int(sidx) << 8) | int(byteVal)
		isLast := curr == end-1
		if off < len(recap.Transitions) {
			for _, entry := range recap.Transitions[off] {
				if entry.InputPriority == pathID && entry.NextPriority == nextPathID && entry.IsMatch == isLast {
					re.applyRawTags(regs, entry.PreTags, curr)
					re.applyRawTags(regs, entry.PostTags, curr+1)
					break
				}
			}
		}
		curr++
	}

	absBase := mc.absBase
	for i := range regs {
		if regs[i] >= 0 {
			regs[i] += absBase
		}
	}
	regs[0], regs[1] = start+absBase, end+absBase
}

func (re *Regexp) applyRawTags(regs []int, tags uint64, pos int) {
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

func (re *Regexp) applyEntryTags(regs []int, updates []ir.PathTagUpdate, pathID int32, pos int) {
	for _, u := range updates {
		if u.NextPriority == pathID {
			re.applyRawTags(regs, u.PreTags|u.PostTags, pos)
		}
	}
}
