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

// findIndexAt is the internal entry point for match-only operations.
func (re *Regexp) findIndexAt(b []byte, pos int, totalBytes int) (int, int, int) {
	in := ir.Input{
		B:           b,
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

func (re *Regexp) findSubmatchIndexInternal(b []byte, mc *matchContext, regs []int) (int, int, int) {
	in := ir.Input{
		B:          b,
		AbsPos:     0,
		TotalBytes: len(b),
		SearchEnd:  len(b),
	}
	if mc != nil {
		in.AbsPos = mc.absBase
		in.TotalBytes = mc.absBase + len(b)
	}

	switch re.strategy {
	case strategyLiteral:
		if regs != nil {
			if re.literalMatcher.FindSubmatchIndexInto(in, regs) {
				return regs[0], regs[1], 0
			}
			return -1, -1, 0
		}
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
							start, end, prio := re.submatch(in, mc)
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
						start, end, prio := re.submatch(in, mc)
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
		return re.submatch(in, mc)
	}
	return -1, -1, 0
}

func (re *Regexp) runDFA(in ir.Input, mc *matchContext) (int, int, int) {
	if mc == nil {
		return re.match(in)
	}
	mc.prepare(len(in.B), re.numSubexp, in.AbsPos)
	return re.submatch(in, mc)
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
