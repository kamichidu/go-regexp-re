package regexp

import (
	"testing"
)

func TestFinal(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
		want    [2]int
	}{
		{"$^", "", [2]int{0, 0}},
		{"a*(|(b))c*", "aacc", [2]int{0, 4}},
		{"$", "abcde", [2]int{5, 5}},
	}

	for _, tt := range tests {
		re, err := Compile(tt.pattern)
		if err != nil {
			t.Errorf("Compile(%q) failed: %v", tt.pattern, err)
			continue
		}
		gotStart, gotEnd := re.match([]byte(tt.input))
		if gotStart != tt.want[0] || gotEnd != tt.want[1] {
			t.Errorf("match(%q, %q) = (%d, %d), want (%d, %d)", tt.pattern, tt.input, gotStart, gotEnd, tt.want[0], tt.want[1])
		}
	}
}
