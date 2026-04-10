package regexp

import (
	"reflect"
	"testing"
)

func TestUnnamedMatch(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
		want    []int
	}{
		{"a*", "aaaa", []int{0, 4}},
		{"a*?", "aaaa", []int{0, 0}},
		{"a+", "aaaa", []int{0, 4}},
		{"a+?", "aaaa", []int{0, 1}},
		{"a*a", "aaaa", []int{0, 4}},
		{"a*?a", "aaaa", []int{0, 1}},
		{"a|aa", "aaaa", []int{0, 1}},
		{"aa|a", "aaaa", []int{0, 2}},
	}

	for _, tt := range tests {
		re, err := Compile(tt.pattern)
		if err != nil {
			t.Errorf("Compile(%q) failed: %v", tt.pattern, err)
			continue
		}
		got := re.FindStringSubmatchIndex(tt.input)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("FindStringSubmatchIndex(%q, %q) = %v, want %v", tt.pattern, tt.input, got, tt.want)
		}
	}
}
