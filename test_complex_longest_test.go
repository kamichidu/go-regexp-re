package regexp

import (
	"reflect"
	"testing"
)

func TestComplexLongestMatch(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
		want    []int
	}{
		{"(.*)a", "baaa", []int{0, 4, 0, 3}},
		{"(.*?)a", "baaa", []int{0, 2, 0, 1}},
		{"a*(a)", "aaaa", []int{0, 4, 3, 4}},
		{"a*?(a)", "aaaa", []int{0, 1, 0, 1}},
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
