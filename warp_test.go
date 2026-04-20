package regexp

import (
	goregexp "regexp"
	"testing"
)

func TestWarpAndAnchors(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
	}{
		// Basic anchors
		{"^abc$", "abc"},
		{"^abc$", "abcd"},
		{"^abc$", " xabc"},
		{"^abc", "abc"},
		{"abc$", "abc"},

		// Word boundaries
		{"\\babc\\b", "abc"},
		{"\\babc\\b", "xabc"},
		{"\\babc\\b", "abcx"},
		{"\\babc\\b", " abc "},
		{"\\babc\\b", "x abc x"},
		{"\\babc\\b", "1abc2"},

		// Multi-byte Warp + Anchors
		{"^あ$", "あ"},
		{"^あ$", "い"},
		{"^あ$", "あい"},
		{"\\bあ\\b", "あ"},
		{"\\bあ\\b", "aあ"},
		{"\\bあ\\b", " あ "},
		{"\\bあ\\b", "あ "},
		{"\\bあ\\b", " あ"},

		// Dot + Warp + Anchors
		{"^.+$", "あいう"},
		{"^あ.う$", "あいう"},
		{"^あ.い$", "あいう"},
		{"^.あ.$", "いあう"},

		// Nested/Sequential Anchors
		{"^\\babc\\b$", "abc"},
		{"^\\babc\\b$", " abc "},
	}

	for _, tt := range tests {
		stdRe := goregexp.MustCompile(tt.pattern)
		want := stdRe.MatchString(tt.input)

		re, err := Compile(tt.pattern)
		if err != nil {
			t.Errorf("Compile(%q) failed: %v", tt.pattern, err)
			continue
		}
		got := re.MatchString(tt.input)
		if got != want {
			t.Errorf("Match(%q, %q) = %v, want %v (standard)", tt.pattern, tt.input, got, want)
		}
	}
}
