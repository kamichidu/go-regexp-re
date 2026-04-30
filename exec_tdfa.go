package regexp

func (re *Regexp) sparseTDFA_PathSelection(mc *matchContext, b []byte, start, end, finalPrio int) {
}

func (re *Regexp) sparseTDFA_Recap(mc *matchContext, b []byte, start, end, finalPrio int, regs []int) {
	d := re.dfa
	history := mc.history
	recapTables := d.RecapTables()
	if len(recapTables) == 0 || len(history) == 0 {
		return
	}
	recap := recapTables[0]

	currentPrio := int16(0)
	curr := start

	// Apply initial tags at match start
	for _, u := range d.StartUpdates() {
		if int16(u.NextPriority) == currentPrio {
			applyTags(regs, u.Tags, curr, 0)
			break
		}
	}

	for i, h := range history {
		sidx := h & histStateMask
		count := 1
		if (h & histWarpMarker) != 0 {
			count = int((h & histLengthMask) >> histLengthShift)
		}

		for j := 0; j < count; j++ {
			if curr >= end {
				break
			}
			byteVal := byte(0)
			if curr < len(b) {
				byteVal = b[curr]
			}
			off := (int(sidx) << 8) | int(byteVal)
			entries := recap.Transitions[off]
			found := false
			for _, e := range entries {
				if e.InputPriority == currentPrio {
					applyTags(regs, e.PreTags, curr, 0)
					applyTags(regs, e.PostTags, curr+1, 0)
					currentPrio = e.NextPriority
					found = true
					break
				}
			}
			if !found {
				break
			}
			curr++
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
	// regs[0], regs[1] must be set correctly using absolute coordinates
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
