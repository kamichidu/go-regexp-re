package regexp

import (
	"bytes"

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

	if re.dfa != nil && re.dfa.HasAnchors() {
		re.strategy = strategyExtended
	} else {
		re.strategy = strategyFast
	}
}

// findIndexAt is the optimized 1-pass path for Match/FindIndex.
func (re *Regexp) findIndexAt(b []byte, pos int, totalBytes int, originalB []byte) (int, int, int) {
	in := ir.Input{
		B:           b,
		OriginalB:   originalB,
		AbsPos:      pos,
		TotalBytes:  totalBytes,
		SearchStart: 0,
		SearchEnd:   len(b),
	}

	switch re.strategy {
	case strategyLiteral:
		res := re.literalMatcher.FindSubmatchIndex(in)
		if res == nil {
			return -1, -1, 0
		}
		return res[0], res[1], 0
	case strategyFast, strategyExtended:
		if len(re.mapAnchors) > 0 {
			for _, a := range re.mapAnchors {
				input := b
				if a.HasBeginText {
					match := false
					if a.HasClass {
						if len(b) > 0 && ir.IndexClass(a.Class, b[:1]) == 0 {
							match = true
						}
					} else {
						if bytes.HasPrefix(b, a.Anchor) {
							match = true
						}
					}
					if match {
						if _, ok := a.Validate(b, 0); ok {
							in.SearchStart = 0
							start, end, prio := re.match(in)
							if start >= 0 {
								return start, end, prio
							}
						}
					}
					continue
				}

				for {
					var posMatch int
					if a.HasClass {
						posMatch = ir.IndexClass(a.Class, input)
					} else {
						posMatch = bytes.Index(input, a.Anchor)
					}
					if posMatch < 0 {
						break
					}
					absolutePos := (len(b) - len(input)) + posMatch
					if _, ok := a.Validate(b, absolutePos); ok {
						startSearch := absolutePos - a.Distance
						if startSearch < 0 {
							startSearch = 0
						}
						in.SearchStart = startSearch
						start, end, prio := re.match(in)
						if start >= 0 {
							return start, end, prio
						}
					}
					input = input[posMatch+1:]
					if !a.HasClass && len(input) < len(a.Anchor) {
						break
					}
				}
			}
			return -1, -1, 0
		}
		return re.match(in)
	}
	return -1, -1, 0
}

// findSubmatchIndexAt is the 3-pass path for Submatch APIs.
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
		// Results are already absolute-ish if literalMatcher handles it,
		// but standard is relative to B[0]. findSubmatchIndexAt should return relative.
		return regs
	}

	mc := matchContextPool.Get().(*matchContext)
	defer matchContextPool.Put(mc)
	mc.prepare(len(b), re.numSubexp, 0) // Prepare with relative 0

	start, end, prio := re.findSubmatchIndexInternal(b, mc, mc.regs, originalB)
	if start < 0 {
		return nil
	}

	regs := mc.regs
	regs[0], regs[1] = start, end
	if re.numSubexp > 0 {
		re.sparseTDFA_PathSelection(mc, b, start, end, prio)
		re.sparseTDFA_Recap(mc, b, start, end, prio, regs)
	}

	// Final conversion to absolute coordinates at the boundary
	for i := range regs {
		if regs[i] >= 0 {
			regs[i] += pos
		}
	}

	res := make([]int, len(mc.regs))
	copy(res, mc.regs)
	return res
}

func (re *Regexp) findSubmatchIndexInternal(b []byte, mc *matchContext, regs []int, originalB []byte) (int, int, int) {
	in := ir.Input{
		B:           b,
		OriginalB:   originalB,
		AbsPos:      0, // Internal scan always treats B[0] as 0
		TotalBytes:  len(originalB),
		SearchEnd:   len(b),
		SearchStart: 0,
	}
	if mc != nil {
		in.AbsPos = mc.absBase // Use original offset for anchor context
	}

	switch re.strategy {
	case strategyFast, strategyExtended:
		// Logic remains same, calling re.match or re.submatch
		return re.submatch(in, mc)
	}
	return -1, -1, 0
}

func (re *Regexp) match(in ir.Input) (int, int, int) {
	switch re.strategy {
	case strategyExtended:
		return extendedMatchExecLoop(re, in)
	default:
		return fastMatchExecLoop(re, in)
	}
}

func (re *Regexp) submatch(in ir.Input, mc *matchContext) (int, int, int) {
	return extendedSubmatchExecLoop(re, in, mc)
}
