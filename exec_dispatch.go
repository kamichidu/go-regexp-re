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
		start, end = re.literalMatcher.FindIndex(&in)
		prio = 0
	default:
		start, end, prio = re.match(&in)
	}

	if start >= 0 {
		return start + pos, end + pos, prio
	}
	return -1, -1, 1<<30 - 1
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
		if ok := re.literalMatcher.FindSubmatchIndexInto(&in, regs); ok {
			// Convert to absolute
			for i := range regs {
				if regs[i] >= 0 {
					regs[i] += pos
				}
			}
			return regs
		}
		return nil
	}

	mc := matchContextPool.Get().(*matchContext)
	defer matchContextPool.Put(mc)

	matchStart, matchEnd, prio := re.submatch(&in, mc)
	if matchStart < 0 {
		return nil
	}

	// Prepare context for submatch extraction
	mc.prepare(len(b), re.numSubexp, pos)

	// Pass 2: Recording
	anchoredRecordingLoop(re, &in, mc, matchStart, matchEnd)

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

func (re *Regexp) submatch(in *ir.Input, mc *matchContext) (int, int, int) {
	return extendedSubmatchExecLoop(re, in, mc)
}
