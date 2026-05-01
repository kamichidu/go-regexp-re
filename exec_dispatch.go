package regexp

import (
	"github.com/kamichidu/go-regexp-re/internal/ir"
)

type matchStrategy uint8

const (
	strategyNone matchStrategy = iota
	strategyLiteral
	strategyFast
	strategyExtended
)

func (re *Regexp) bindMatchStrategy() {
	if re.literalMatcher != nil {
		re.strategy = strategyLiteral
		return
	}

	// If the pattern has no capturing groups and no complex priority shifts,
	// we can use the fastest match loop.
	if re.numSubexp == 0 && (re.dfa == nil || !re.dfa.HasAnchors()) {
		re.strategy = strategyFast
	} else {
		re.strategy = strategyExtended
	}
}

func (re *Regexp) findIndexAt(b []byte, pos int, totalBytes int, originalB []byte) (int, int, int) {
	in := ir.Input{
		B:           b,
		OriginalB:   originalB,
		AbsPos:      pos,
		TotalBytes:  totalBytes,
		SearchStart: 0,
		SearchEnd:   len(b),
	}

	var start, end, prio int
	switch re.strategy {
	case strategyLiteral:
		res := re.literalMatcher.FindSubmatchIndex(in)
		if res == nil {
			return -1, -1, 0
		}
		start, end, prio = res[0], res[1], 0
	default:
		start, end, prio = re.match(&in)
	}

	if start >= 0 {
		return start + pos, end + pos, prio
	}
	return -1, -1, 0
}

func (re *Regexp) findSubmatchIndexAt(b []byte, pos int, totalBytes int, originalB []byte) []int {
	in := ir.Input{
		B:           b,
		OriginalB:   originalB,
		AbsPos:      pos,
		TotalBytes:  totalBytes,
		SearchStart: 0,
		SearchEnd:   len(b),
	}

	if re.strategy == strategyLiteral {
		regs := make([]int, (re.numSubexp+1)*2)
		for i := range regs {
			regs[i] = -1
		}
		if !re.literalMatcher.FindSubmatchIndexInto(in, regs) {
			return nil
		}
		// Adjust to absolute
		for i := range regs {
			if regs[i] >= 0 {
				regs[i] += pos
			}
		}
		return regs
	}

	mc := matchContextPool.Get().(*matchContext)
	defer matchContextPool.Put(mc)
	mc.prepare(len(b), re.numSubexp, pos)

	// Pass 0 & 1: Discovery
	matchStart, matchEnd, prio := fastDiscoveryLoop(re, &in)
	if matchStart < 0 {
		return nil
	}

	// Pass 2: Anchored Recording
	prio = anchoredRecordingLoop(re, &in, mc, matchStart, matchEnd)

	// Pass 3 & 4: Extraction
	regs := mc.regs
	re.sparseTDFA_PathSelection(mc, b, matchStart, matchEnd, prio)
	re.sparseTDFA_Recap(mc, b, matchStart, matchEnd, prio, regs)

	res := make([]int, len(mc.regs))
	copy(res, mc.regs)
	return res
}

func (re *Regexp) match(in *ir.Input) (int, int, int) {
	return fastMatchExecLoop(re, in)
}

func (re *Regexp) submatch(in ir.Input, mc *matchContext) (int, int, int) {
	return extendedSubmatchExecLoop(re, in, mc)
}
