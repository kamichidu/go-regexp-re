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
		if len(re.mapAnchors) > 0 {
			// Try each anchor candidate.
			// This allows patterns like (^|[abc])feb to have multiple entry points.
			for _, a := range re.mapAnchors {
				input := b

				if a.HasBeginText {
					// Anchored to start of text (^). Only check position 0.
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
							start, end, prio := re.runDFA(b, 0, mc)
							if start >= 0 {
								return start, end, prio
							}
						}
					}
					// If this anchor is strictly ^ and failed at 0, don't search further for THIS anchor.
					continue
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
						break // Try next anchor info
					}

					absolutePos := (len(b) - len(input)) + pos
					if _, ok := a.Validate(b, absolutePos); ok {
						startSearch := absolutePos - a.Distance
						if startSearch < 0 {
							startSearch = 0
						}

						start, end, prio := re.runDFA(b[startSearch:], startSearch, mc)
						if start >= 0 {
							return start, end, prio
						}
					}

					input = input[pos+1:]
					if !a.HasClass && len(input) < len(a.Anchor) {
						break
					}
				}
			}
			// If all MAP anchors failed, no match is possible if they cover all mandatory paths.
			// However, for safety, if there's any ambiguity, we might want to fallback.
			// Currently, ExtractAnchors only extracts mandatory ones.
			return -1, -1, 0
		}

		if mc == nil {
			return re.match(b)
		}
		mc.prepare(len(b), re.numSubexp)
		return re.submatch(b, mc)
	}
	return -1, -1, 0
}

func (re *Regexp) runDFA(b []byte, offset int, mc *matchContext) (int, int, int) {
	var start, end, prio int
	if mc == nil {
		start, end, prio = re.match(b)
	} else {
		mc.prepare(len(b), re.numSubexp)
		start, end, prio = re.submatch(b, mc)
	}
	if start >= 0 {
		return start + offset, end + offset, prio
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
