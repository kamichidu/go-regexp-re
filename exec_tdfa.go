package regexp

import (
	"github.com/kamichidu/go-regexp-re/internal/ir"
)

func (re *Regexp) sparseTDFA_PathSelection(mc *matchContext, b []byte, start, end, finalPrio int) {
	d := re.dfa
	history := mc.history
	recapTables := d.RecapTables()
	if len(recapTables) == 0 || len(history) == 0 {
		return
	}
	recap := recapTables[0]

	// Tracing backwards
	currentPrio := int16(finalPrio)

	// Collect steps first
	type step struct {
		sidx uint32
		byte byte
	}
	var steps []step
	currPos := start
	for _, h := range history {
		sidx := h & ir.StateIDMask
		count := 1
		if (h & 0x80000000) != 0 {
			count = int((h & 0x7FF00000) >> 20)
		}
		for j := 0; j < count; j++ {
			if currPos >= end {
				break
			}
			byteVal := byte(0)
			if currPos < len(b) {
				byteVal = b[currPos]
			}
			steps = append(steps, step{sidx, byteVal})
			currPos++
		}
		if currPos >= end {
			break
		}
	}

	// Trace backwards
	for i := len(steps) - 1; i >= 0; i-- {
		mc.pathHistory[i+1] = int32(currentPrio)
		s := steps[i]
		off := (int(s.sidx) << 8) | int(s.byte)
		found := false
		for _, e := range recap.Transitions[off] {
			if e.NextPriority == currentPrio {
				currentPrio = e.InputPriority
				found = true
				break
			}
		}
		if !found {
			// Fallback: assume priority 0 if path is broken
			currentPrio = 0
		}
	}
	mc.pathHistory[0] = int32(currentPrio)
}

func (re *Regexp) sparseTDFA_Recap(mc *matchContext, b []byte, start, end, finalPrio int, regs []int) {
	d := re.dfa
	history := mc.history
	recapTables := d.RecapTables()
	if len(recapTables) == 0 || len(history) == 0 {
		return
	}
	recap := recapTables[0]

	curr := start
	stepIdx := 0

	// Apply initial tags at match start using pathHistory[0]
	currentPrio := int16(mc.pathHistory[0])
	for _, u := range d.StartUpdates() {
		if int16(u.NextPriority) == currentPrio {
			applyTags(regs, u.Tags, curr, 0)
			// Do NOT break here, OR tags if multiple start updates exist (shouldn't happen with dedup)
		}
	}

	for i, h := range history {
		sidx := h & ir.StateIDMask
		count := 1
		if (h & 0x80000000) != 0 {
			count = int((h & 0x7FF00000) >> 20)
		}

		for j := 0; j < count; j++ {
			if curr >= end {
				break
			}
			byteVal := byte(0)
			if curr < len(b) {
				byteVal = b[curr]
			}

			currentPrio = int16(mc.pathHistory[stepIdx])
			nextPrio := int16(mc.pathHistory[stepIdx+1])

			off := (int(sidx) << 8) | int(byteVal)
			entries := recap.Transitions[off]
			for _, e := range entries {
				if e.InputPriority == currentPrio && e.NextPriority == nextPrio {
					applyTags(regs, e.PreTags, curr, 0)
					applyTags(regs, e.PostTags, curr+1, 0)
					break
				}
			}
			curr++
			stepIdx++
		}
		if curr >= end || i == len(history)-1 {
			break
		}
	}

	// Final mapping of indices to absolute coordinates exactly once
	absBase := mc.absBase
	for i := range regs {
		if regs[i] >= 0 {
			regs[i] += absBase
		}
	}
	regs[0], regs[1] = start+absBase, end+absBase
}

func applyTags(regs []int, tags uint64, pos int, absBase int) {
	if tags == 0 {
		return
	}
	for i := uint(0); i < 32; i++ {
		if (tags & (1 << (2 * i))) != 0 {
			if regs[2*i] < 0 {
				regs[2*i] = pos + absBase
			}
		}
		if (tags & (1 << (2*i + 1))) != 0 {
			regs[2*i+1] = pos + absBase
		}
	}
}
