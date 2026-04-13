package ir

import (
	"context"
	"strings"
	"testing"

	"github.com/kamichidu/go-regexp-re/syntax"
)

func TestStateExplosion(t *testing.T) {
	tests := []struct {
		name      string
		pattern   string
		limit     int
		expectErr bool
	}{
		// 1. Saved by Subset Construction Merging or AST Prep (Expect Success)
		{
			"Factoring: Common Suffix",
			"a*c|b*c",
			256 * 1024,
			false,
		},
		{
			"Factoring: Common Prefix",
			"ca*|cb*",
			256 * 1024,
			false,
		},
		{
			"Simple Alternation (Literal-like)",
			"apple|orange|banana|grape|peach|melon|cherry|berry",
			256 * 1024,
			false,
		},

		// 2. Fundamental DFA Explosion (Expect Error)
		{
			"Ambiguous Overlaps with Repetition",
			"(a|ab|abc|abcd|abcde|abcdef|abcdefg)*x|(a|ab|abc|abcd|abcde|abcdef|abcdefg)*y",
			64 * 1024,
			true,
		},
		{
			"Classic Exponential Explosion (N=20)",
			"([ab]*)a[ab]{20}b",
			64 * 1024,
			true,
		},
		{
			"Overlapping Captures Depth",
			// Many overlapping captures that force subset differentiation
			"((a|b)*c)|((a|b)*d)|((a|b)*e)|((a|b)*f)|((a|b)*g)|((a|b)*h)",
			32 * 1024,
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			re, err := syntax.Parse(tt.pattern, syntax.Perl)
			if err != nil {
				t.Fatalf("Parse(%q) failed: %v", tt.pattern, err)
			}
			re = syntax.Simplify(re)
			re = syntax.Optimize(re)
			prog, err := syntax.Compile(re)
			if err != nil {
				t.Fatalf("Compile(%q) failed: %v", tt.pattern, err)
			}

			dfa, err := NewDFAWithMemoryLimit(context.Background(), prog, tt.limit)

			if tt.expectErr {
				if err == nil {
					t.Errorf("NewDFAWithMemoryLimit(%q) expected error, but got nil (States: %d)", tt.pattern, dfa.NumStates())
				} else if !strings.Contains(err.Error(), "pattern too large or ambiguous") {
					t.Errorf("NewDFAWithMemoryLimit(%q) expected 'pattern too large or ambiguous' error, but got: %v", tt.pattern, err)
				}
			} else {
				if err != nil {
					t.Errorf("NewDFAWithMemoryLimit(%q) failed: %v", tt.pattern, err)
				}
			}
		})
	}
}
