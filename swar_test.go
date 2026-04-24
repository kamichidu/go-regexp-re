package regexp

import (
	"testing"
)

func TestCCWarp(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
		match   bool
		indices []int
	}{
		// 1. Single Range [0-9]+
		{"[0-9]+", "abc12345678def", true, []int{3, 11}},
		{"[0-9]+", "1234567890123456", true, []int{0, 16}},

		// 2. Bitmask [a-zA-Z0-9_]+
		{"[a-zA-Z0-9_]+", "  word1234_5678  ", true, []int{2, 15}},

		// 3. AnyExceptNL (.*)
		{".*", "hello swar warp", true, []int{0, 15}},
		{"^.*$", "line1\nline2", false, nil}, // Should not match \n

		// 4. Combined Warp and DFA
		{"[a-z]+[0-9]+", "abcdefgh12345678", true, []int{0, 16}},

		// 5. Submatch with Warp
		{"([a-z]+)([0-9]+)", "abcdefgh12345678", true, []int{0, 16, 0, 8, 8, 16}},

		// 6. UTF-8 Edge (Warp should stop at multi-byte)
		{"[a-z]+", "abcdあefgh", true, []int{0, 4}},
	}

	for _, tt := range tests {
		re, err := Compile(tt.pattern)
		if err != nil {
			t.Errorf("Pattern %q: Compile error: %v", tt.pattern, err)
			continue
		}

		got := re.FindStringSubmatchIndex(tt.input)
		if tt.match {
			if got == nil {
				t.Errorf("Pattern %q, Input %q: expected match, got nil", tt.pattern, tt.input)
				continue
			}
			for i, idx := range tt.indices {
				if i >= len(got) || got[i] != idx {
					t.Errorf("Pattern %q, Input %q: index %d got %d, want %d", tt.pattern, tt.input, i, got[i], idx)
				}
			}
		} else {
			if got != nil {
				t.Errorf("Pattern %q, Input %q: expected no match, got %v", tt.pattern, tt.input, got)
			}
		}
	}
}
