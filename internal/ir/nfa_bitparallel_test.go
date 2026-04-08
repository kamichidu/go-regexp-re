package ir

import (
	"github.com/kamichidu/go-regexp-re/syntax"
	"testing"
)

func TestNfaMatchBitParallel(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
		want    []int
	}{
		{"abc", "abc", []int{0, 3}},
		{"a*b", "aaab", []int{0, 4}},
		{"(a|b)c", "ac", []int{0, 2, 0, 1}},
		{"(a|b)c", "bc", []int{0, 2, 0, 1}},
		{"(([^xyz]*)(d))", "abcd", []int{0, 4, 0, 4, 0, 3, 3, 4}},
	}

	for _, tt := range tests {
		re, _ := syntax.Parse(tt.pattern, syntax.Perl)
		prog, _ := syntax.Compile(re)
		got := nfaMatchBitParallel(prog, []byte(tt.input), 0, len(tt.input), re.MaxCap())
		if len(got) != len(tt.want) {
			t.Errorf("nfaMatchBitParallel(%q, %q) len = %d, want %d", tt.pattern, tt.input, len(got), len(tt.want))
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("nfaMatchBitParallel(%q, %q) = %v, want %v", tt.pattern, tt.input, got, tt.want)
				break
			}
		}
	}
}
