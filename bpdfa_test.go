package regexp

import (
	"fmt"
	"strings"
	"testing"
)

func TestBitParallelDFA_Exhaustive(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
		want    bool
	}{
		// Basic Literals
		{"abc", "abc", true},
		{"abc", "xabcy", true},
		{"abc", "ab", false},

		// Alternations
		{"a|b|c", "a", true},
		{"a|b|c", "b", true},
		{"a|b|c", "d", false},
		{"apple|banana", "I eat a banana.", true},

		// Repetitions
		{"a*", "", true},
		{"a*", "aaaaa", true},
		{"a+", "", false},
		{"a+", "a", true},
		{"a?", "", true},
		{"a?", "b", true}, // matches empty at start

		// Anchors (Standard)
		{"^abc", "abc", true},
		{"^abc", "xabc", false},
		{"abc$", "abc", true},
		{"abc$", "abcx", false},
		{"^abc$", "abc", true},
		{"^abc$", "abcd", false},

		// Multiline Anchors
		{"(?m)^abc", "abc", true},
		{"(?m)^abc", "x\nabc", true},
		{"(?m)abc$", "abc", true},
		{"(?m)abc$", "abc\nx", true},

		// Word Boundaries
		{"\\babc\\b", "abc", true},
		{"\\babc\\b", " abc ", true},
		{"\\babc\\b", "xabc", false},
		{"\\babc\\b", "abcx", false},
		{"\\Babc\\B", "xabcy", true},
		{"\\Babc\\B", " abc ", false},
		{"\\B", "xx", true},
		{"\\B", "x x", false},

		// Character Classes
		{"[a-z]+", "123abc456", true},
		{"[^a-z]+", "abc123abc", true},
		{"[\\d]+", "abc123abc", true},

		// Dot
		{".+", "abc", true},
		{"a.c", "abc", true},
		{"a.c", "a\nc", false}, // syntax.DotNotNL is default
		{"(?s)a.c", "a\nc", true},

		// Complex unanchored search
		{"a+b", "caaaab", true},
		{"a+b", "baaaac", false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/%s", tt.pattern, tt.input), func(t *testing.T) {
			re, err := CompileWithOptions(tt.pattern, CompileOptions{forceStrategy: strategyBitParallel})
			if err != nil {
				t.Fatalf("Compile(%q) error: %v", tt.pattern, err)
			}
			if re.strategy != strategyBitParallel {
				t.Errorf("Pattern %q: strategy was %v, want strategyBitParallel", tt.pattern, re.strategy)
			}
			if got := re.MatchString(tt.input); got != tt.want {
				t.Errorf("MatchString(%q, %q) = %v; want %v", tt.pattern, tt.input, got, tt.want)
			}
		})
	}
}

func TestBitParallelDFA_Boundary64(t *testing.T) {
	// Use a pattern that cannot be optimized into a pure literal.
	// (a|b) prevents literal matcher bypass.
	for _, n := range []int{5, 10, 20, 25} {
		pattern := strings.Repeat("a", n) + "(a|b)"
		t.Run(fmt.Sprintf("Count_%d", n), func(t *testing.T) {
			re, err := Compile(pattern)
			if err != nil {
				t.Fatalf("Compile error: %v", err)
			}

			instCount := len(re.prog.Inst)
			t.Logf("N=%d -> Instructions: %d, Strategy: %v", n, instCount, re.strategy)

			if instCount <= 64 {
				// BP-DFA should be chosen unless prefix is too long (we use n to control it)
				// Current bindMatchStrategy uses prefix >= 4 to prefer DFA.
				// To test BP-DFA, we need short or no prefix.
			}
		})
	}
}

func TestBitParallelDFA_ForceStrategy(t *testing.T) {
	// Force BP-DFA for various patterns to ensure correctness.
	tests := []struct {
		pattern string
		input   string
		want    bool
	}{
		{"abc$", "abc", true},
		{"abc$", "abcx", false},
		{"^abc", "abc", true},
		{"^abc", "xabc", false},
		{"a*b", "aaab", true},
		{"a*b", "aaaa", false},
	}
	for _, tt := range tests {
		re, _ := CompileWithOptions(tt.pattern, CompileOptions{forceStrategy: strategyBitParallel})
		if got := re.MatchString(tt.input); got != tt.want {
			t.Errorf("Forced BP-DFA: MatchString(%q, %q) = %v, want %v", tt.pattern, tt.input, got, tt.want)
		}
	}
}
func TestBitParallelDFA_MatchesEmpty(t *testing.T) {
	tests := []struct {
		pattern string
		want    bool
	}{
		{"a*", true},
		{"a+", false},
		{"a?", true},
		{"(a|b)*", true},
		{"a*b", false},
		{"^", true},
		{"$", true},
		{"(?m)^", true},
		{"(?m)$", true},
	}

	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			re, err := CompileWithOptions(tt.pattern, CompileOptions{forceStrategy: strategyBitParallel})
			if err != nil {
				t.Fatalf("Compile error: %v", err)
			}
			if got := re.MatchString(""); got != tt.want {
				t.Errorf("MatchString(%q, \"\") = %v; want %v", tt.pattern, got, tt.want)
			}
		})
	}
}

func TestBitParallelDFA_EdgeCases(t *testing.T) {
	// 1. Matches empty string at start
	re, _ := CompileWithOptions("^", CompileOptions{forceStrategy: strategyBitParallel})
	if !re.MatchString("abc") {
		t.Error("MatchString(\"^\", \"abc\") = false, want true")
	}

	// 2. Matches empty string at end
	re, _ = CompileWithOptions("$", CompileOptions{forceStrategy: strategyBitParallel})
	if !re.MatchString("abc") {
		t.Error("MatchString(\"$\", \"abc\") = false, want true")
	}

	// 3. Match at exactly EOF
	re, _ = CompileWithOptions("a$", CompileOptions{forceStrategy: strategyBitParallel})
	if !re.MatchString("ba") {
		t.Error("MatchString(\"a$\", \"ba\") = false, want true")
	}
	if re.MatchString("ab") {
		t.Error("MatchString(\"a$\", \"ab\") = true, want false")
	}
}
