package ir

import (
	"bytes"
	"regexp/syntax"
	"unicode/utf8"
)

// LiteralStrategy defines how the literal should be matched.
type LiteralStrategy uint8

const (
	LiteralStrategyExact LiteralStrategy = iota
	LiteralStrategyPrefix
	LiteralStrategySuffix
	LiteralStrategyContains
)

// LiteralMatcher is a fast matcher for patterns consisting only of literals and anchors (^, $).
// It is a concrete struct to avoid interface overhead in hot loops.
type LiteralMatcher struct {
	Literal     []byte
	CapTemplate []int // Relative offset template [start0, end0, start1, end1, ...]
	Strategy    LiteralStrategy
}

// Match reports whether the input matches the literal pattern.
func (m *LiteralMatcher) Match(in *Input) bool {
	switch m.Strategy {
	case LiteralStrategyExact:
		return in.AbsPos == 0 && len(in.B) == in.TotalBytes && bytes.Equal(in.B, m.Literal)
	case LiteralStrategyPrefix:
		return in.AbsPos == 0 && bytes.HasPrefix(in.B, m.Literal)
	case LiteralStrategySuffix:
		return in.AbsPos+len(in.B) == in.TotalBytes && bytes.HasSuffix(in.B, m.Literal)
	case LiteralStrategyContains:
		return bytes.Index(in.B, m.Literal) >= 0
	}
	return false
}

// FindIndex returns the match boundaries [start, end] for the literal pattern without allocation.
func (m *LiteralMatcher) FindIndex(in *Input) (int, int) {
	switch m.Strategy {
	case LiteralStrategyExact:
		if in.AbsPos == 0 && len(in.B) == in.TotalBytes && bytes.Equal(in.B, m.Literal) {
			return 0, len(in.B)
		}
	case LiteralStrategyPrefix:
		if in.AbsPos == 0 && bytes.HasPrefix(in.B, m.Literal) {
			return 0, len(m.Literal)
		}
	case LiteralStrategySuffix:
		if in.AbsPos+len(in.B) == in.TotalBytes && bytes.HasSuffix(in.B, m.Literal) {
			return len(in.B) - len(m.Literal), len(in.B)
		}
	case LiteralStrategyContains:
		if i := bytes.Index(in.B, m.Literal); i >= 0 {
			return i, i + len(m.Literal)
		}
	}
	return -1, -1
}

// FindSubmatchIndexInto populates regs with the submatch indices for the literal pattern.
func (m *LiteralMatcher) FindSubmatchIndexInto(in *Input, regs []int) bool {
	var start int
	var matched bool

	switch m.Strategy {
	case LiteralStrategyExact:
		if in.AbsPos == 0 && len(in.B) == in.TotalBytes && bytes.Equal(in.B, m.Literal) {
			matched, start = true, 0
		}
	case LiteralStrategyPrefix:
		if in.AbsPos == 0 && bytes.HasPrefix(in.B, m.Literal) {
			matched, start = true, 0
		}
	case LiteralStrategySuffix:
		if in.AbsPos+len(in.B) == in.TotalBytes && bytes.HasSuffix(in.B, m.Literal) {
			matched, start = true, len(in.B)-len(m.Literal)
		}
	case LiteralStrategyContains:
		if i := bytes.Index(in.B, m.Literal); i >= 0 {
			matched, start = true, i
		}
	}

	if !matched {
		return false
	}

	if len(regs) < len(m.CapTemplate) {
		return true
	}

	for i, offset := range m.CapTemplate {
		if offset < 0 {
			regs[i] = -1
		} else {
			regs[i] = start + offset
		}
	}
	return true
}

// FindSubmatchIndex returns the submatch indices for the literal pattern.
func (m *LiteralMatcher) FindSubmatchIndex(in *Input) []int {
	res := make([]int, len(m.CapTemplate))
	for i := range res {
		res[i] = -1
	}
	if !m.FindSubmatchIndexInto(in, res) {
		return nil
	}
	return res
}

// AnalyzeLiteralPattern returns a *LiteralMatcher if the Regexp can be specialized as a literal match.
func AnalyzeLiteralPattern(re *syntax.Regexp, totalCaps int) *LiteralMatcher {
	var beginText, endText bool
	var literal []byte
	var capTemplate []int

	capTemplate = make([]int, totalCaps*2)
	for i := range capTemplate {
		capTemplate[i] = -1
	}

	if !extractLiteral(re, &beginText, &endText, &literal, capTemplate, 0) {
		return nil
	}

	capTemplate[0] = 0
	capTemplate[1] = len(literal)

	m := &LiteralMatcher{
		Literal:     literal,
		CapTemplate: capTemplate,
	}

	if beginText && endText {
		m.Strategy = LiteralStrategyExact
	} else if beginText {
		m.Strategy = LiteralStrategyPrefix
	} else if endText {
		m.Strategy = LiteralStrategySuffix
	} else {
		m.Strategy = LiteralStrategyContains
	}
	return m
}

func extractLiteral(re *syntax.Regexp, beginText, endText *bool, literal *[]byte, capTemplate []int, offset int) bool {
	switch re.Op {
	case syntax.OpEmptyMatch:
		return true
	case syntax.OpBeginText:
		if offset != 0 {
			return false
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
	case syntax.OpCharClass:
		if re.Flags&syntax.FoldCase != 0 {
			return false
		}
		if len(re.Rune) == 2 && re.Rune[0] == re.Rune[1] {
			var buf [utf8.UTFMax]byte
			n := utf8.EncodeRune(buf[:], re.Rune[0])
			*literal = append(*literal, buf[:n]...)
			return true
		}
		return false
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
	default:
		return false
	}
}
