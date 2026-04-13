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
		{`abc`, "abc", true, []int{0, 3}},
		{`abc`, "xabcy", true, []int{1, 4}},
		{`^abc`, "abc", true, []int{0, 3}},
		{`^abc`, "xabc", false, nil},
		{`abc$`, "abc", true, []int{0, 3}},
		{`abc$`, "abcx", false, nil},
		{`^abc$`, "abc", true, []int{0, 3}},
		{`^abc$`, "abcd", false, nil},
		{`(abc)`, "abc", true, []int{0, 3, 0, 3}},
		{`^(a)b(c)$`, "abc", true, []int{0, 3, 0, 1, 2, 3}},
		{`^$`, "", true, []int{0, 0}},
		{`^$`, "a", false, nil},
	}

	for _, tt := range tests {
		re, err := syntax.Parse(tt.pattern, syntax.Perl)
		if err != nil {
			t.Fatalf("parse %q: %v", tt.pattern, err)
		}
		m := AnalyzeLiteralPattern(re, re.MaxCap()+1)
		if m == nil {
			t.Errorf("AnalyzeLiteralPattern(%q) = nil; want non-nil", tt.pattern)
			continue
		}

		if got := m.Match([]byte(tt.input)); got != tt.match {
			t.Errorf("Match(%q, %q) = %v; want %v", tt.pattern, tt.input, got, tt.match)
		}

		gotIndices := m.FindSubmatchIndex([]byte(tt.input))
		if !reflect.DeepEqual(gotIndices, tt.indices) {
			t.Errorf("FindSubmatchIndex(%q, %q) = %v; want %v", tt.pattern, tt.input, gotIndices, tt.indices)
		}
	}
}

func TestAnalyzeLiteralPattern_Reject(t *testing.T) {
	patterns := []string{
		`a|b`,
		`a*`,
		`[a-z]`,
		`.`,
		`^a|b$`,
	}

	for _, p := range patterns {
		re, err := syntax.Parse(p, syntax.Perl)
		if err != nil {
			t.Fatalf("parse %q: %v", p, err)
		}
		if m := AnalyzeLiteralPattern(re, re.MaxCap()+1); m != nil {
			t.Errorf("AnalyzeLiteralPattern(%q) = non-nil; want nil", p)
		}
	}
}
