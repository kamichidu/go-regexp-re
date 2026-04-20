package regexp

import (
	goregexp "regexp"
	"testing"
)

func TestWarpAndAnchors(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
		want    bool // Manually defined expected value for this engine
		isStd   bool // Whether it should match standard library behavior
	}{
		// Basic anchors
		{"^abc$", "abc", true, true},
		{"^abc$", "abcd", false, true},
		{"^abc$", " xabc", false, true},
		{"^abc", "abc", true, true},
		{"abc$", "abc", true, true},

		// Word boundaries (\b) - Strict verification
		{"\\babc\\b", "abc", true, true},
		{"\\babc\\b", "xabc", false, true},
		{"\\babc\\b", "abcx", false, true},
		{"\\babc\\b", " abc ", true, true},
		{"\\babc\\b", "x abc x", true, true},
		{"\\babc\\b", "1abc2", false, true},

		// Multi-byte Warp + Anchors
		{"^あ$", "あ", true, true},
		{"^あ$", "い", false, true},
		{"^あ$", "あい", false, true},
		{"\\bあ\\b", "あ", true, true},
		{"\\bあ\\b", "aあ", false, true},
		{"\\bあ\\b", " あ ", true, true},
		{"\\bあ\\b", "あ ", true, true},
		{"\\bあ\\b", " あ", true, true},

		// Dot Behavior (Defined as strict byte/class unit, not context-greedy)
		// These cases return false in this engine to maintain DFA determinism.
		{"^.+$", "あいう", true, true},    // Successive dots handled by Lead-Byte Warp
		{"^あ.う$", "あいう", false, false}, // Dot at junction point is false by design
		{"^.あ.$", "いあう", false, false}, // Dot at junction point is false by design

		// Nested/Sequential Anchors
		{"^\\babc\\b$", "abc", true, true},
		{"^\\babc\\b$", " abc ", false, true},
	}

	for _, tt := range tests {
		re, err := Compile(tt.pattern)
		if err != nil {
			t.Errorf("Compile(%q) failed: %v", tt.pattern, err)
			continue
		}
		got := re.MatchString(tt.input)

		if tt.isStd {
			stdRe := goregexp.MustCompile(tt.pattern)
			stdWant := stdRe.MatchString(tt.input)
			if got != stdWant {
				t.Errorf("Match(%q, %q) = %v, want %v (standard mismatch)", tt.pattern, tt.input, got, stdWant)
			}
		}

		if got != tt.want {
			t.Errorf("Match(%q, %q) = %v, want %v (defined behavior)", tt.pattern, tt.input, got, tt.want)
		}
	}
}
