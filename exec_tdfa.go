package regexp

import (
	"github.com/kamichidu/go-regexp-re/internal/ir"
)

func (re *Regexp) sparseTDFA_PathSelection(mc *matchContext, b []byte, start, end, prio int) {
	d := re.dfa
	recap := d.RecapTables()[0]

	// mc.history has entries for pos start, start+1, ..., end
	lastSidx := mc.history[end-start] & ir.StateIDMask
	currPrio := int32(d.MatchPriority(lastSidx))
	mc.pathHistory[end] = currPrio

	for curr := end - 1; curr >= start; curr-- {
		h := mc.history[curr-start]
		sidx := h & ir.StateIDMask
		byteVal := byte(0)
		if curr < len(b) {
			byteVal = b[curr]
		}

		off := (int(sidx) << 8) | int(byteVal)
		found := false
		bestInputPrio := int32(1 << 30)

		isLast := curr == end-1
		for _, entry := range recap.Transitions[off] {
			if entry.NextPriority == currPrio && entry.IsMatch == isLast {
				if entry.InputPriority < bestInputPrio {
					bestInputPrio = entry.InputPriority
					found = true
				}
			}
		}

		if !found {
			// Fallback: try without IsMatch constraint if trace is broken (should not happen)
			for _, entry := range recap.Transitions[off] {
				if entry.NextPriority == currPrio {
					if entry.InputPriority < bestInputPrio {
						bestInputPrio = entry.InputPriority
						found = true
					}
				}
			}
		}

		if found {
			currPrio = bestInputPrio
		} else {
			currPrio = 0
		}
		mc.pathHistory[curr] = currPrio
	}
}

func (re *Regexp) sparseTDFA_Recap(mc *matchContext, b []byte, start, end, prio int, regs []int) {
	d := re.dfa
	recap := d.RecapTables()[0]

	re.applyEntryTags(regs, d.StartUpdates(), mc.pathHistory[start], start)

	for curr := start; curr < end; curr++ {
		h := mc.history[curr-start]
		sidx := h & ir.StateIDMask
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
