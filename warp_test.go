package regexp

import (
	"testing"
)

func TestMultiByteWarp(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
		match   bool
	}{
		{"あ", "あ", true},
		{"あ", "い", false},
		{".", "あ", true},
		{".", "い", true},
		{"^あ*$", "あああ", true},
		{"^あ*$", "あいう", false},
		{".*", "あいうえお", true},
	}

	for _, tt := range tests {
		re, err := Compile(tt.pattern)
		if err != nil {
			t.Errorf("Compile(%q) failed: %v", tt.pattern, err)
			continue
		}
		got := re.MatchString(tt.input)
		if got != tt.match {
			t.Errorf("Match(%q, %q) = %v; want %v", tt.pattern, tt.input, got, tt.match)
		}
	}
}

func TestMultiByteSubmatch(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
		want    []int
	}{
		{"(あ)", "あ", []int{0, 3, 0, 3}},
		{"(.)", "あ", []int{0, 3, 0, 3}},
		{"あ(い)う", "あいう", []int{0, 9, 3, 6}},
	}

	for _, tt := range tests {
		re, err := Compile(tt.pattern)
		if err != nil {
			t.Errorf("Compile(%q) failed: %v", tt.pattern, err)
			continue
		}
		got := re.FindStringSubmatchIndex(tt.input)
		if !equalIndices(got, tt.want) {
			t.Errorf("FindSubmatchIndex(%q, %q) = %v; want %v", tt.pattern, tt.input, got, tt.want)
		}
	}
}

func equalIndices(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
