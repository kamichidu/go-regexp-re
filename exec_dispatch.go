package regexp

import (
	"bytes"
	"unsafe"

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

func (re *Regexp) findSubmatchIndexInternal(b []byte, mc *matchContext, regs []int) (int, int, int) {
	switch re.strategy {
	case strategyLiteral:
		res := re.literalMatcher.FindSubmatchIndex(b)
		if res == nil {
			return -1, -1, 0
		}
		return res[0], res[1], 0
	case strategyFast, strategyExtended:
		if re.mapAnchor != nil {
			a := re.mapAnchor
			input := b

			// Handle BeginText anchor (^)
			if a.HasBeginText {
				// If it's a true BeginText anchor (not multiline), it MUST be at 0
				if bytes.HasPrefix(b, a.Anchor) {
					if _, ok := a.Validate(b, 0); ok {
						startSearch := 0
						var start, end, prio int
						if mc == nil {
							start, end, prio = re.match(b[startSearch:])
						} else {
							mc.prepare(len(b[startSearch:]), re.numSubexp)
							start, end, prio = re.submatch(b[startSearch:], mc)
						}
						if start >= 0 {
							return start + startSearch, end + startSearch, prio
						}
					}
				}
				// If we have ^ and it's not at the start, it will never match (non-multiline)
				return -1, -1, 0
			}

			// Standard Pivot/Suffix search
			for {
				var pos int
				if a.HasClass {
					pos = ir.IndexClass(a.Class, input)
				} else {
					pos = bytes.Index(input, a.Anchor)
				}

				if pos < 0 {
					// If it's a Suffix type anchor and search fails, no match possible
					return -1, -1, 0
				}

				absolutePos := (len(b) - len(input)) + pos
				if _, ok := a.Validate(b, absolutePos); ok {
					// Constraints satisfied, start DFA Pass 1
					startSearch := absolutePos - a.Distance
					if startSearch < 0 {
						startSearch = 0
					}

					var start, end, prio int
					if mc == nil {
						start, end, prio = re.match(b[startSearch:])
					} else {
						mc.prepare(len(b[startSearch:]), re.numSubexp)
						start, end, prio = re.submatch(b[startSearch:], mc)
					}

					if start >= 0 {
						return start + startSearch, end + startSearch, prio
					}
				}

				input = input[pos+1:]
				if len(input) < len(a.Anchor) {
					return -1, -1, 0
				}
			}
		}

		if mc == nil {
			return re.match(b)
		}
		mc.prepare(len(b), re.numSubexp)
		return re.submatch(b, mc)
	}
	return -1, -1, 0
}

func (re *Regexp) match(b []byte) (int, int, int) {
	switch re.strategy {
	case strategyExtended:
		return extendedMatchExecLoop(re, b)
	default:
		return fastMatchExecLoop(re, b)
	}
}

func (re *Regexp) submatch(b []byte, mc *matchContext) (int, int, int) {
	// Submatch always uses the extended loop because it needs to record history
	return extendedSubmatchExecLoop(re, b, mc)
}

func (re *Regexp) Match(b []byte) bool {
	start, _, _ := re.findSubmatchIndexInternal(b, nil, nil)
	return start >= 0
}

func (re *Regexp) MatchString(s string) bool {
	b := unsafe.Slice(unsafe.StringData(s), len(s))
	return re.Match(b)
}
