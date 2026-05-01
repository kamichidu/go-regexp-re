package ir

import (
	"reflect"
	"regexp/syntax"
	"testing"
)

func TestAnalyzeLiteralPattern(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
		match   bool
		indices []int
	}{
		{"abc", "abc", true, []int{0, 3}},
		{"abc", "xabcy", true, []int{1, 4}},
		{"^abc", "abc", true, []int{0, 3}},
		{"^abc", "xabc", false, nil},
		{"abc$", "abc", true, []int{0, 3}},
		{"abc$", "abcy", false, nil},
		{"^abc$", "abc", true, []int{0, 3}},
		{"^abc$", "abcd", false, nil},
		{"(abc)", "abc", true, []int{0, 3, 0, 3}},
		{"^(a)b(c)$", "abc", true, []int{0, 3, 0, 1, 2, 3}},
		{"^$", "", true, []int{0, 0}},
	}

	for _, tt := range tests {
		re, _ := syntax.Parse(tt.pattern, syntax.Perl)
		m := AnalyzeLiteralPattern(re, re.MaxCap()+1)
		if m == nil {
			t.Errorf("AnalyzeLiteralPattern(%q) = nil; want non-nil", tt.pattern)
			continue
		}

		in := Input{
			B:           []byte(tt.input),
			AbsPos:      0,
			TotalBytes:  len(tt.input),
			SearchStart: 0,
			SearchEnd:   len(tt.input),
		}

		if got := m.Match(&in); got != tt.match {
			t.Errorf("Match(%q, %q) = %v; want %v", tt.pattern, tt.input, got, tt.match)
		}

		gotIndices := m.FindSubmatchIndex(&in)
		if !reflect.DeepEqual(gotIndices, tt.indices) {
			t.Errorf("FindSubmatchIndex(%q, %q) = %v; want %v", tt.pattern, tt.input, gotIndices, tt.indices)
		}
	}
}
