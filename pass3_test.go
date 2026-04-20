package regexp

import (
	"reflect"
	"testing"
)

func TestPass3SubmatchExtraction(t *testing.T) {
	tests := []struct {
		pattern  string
		input    string
		expected []int
	}{
		{`a(b)c`, "abc", []int{0, 3, 1, 2}},
		{`a(b|c)d`, "abd", []int{0, 3, 1, 2}},
		{`a(b|c)d`, "acd", []int{0, 3, 1, 2}},
		{`(a|ab)c`, "abc", []int{0, 3, 0, 2}}, // Leftmost-first: "ab"c is prio, but "(a)bc" is shorter and match is best at i=2?
		// Note: (a|ab)c on "abc" matches "abc". Groups are (a|ab).
		// Standard Go: "abc" matches, group 1 is "ab" [0, 2].
		{`(a|ab)c`, "abc", []int{0, 3, 0, 2}},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"/"+tt.input, func(t *testing.T) {
			re, err := Compile(tt.pattern)
			if err != nil {
				t.Fatalf("Compile failed: %v", err)
			}

			got := re.FindSubmatchIndex([]byte(tt.input))
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("Pattern %q, input %q: expected %v, got %v", tt.pattern, tt.input, tt.expected, got)
			}
		})
	}
}

func TestPass3MultiByte(t *testing.T) {
	re := MustCompile(`あ(い)う`)
	input := "あいう"
	got := re.FindSubmatchIndex([]byte(input))
	// Each character is 3 bytes. Total=9. Group1=[3, 6].
	expected := []int{0, 9, 3, 6}
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("Multi-byte failed: expected %v, got %v", expected, got)
	}
}
