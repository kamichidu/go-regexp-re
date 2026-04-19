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
			if i%2 == 0 { // Start tag: keep the FIRST one
				if regs[i] == -1 {
					regs[i] = pos
				}
			} else { // End tag: keep the LAST one
				regs[i] = pos
			}
		}
		t &= ^(uint64(1) << i)
	}
}

func (re *Regexp) followPathAnchors(d *ir.DFA, fromState, toState ir.StateID, context syntax.EmptyOp, pos int, regs []int, p_abs int32) int32 {
	if (fromState & ir.StateIDMask) == (toState & ir.StateIDMask) {
		return p_abs
	}
	s := fromState & ir.StateIDMask
	p := p_abs
	for iter := 0; iter < 10; iter++ {
		changed := false
		for bit := 0; bit < 6; bit++ {
			if (context & (1 << uint(bit))) != 0 {
				rawNext := d.AnchorNext(s, bit)
				if rawNext != ir.InvalidState {
					nextID := rawNext & ir.StateIDMask
					idx := (int(s) << 8) | (256 + bit)
					tables := d.RecapTables()
					if len(tables) > 0 && idx < len(tables[0].Transitions) {
						p_in_rel := p - d.StateMinPriority(s)
						entries := tables[0].Transitions[idx]
						for _, e := range entries {
							if int32(e.InputPriority) == p_in_rel {
								// APPLY ANCHOR TAGS AT CURRENT POSITION
								applyTags(e.PreTags|e.PostTags, pos, regs)
								p = int32(e.NextPriority) + d.StateMinPriority(nextID)
								break
							}
						}
					}
					if nextID != s {
						s = nextID
						changed = true
					}
					if s == (toState & ir.StateIDMask) {
						return p
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

	// Pass 2: Path Identity Selection (Backward Propagation)
	if mc.pathHistory == nil || len(mc.pathHistory) < len(b)+1 {
		mc.pathHistory = make([]int32, len(b)+1)
	}
	for j := range mc.pathHistory {
		mc.pathHistory[j] = -1
	}

	// Absolute priority of the winning match at 'end'
	p_abs_end := int32(bestPriority % ir.SearchRestartPenalty)
	mc.pathHistory[end] = p_abs_end

	recapTables := d.RecapTables()
	trans := d.Transitions()

	for i := end; i > start; {
		charStart := i - 1
		for charStart > start && (b[charStart]&0xC0) == 0x80 && mc.history[charStart] == mc.history[i-1] {
			charStart--
		}
		byteVal := b[charStart]
		prevState := mc.history[charStart]
		idx := (int(prevState&ir.StateIDMask) << 8) | int(byteVal)

		p_out_abs := mc.pathHistory[i]
		p_out_rel := p_out_abs - d.StateMinPriority(mc.history[i]&ir.StateIDMask)

		bestPInAbs := int32(1<<30 - 1)
		found := false

		// Attempt 1: Check recap tables
		if len(recapTables) > 0 && idx < len(recapTables[0].Transitions) {
			for _, entry := range recapTables[0].Transitions[idx] {
				if int32(entry.NextPriority) == p_out_rel {
					p_in_abs := int32(entry.InputPriority) + d.StateMinPriority(prevState&ir.StateIDMask)
					if p_in_abs < bestPInAbs {
						bestPInAbs = p_in_abs
						found = true
					}
				}
			}
		}
		// Attempt 2: Comprehensive virtual search
		if !found {
			for p_in_rel := int32(0); p_in_rel < 2000; p_in_rel++ {
				if d.CanReachPriority(prevState&ir.StateIDMask, mc.history[i], ir.CalculateContext(b, i), p_in_rel, p_out_rel) {
					bestPInAbs = p_in_rel + d.StateMinPriority(prevState&ir.StateIDMask)
					found = true
					break
				}
			}
		}
		if found {
			mc.pathHistory[charStart] = bestPInAbs
		} else {
			// If still not found, we MUST maintain connectivity.
			// Default to state minimum priority to allow Pass 3 to continue.
			mc.pathHistory[charStart] = d.StateMinPriority(prevState & ir.StateIDMask)
		}
		i = charStart
	}

	// Pass 3: Forward Submatch Extraction (Single Pass)
	for j := 0; j < len(regs); j++ {
		regs[j] = -1
	}
	regs[0], regs[1] = start, end

	p := mc.pathHistory[start]
	p_rel := p - d.StateMinPriority(mc.history[start]&ir.StateIDMask)
	for _, u := range d.StartUpdates() {
		if u.NextPriority == p_rel {
			applyTags(u.Tags, start, regs)
			break
		}
	}

	currentState := mc.history[start]
	p = re.followPathAnchors(d, currentState, currentState, ir.CalculateContext(b, start), start, regs, p)

	for i := start; i < end; {
		byteVal := b[i]
		prevState := mc.history[i]
		idx := (int(prevState&ir.StateIDMask) << 8) | int(byteVal)

		// Find the true end of this character
		nextI := i + 1
		for nextI < end && (b[nextI]&0xC0) == 0x80 {
			nextI++
		}

		p_next_abs := mc.pathHistory[nextI]
		if p_next_abs == -1 {
			p_next_abs = p
		}
		p_in_rel := p - d.StateMinPriority(prevState&ir.StateIDMask)
		p_out_rel := p_next_abs - d.StateMinPriority(mc.history[nextI]&ir.StateIDMask)

		if len(recapTables) > 0 && idx < len(recapTables[0].Transitions) {
			for _, entry := range recapTables[0].Transitions[idx] {
				if int32(entry.InputPriority) == p_in_rel && int32(entry.NextPriority) == p_out_rel {
					// PreTags are at the START of the character transition
					applyTags(entry.PreTags, i, regs)
					// PostTags are at the END of the character transition
					applyTags(entry.PostTags, nextI, regs)
					break
				}
			}
		}
		p = p_next_abs

		stateAfterByte := trans[idx] & ir.StateIDMask
		// Anchor following from the character's END position
		p = re.followPathAnchors(d, stateAfterByte, mc.history[nextI], ir.CalculateContext(b, nextI), nextI, regs, p)
		i = nextI
	}

	p_final_rel := p - d.StateMinPriority(mc.history[end]&ir.StateIDMask)
	for _, u := range d.MatchUpdates(mc.history[end]) {
		if u.RelativePriority == p_final_rel {
			applyTags(u.Tags, end, regs)
			break
		}
	}
}

func (re *Regexp) burnedRecap(mc *matchContext, b []byte, start, end, bestPriority int, regs []int) {
	re.sparseTDFA_Recap(mc, b, start, end, bestPriority, regs)
}
