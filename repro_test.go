package regexp

import (
	"reflect"
	"testing"
)

func TestRepro(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
		want    []int
	}{
		{"$", "abcde", []int{5, 5}},
		{"(.*)", "abcd", []int{0, 4, 0, 4}},
		{"(.*).*", "ab", []int{0, 2, 0, 2}},
		{"a(b*)", "abbaab", []int{0, 3, 1, 3}},
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
