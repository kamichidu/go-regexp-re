package regexp

import (
	goregexp "regexp"
	"testing"
)

func TestCCWarp(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
		match   bool
		indices []int
	}{
		// 1. CCWarpEqual (a+)
		{"a+", "aaabbb", true, []int{0, 3}},
		{"a+", "bbbaaa", true, []int{3, 6}},

		// 2. CCWarpSingleRange ([0-9]+)
		{"[0-9]+", "abc12345def", true, []int{3, 8}},

		// 3. CCWarpEqualSet ([aeiou]+)
		{"[aeiou]+", "sky-apple-tree", true, []int{4, 5}}, // 'a' in apple

		// 4. CCWarpAnyChar ((?s).*)
		{"(?s).*", "line1\nline2", true, []int{0, 11}},

		// 5. CCWarpAnyExceptNL (.*)
		{".*", "hello world", true, []int{0, 11}},
		{"^.*$", "line1\nline2", false, nil},

		// 6. CCWarpNotEqual ([^"]+)
		{`[^"]+`, `say "hello"`, true, []int{0, 4}}, // 'say '

		// 7. CCWarpNotSingleRange ([^0-9]+)
		{"[^0-9]+", "123abc456", true, []int{3, 6}}, // 'abc'

		// 8. CCWarpNotEqualSet ([^ "]+)
		{`[^ "]+`, `hello "world"`, true, []int{0, 5}}, // 'hello'

		// 9. CCWarpBitmask ([a-zA-Z0-9_]+)
		{"[a-zA-Z0-9_]+", "  word123  ", true, []int{2, 9}},

		// 10. CCWarpNotBitmask ([^a-z]+)
		{"[^a-z]+", "abc12345DEF", true, []int{3, 11}}, // '12345DEF'

		// UTF-8 Edge (Warp should stop at multi-byte)
		{"[a-z]+", "abcdあefgh", true, []int{0, 4}},
	}

	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			re := MustCompile(tt.pattern)
			got := re.FindStringSubmatchIndex(tt.input)
			if tt.match {
				if got == nil {
					t.Errorf("Pattern %q should match input %q", tt.pattern, tt.input)
					return
				}
				if got[0] != tt.indices[0] || got[1] != tt.indices[1] {
					t.Errorf("Pattern %q match %q: got %v, want %v", tt.pattern, tt.input, got[:2], tt.indices)
				}
			} else {
				if got != nil {
					t.Errorf("Pattern %q should NOT match input %q, got %v", tt.pattern, tt.input, got)
				}
			}

			// Parity check
			stdRE := goregexp.MustCompile(tt.pattern)
			want := stdRE.FindStringSubmatchIndex(tt.input)
			if want == nil && got != nil {
				t.Errorf("Pattern %q match %q: std rejected, but got %v", tt.pattern, tt.input, got)
			} else if want != nil && got == nil {
				t.Errorf("Pattern %q match %q: std matched %v, but got nil", tt.pattern, tt.input, want)
			} else if want != nil && got != nil {
				if want[0] != got[0] || want[1] != got[1] {
					t.Errorf("Pattern %q match %q: got %v, want std %v", tt.pattern, tt.input, got[:2], want[:2])
				}
			}
		})
	}
}
