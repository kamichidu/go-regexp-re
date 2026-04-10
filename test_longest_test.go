package regexp

import (
	"reflect"
	"testing"
)

func TestLongestMatch(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
		want    []int
	}{
		{"(a*)", "aaaa", []int{0, 4, 0, 4}},
		{"(a*?)", "aaaa", []int{0, 0, 0, 0}},
		{"(a+)", "aaaa", []int{0, 4, 0, 4}},
		{"(a+?)", "aaaa", []int{0, 1, 0, 1}},
		{"(a*)a", "aaaa", []int{0, 4, 0, 3}},
		{"(a*?)a", "aaaa", []int{0, 1, 0, 0}},
		{"(a)|(aa)", "aaaa", []int{0, 1, 0, 1, -1, -1}}, // Leftmost-first: a wins
		{"(aa)|(a)", "aaaa", []int{0, 2, 0, 2, -1, -1}}, // Leftmost-first: aa wins
		{"(a*)b", "aaab", []int{0, 4, 0, 3}},
		{"(.*)", "abcd", []int{0, 4, 0, 4}},
		{"(.*?)", "abcd", []int{0, 0, 0, 0}},
	}

	for _, tt := range tests {
		re, err := Compile(tt.pattern)
		if err != nil {
			t.Errorf("Compile(%q) failed: %v", tt.pattern, err)
			continue
		}
		got := re.FindStringSubmatchIndex(tt.input)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("FindStringSubmatchIndex(%q, %q) = %v (numSubexp=%d), want %v", tt.pattern, tt.input, got, re.NumSubexp(), tt.want)
		}
	}
}
