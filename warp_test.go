package regexp

import (
	"testing"
)

func TestWarpAndAnchors(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
		want    bool
	}{
		// Basic anchors
		{"^abc$", "abc", true},
		{"^abc$", "abcd", false},
		{"^abc$", " xabc", false},

		// Word boundaries
		{"\\babc\\b", "abc", true},
		{"\\babc\\b", "xabc", false},
		{"\\babc\\b", "abcx", false},
		{"\\babc\\b", " abc ", true},

		// Multi-byte Warp + Anchors
		{"^あ$", "あ", true},
		{"^あ$", "い", false},
		{"^あ$", "あい", false},
		{"\\bあ\\b", "あ", true},
		{"\\bあ\\b", "aあ", false}, // 'a' is a word character, so NO boundary
		{"\\bあ\\b", " あ ", true},

		// Dot + Warp + Anchors
		{"^.+$", "あいう", true},
		{"^あ.う$", "あいう", true},
		{"^あ.い$", "あいう", false},
	}

	for _, tt := range tests {
		re, err := Compile(tt.pattern)
		if err != nil {
			t.Errorf("Compile(%q) failed: %v", tt.pattern, err)
			continue
		}
		got := re.MatchString(tt.input)
		if got != tt.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tt.pattern, tt.input, got, tt.want)
		}
	}
}
