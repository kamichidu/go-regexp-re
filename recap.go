package regexp

import (
	"fmt"
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

func (re *Regexp) followPathAnchors(d *ir.DFA, fromState, toState ir.StateID, context syntax.EmptyOp, pos int, regs []int, p_in int32) int32 {
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
								applyTags(pu.Tags, pos, regs)
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

	trans := d.Transitions()
	p_end := int32(bestPriority % ir.SearchRestartPenalty)

	// Step 1: Find the priority path from start to end that leads to p_end.
	// This is Pass 2.
	mc.pathHistory[end] = p_end

	for i := end; i > start; {
		p_out := mc.pathHistory[i]
		charStart := i - 1
		for charStart > start && (b[charStart]&0xC0) == 0x80 {
			charStart--
		}
		prevState := mc.history[charStart]
		byteVal := b[charStart]
		idx := (int(prevState&ir.StateIDMask) << 8) | int(byteVal)
		rawNext := trans[idx]
		stateAfterByte := rawNext & ir.StateIDMask
		if (rawNext & ir.WarpStateFlag) == 0 {
			charStart = i - 1
			byteVal = b[charStart]
			idx = (int(prevState&ir.StateIDMask) << 8) | int(byteVal)
			rawNext = trans[idx]
			stateAfterByte = rawNext & ir.StateIDMask
		}

		bestPIn := int32(1<<30 - 1)
		for p_in := int32(0); p_in < 1000; p_in++ {
			found := false
			if rawNext < 0 {
				update := d.TransitionUpdate(prevState&ir.StateIDMask, byteVal)
				p_base := update.BasePriority
				for _, pu_pre := range update.PreUpdates {
					if pu_pre.RelativePriority == p_in {
						p_mid := pu_pre.NextPriority
						found_inner := false
						for _, pu_post := range update.PostUpdates {
							if pu_post.RelativePriority == p_mid {
								if d.CanReachPriority(stateAfterByte, mc.history[i], ir.CalculateContext(b, i), pu_post.NextPriority+p_base, p_out) {
									found_inner = true
									break
								}
							}
						}
						if !found_inner && d.CanReachPriority(stateAfterByte, mc.history[i], ir.CalculateContext(b, i), p_mid+p_base, p_out) {
							found_inner = true
						}
						if found_inner {
							found = true
							break
						}
					}
				}
			} else {
				if d.CanReachPriority(stateAfterByte, mc.history[i], ir.CalculateContext(b, i), p_in, p_out) {
					found = true
				}
			}
			if found {
				bestPIn = p_in
				break
			}
		}
		fmt.Printf("Pass 2: i=%d, p_out=%d -> charStart=%d, p_in=%d\n", i, p_out, charStart, bestPIn)
		mc.pathHistory[charStart] = bestPIn
		i = charStart
	}

	// Step 2: Forward pass to apply tags.
	// This is Pass 3.
	p := mc.pathHistory[start]
	fmt.Printf("Pass 3: start=%d, p_initial=%d\n", start, p)
	for _, u := range d.StartUpdates() {
		if u.NextPriority == p {
			applyTags(u.Tags, start, regs)
			break
		}
	}

	initialState := d.SearchState()
	if re.anchorStart {
		initialState = d.MatchState()
	}
	p = re.followPathAnchors(d, initialState, mc.history[start], ir.CalculateContext(b, start), start, regs, p)

	for i := start; i < end; {
		byteVal := b[i]
		prevState := mc.history[i]
		idx := (int(prevState&ir.StateIDMask) << 8) | int(byteVal)
		rawNext := trans[idx]
		stateAfterByte := rawNext & ir.StateIDMask

		nextI := i + 1
		if (rawNext & ir.WarpStateFlag) != 0 {
			skip := bits.LeadingZeros8(^byteVal) - 1
			if skip < 0 {
				skip = 0
			}
			nextI = i + 1 + skip
		}
		if nextI > end {
			nextI = end
		}
		p_next_target := mc.pathHistory[nextI]
		fmt.Printf("Pass 3: i=%d, p=%d, p_next_target=%d\n", i, p, p_next_target)
		if rawNext < 0 {
			update := d.TransitionUpdate(prevState&ir.StateIDMask, byteVal)
			p_base := update.BasePriority
			for _, pu_pre := range update.PreUpdates {
				if pu_pre.RelativePriority == p {
					p_mid := pu_pre.NextPriority
					found := false
					for _, pu_post := range update.PostUpdates {
						if pu_post.RelativePriority == p_mid {
							if d.CanReachPriority(stateAfterByte, mc.history[nextI], ir.CalculateContext(b, nextI), pu_post.NextPriority+p_base, p_next_target) {
								fmt.Printf("  Transition Match! preTags=%x, postTags=%x\n", pu_pre.Tags, pu_post.Tags)
								applyTags(pu_pre.Tags, i, regs)
								applyTags(pu_post.Tags, nextI, regs)
								p = re.followPathAnchors(d, stateAfterByte, mc.history[nextI], ir.CalculateContext(b, nextI), nextI, regs, pu_post.NextPriority+p_base)
								found = true
								break
							}
						}
					}
					if !found && d.CanReachPriority(stateAfterByte, mc.history[nextI], ir.CalculateContext(b, nextI), p_mid+p_base, p_next_target) {
						fmt.Printf("  Transition Match (pre only)! preTags=%x\n", pu_pre.Tags)
						applyTags(pu_pre.Tags, i, regs)
						p = re.followPathAnchors(d, stateAfterByte, mc.history[nextI], ir.CalculateContext(b, nextI), nextI, regs, p_mid+p_base)
						found = true
					}
					if found {
						break
					}
				}
			}
		} else {
			p = re.followPathAnchors(d, stateAfterByte, mc.history[nextI], ir.CalculateContext(b, nextI), nextI, regs, p)
		}
		i = nextI
	}
	for _, u := range d.MatchUpdates(mc.history[end]) {
		if u.RelativePriority == mc.pathHistory[end] {
			fmt.Printf("Pass 3: Final MatchTags=%x\n", u.Tags)
			applyTags(u.Tags, end, regs)
			break
		}
	}
}

func (re *Regexp) burnedRecap(mc *matchContext, b []byte, start, end, bestPriority int, regs []int) {
	d := re.dfa
	if d == nil {
		return
	}
	trans := d.Transitions()
	p_end := int32(bestPriority % ir.SearchRestartPenalty)
	mc.pathHistory[end] = p_end

	for i := end; i > start; {
		p_out := mc.pathHistory[i]
		charStart := i - 1
		for charStart > start && (b[charStart]&0xC0) == 0x80 {
			charStart--
		}
		prevState := mc.history[charStart]
		byteVal := b[charStart]
		idx := (int(prevState&ir.StateIDMask) << 8) | int(byteVal)
		rawNext := trans[idx]
		stateAfterByte := rawNext & ir.StateIDMask
		if (rawNext & ir.WarpStateFlag) == 0 {
			charStart = i - 1
			byteVal = b[charStart]
			idx = (int(prevState&ir.StateIDMask) << 8) | int(byteVal)
			rawNext = trans[idx]
			stateAfterByte = rawNext & ir.StateIDMask
		}
		bestPIn := int32(1<<30 - 1)
		for p_in := int32(0); p_in < 1000; p_in++ {
			found := false
			if rawNext < 0 {
				update := d.TransitionUpdate(prevState&ir.StateIDMask, byteVal)
				for _, pu := range update.PreUpdates {
					if pu.RelativePriority == p_in {
						tempMid := pu.NextPriority
						for _, pu_post := range update.PostUpdates {
							if pu_post.RelativePriority == tempMid {
								if d.CanReachPriority(stateAfterByte, mc.history[i], ir.CalculateContext(b, i), pu_post.NextPriority, p_out) {
									found = true
									break
								}
							}
						}
						if !found && d.CanReachPriority(stateAfterByte, mc.history[i], ir.CalculateContext(b, i), tempMid, p_out) {
							found = true
						}
						if found {
							break
						}
					}
				}
			} else {
				if d.CanReachPriority(stateAfterByte, mc.history[i], ir.CalculateContext(b, i), p_in, p_out) {
					found = true
				}
			}
			if found {
				bestPIn = p_in
				break
			}
		}
		mc.pathHistory[charStart] = bestPIn
		i = charStart
	}

	p := mc.pathHistory[start]
	for _, u := range d.StartUpdates() {
		if u.NextPriority == p {
			applyTags(u.Tags, start, regs)
			break
		}
	}
	initialState := d.SearchState()
	if re.anchorStart {
		initialState = d.MatchState()
	}
	p = re.followPathAnchors(d, initialState, mc.history[start], ir.CalculateContext(b, start), start, regs, p)
	for i := start; i < end; {
		byteVal := b[i]
		prevState := mc.history[i]
		idx := (int(prevState&ir.StateIDMask) << 8) | int(byteVal)
		rawNext := trans[idx]
		stateAfterByte := rawNext & ir.StateIDMask
		nextI := i + 1
		if (rawNext & ir.WarpStateFlag) != 0 {
			skip := bits.LeadingZeros8(^byteVal) - 1
			if skip < 0 {
				skip = 0
			}
			nextI = i + 1 + skip
		}
		if nextI > end {
			nextI = end
		}
		p_next_target := mc.pathHistory[nextI]
		if rawNext < 0 {
			update := d.TransitionUpdate(prevState&ir.StateIDMask, byteVal)
			for _, pu_pre := range update.PreUpdates {
				if pu_pre.RelativePriority == p {
					p_mid := pu_pre.NextPriority
					found := false
					for _, pu_post := range update.PostUpdates {
						if pu_post.RelativePriority == p_mid {
							if d.CanReachPriority(stateAfterByte, mc.history[nextI], ir.CalculateContext(b, nextI), pu_post.NextPriority, p_next_target) {
								applyTags(pu_pre.Tags, i, regs)
								applyTags(pu_post.Tags, nextI, regs)
								p = re.followPathAnchors(d, stateAfterByte, mc.history[nextI], ir.CalculateContext(b, nextI), nextI, regs, pu_post.NextPriority)
								found = true
								break
							}
						}
					}
					if !found && d.CanReachPriority(stateAfterByte, mc.history[nextI], ir.CalculateContext(b, nextI), p_mid, p_next_target) {
						applyTags(pu_pre.Tags, i, regs)
						p = re.followPathAnchors(d, stateAfterByte, mc.history[nextI], ir.CalculateContext(b, nextI), nextI, regs, p_mid)
						found = true
					}
					if found {
						break
					}
				}
			}
		} else {
			p = re.followPathAnchors(d, stateAfterByte, mc.history[nextI], ir.CalculateContext(b, nextI), nextI, regs, p)
		}
		i = nextI
	}
	for _, u := range d.MatchUpdates(mc.history[end]) {
		if u.RelativePriority == mc.pathHistory[end] {
			applyTags(u.Tags, end, regs)
			break
		}
	}
}
