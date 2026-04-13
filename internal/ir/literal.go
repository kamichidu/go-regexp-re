package ir

import (
	"bytes"
	"regexp/syntax"
)

// LiteralMatcher is a fast matcher for patterns consisting only of literals and anchors (^, $).
type LiteralMatcher interface {
	Match(b []byte) bool
	FindSubmatchIndex(b []byte) []int
}

type literalStrategy interface {
	Exec(input, lit []byte) (bool, int, int)
}

type exactStrategy struct{}

func (exactStrategy) Exec(input, lit []byte) (bool, int, int) {
	if bytes.Equal(input, lit) {
		return true, 0, len(input)
	}
	return false, 0, 0
}

type prefixStrategy struct{}

func (prefixStrategy) Exec(input, lit []byte) (bool, int, int) {
	if bytes.HasPrefix(input, lit) {
		return true, 0, len(lit)
	}
	return false, 0, 0
}

type suffixStrategy struct{}

func (suffixStrategy) Exec(input, lit []byte) (bool, int, int) {
	if len(input) >= len(lit) && bytes.HasSuffix(input, lit) {
		return true, len(input) - len(lit), len(input)
	}
	return false, 0, 0
}

type containsStrategy struct{}

func (containsStrategy) Exec(input, lit []byte) (bool, int, int) {
	i := bytes.Index(input, lit)
	if i >= 0 {
		return true, i, i + len(lit)
	}
	return false, 0, 0
}

type genericLiteralMatcher[S literalStrategy] struct {
	literal     []byte
	capTemplate []int // Relative offset template [start0, end0, start1, end1, ...]
	strategy    S
}

func (m *genericLiteralMatcher[S]) Match(b []byte) bool {
	matched, _, _ := m.strategy.Exec(b, m.literal)
	return matched
}

func (m *genericLiteralMatcher[S]) FindSubmatchIndex(b []byte) []int {
	matched, start, _ := m.strategy.Exec(b, m.literal)
	if !matched {
		return nil
	}
	res := make([]int, len(m.capTemplate))
	for i, offset := range m.capTemplate {
		if offset < 0 {
			res[i] = -1
		} else {
			res[i] = start + offset
		}
	}
	return res
}

// AnalyzeLiteralPattern returns a LiteralMatcher if the Regexp can be specialized as a literal match.
// totalCaps is the total number of capturing groups (including Group 0) expected by the caller.
func AnalyzeLiteralPattern(re *syntax.Regexp, totalCaps int) LiteralMatcher {
	var beginText, endText bool
	var literal []byte
	var capTemplate []int

	capTemplate = make([]int, totalCaps*2)
	for i := range capTemplate {
		capTemplate[i] = -1
	}

	// Analyze if the pattern is a simple combination of literals and anchors
	if !extractLiteral(re, &beginText, &endText, &literal, capTemplate, 0) {
		return nil
	}

	// Set Group 0 for the full match
	capTemplate[0] = 0
	capTemplate[1] = len(literal)

	if beginText && endText {
		return &genericLiteralMatcher[exactStrategy]{literal, capTemplate, exactStrategy{}}
	}
	if beginText {
		return &genericLiteralMatcher[prefixStrategy]{literal, capTemplate, prefixStrategy{}}
	}
	if endText {
		return &genericLiteralMatcher[suffixStrategy]{literal, capTemplate, suffixStrategy{}}
	}
	return &genericLiteralMatcher[containsStrategy]{literal, capTemplate, containsStrategy{}}
}

func extractLiteral(re *syntax.Regexp, beginText, endText *bool, literal *[]byte, capTemplate []int, offset int) bool {
	switch re.Op {
	case syntax.OpEmptyMatch:
		return true
	case syntax.OpBeginText:
		if offset != 0 {
			return false // ^ anchor is only allowed at the beginning
		}
		*beginText = true
		return true
	case syntax.OpEndText:
		*endText = true
		return true
	case syntax.OpLiteral:
		if re.Flags&syntax.FoldCase != 0 {
			return false
		}
		*literal = append(*literal, string(re.Rune)...)
		return true
	case syntax.OpCapture:
		capTemplate[re.Cap*2] = offset
		if !extractLiteral(re.Sub[0], beginText, endText, literal, capTemplate, offset) {
			return false
		}
		capTemplate[re.Cap*2+1] = len(*literal)
		return true
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			if !extractLiteral(sub, beginText, endText, literal, capTemplate, len(*literal)) {
				return false
			}
		}
		return true
	case syntax.OpCharClass:
		// Single character classes could be treated as literals, but skipped for now
		return false
	default:
		return false
	}
}
