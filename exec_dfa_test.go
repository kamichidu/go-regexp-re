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
			if got[0] != tt.indices[0] || got[1] != tt.indices[1] {
				t.Errorf("Pattern %q, Input %q: got indices %v, want %v", tt.pattern, tt.input, got[:2], tt.indices[:2])
			}
		} else {
			if got != nil {
				t.Errorf("Pattern %q, Input %q: expected no match, got %v", tt.pattern, tt.input, got)
			}
		}
	}
}

func TestWarpAndAnchors(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
		want    bool // Expected value (Go standard compliant)
		isStd   bool // Should match standard library behavior
	}{
		// Basic anchors
		{"^abc$", "abc", true, true},
		{"^abc$", "abcd", false, true},
		{"^abc$", " xabc", false, true},
		{"^abc", "abc", true, true},
		{"abc$", "abc", true, true},

		// Word boundaries (\b) - Standard ASCII word boundary
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
		// Note: \b is ASCII-only by default in Go. 'あ' is NOT a word character.
		{"\\bあ\\b", "あ", false, true},
		{"\\bあ\\b", "aあ", false, true},
		{"\\bあ\\b", " あ ", false, true},
		{"\\bあ\\b", "あ ", false, true},
		{"\\bあ\\b", " あ", false, true},

		// Dot Behavior (Standard compliant byte-level DFA)
		{"^.+$", "あいう", true, true},
		{"^あ.う$", "あいう", true, true},
		{"^.あ.$", "いあう", true, true},

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
