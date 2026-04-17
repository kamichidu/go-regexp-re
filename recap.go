package regexp

import (
	"math/bits"

	"github.com/kamichidu/go-regexp-re/internal/ir"
	"github.com/kamichidu/go-regexp-re/syntax"
)

func applyTags(t uint64, pos int, regs []int) {
	for t != 0 {
		i := bits.TrailingZeros64(t)
		if i < len(regs) {
			regs[i] = pos
		}
		t &= ^(uint64(1) << i)
	}
}

func (re *Regexp) applyContextToState(d *ir.DFA, state ir.StateID, context syntax.EmptyOp, pos int, currentPrio *int, targetPrio int) ir.StateID {
	if state == ir.InvalidState || context == 0 {
		return state
	}
	s := state & ir.StateIDMask
	flags := state & ^ir.StateIDMask
	for iter := 0; iter < 6; iter++ {
		changed := false
		for bit := 0; bit < 6; bit++ {
			if (context & (1 << uint(bit))) != 0 {
				rawNext := d.AnchorNext(s, bit)
				if rawNext != ir.InvalidState {
					nextID := rawNext & ir.StateIDMask
					if rawNext < 0 {
						update := d.AnchorTransitionUpdate(s, bit)
						if currentPrio != nil {
							*currentPrio += int(update.BasePriority)
						}
					}
					if nextID != s {
						s = nextID
						changed = true
					}
				}
			}
		}
		if !changed {
			break
		}
	}
	return s | flags
}

func (re *Regexp) followPathAnchorsGroup(d *ir.DFA, fromState, toState ir.StateID, context syntax.EmptyOp, pos int, regs []int, p_in int32, groupIdx int) int32 {
	if (fromState & ir.StateIDMask) == (toState & ir.StateIDMask) {
		return p_in
	}
	s := fromState & ir.StateIDMask
	p := p_in
	for iter := 0; iter < 6; iter++ {
		changed := false
		for bit := 0; bit < 6; bit++ {
			if (context & (1 << uint(bit))) != 0 {
				rawNext := d.AnchorNext(s, bit)
				if rawNext != ir.InvalidState {
					nextID := rawNext & ir.StateIDMask
					if rawNext < 0 {
						update := d.AnchorTransitionUpdate(s, bit)
						for _, pu := range update.PreUpdates {
							if pu.RelativePriority == p {
								tagStart := uint32(groupIdx * 2)
								tagEnd := tagStart + 1
								groupTags := uint64(0)
								if (pu.Tags & (1 << tagStart)) != 0 {
									groupTags |= (1 << tagStart)
								}
								if (pu.Tags & (1 << tagEnd)) != 0 {
									groupTags |= (1 << tagEnd)
								}
								applyTags(groupTags, pos, regs)
								p = pu.NextPriority
								break
							}
						}
					}
					if nextID != s {
						s = nextID
						changed = true
						if s == (toState & ir.StateIDMask) {
							return p
						}
					}
				}
			}
		}
		if !changed {
			break
		}
	}
	return p
}

func (re *Regexp) sparseTDFA_Recap(mc *matchContext, b []byte, start, end, bestPriority int, regs []int) {
	d := re.dfa
	if d == nil {
		return
	}

	p_end := int32(bestPriority % ir.SearchRestartPenalty)
	recapTables := d.RecapTables()
	trans := d.Transitions()

	// Pass 2: Path Selection (Backward)
	mc.pathHistory[end] = p_end
	for i := end; i > start; {
		charStart := i - 1
		for charStart > start && (b[charStart]&0xC0) == 0x80 {
			charStart--
		}
		prevState := mc.history[charStart]
		byteVal := b[charStart]
		idx := (int(prevState&ir.StateIDMask) << 8) | int(byteVal)

		p_out := mc.pathHistory[i]
		bestPIn := int32(1<<30 - 1)

		for _, entry := range recapTables[0].Transitions[idx] {
			if int32(entry.NextPriority) == p_out {
				if int32(entry.InputPriority) < bestPIn {
					bestPIn = int32(entry.InputPriority)
				}
			}
		}
		mc.pathHistory[charStart] = bestPIn
		i = charStart
	}

	// Pass 3: Forward Submatch Extraction
	for j := 2; j < len(regs); j++ {
		regs[j] = -1
	}

	for groupIdx := 0; groupIdx <= d.NumSubexp(); groupIdx++ {
		p := mc.pathHistory[start]
		for _, u := range d.StartUpdates() {
			if u.NextPriority == p {
				tagStart := uint32(groupIdx * 2)
				tagEnd := tagStart + 1
				groupTags := uint64(0)
				if (u.Tags & (1 << tagStart)) != 0 {
					groupTags |= (1 << tagStart)
				}
				if (u.Tags & (1 << tagEnd)) != 0 {
					groupTags |= (1 << tagEnd)
				}
				applyTags(groupTags, start, regs)
				break
			}
		}

		initialState := d.SearchState()
		if re.anchorStart {
			initialState = d.MatchState()
		}
		p = re.followPathAnchorsGroup(d, initialState, mc.history[start], ir.CalculateContext(b, start), start, regs, p, groupIdx)

		for i := start; i < end; {
			byteVal := b[i]
			prevState := mc.history[i]
			idx := (int(prevState&ir.StateIDMask) << 8) | int(byteVal)

			nextI := i + 1
			for nextI < end && (b[nextI]&0xC0) == 0x80 {
				nextI++
			}

			p_next_target := mc.pathHistory[nextI]

			// Apply tags from recap table
			for _, entry := range recapTables[groupIdx].Transitions[idx] {
				if int32(entry.InputPriority) == p && int32(entry.NextPriority) == p_next_target {
					applyTags(entry.PreTags, i, regs)
					applyTags(entry.PostTags, nextI, regs)
					p = int32(entry.NextPriority)
					break
				}
			}

			stateAfterByte := trans[idx] & ir.StateIDMask
			p = re.followPathAnchorsGroup(d, stateAfterByte, mc.history[nextI], ir.CalculateContext(b, nextI), nextI, regs, p, groupIdx)
			i = nextI
		}

		for _, u := range d.MatchUpdates(mc.history[end]) {
			if u.RelativePriority == mc.pathHistory[end] {
				tagStart := uint32(groupIdx * 2)
				tagEnd := tagStart + 1
				groupTags := uint64(0)
				if (u.Tags & (1 << tagStart)) != 0 {
					groupTags |= (1 << tagStart)
				}
				if (u.Tags & (1 << tagEnd)) != 0 {
					groupTags |= (1 << tagEnd)
				}
				applyTags(groupTags, end, regs)
				break
			}
		}
	}
}

func (re *Regexp) burnedRecap(mc *matchContext, b []byte, start, end, bestPriority int, regs []int) {
	// Fallback or legacy support
	re.sparseTDFA_Recap(mc, b, start, end, bestPriority, regs)
}
