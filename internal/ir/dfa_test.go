package ir_test

import (
	"testing"

	"github.com/kamichidu/go-regexp-re/internal/ir"
	"github.com/kamichidu/go-regexp-re/syntax"
)

func TestDFA_Initialization(t *testing.T) {
	re, err := syntax.Parse("a", syntax.Perl)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	prog, err := syntax.Compile(re)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}

	dfa, err := ir.NewDFA(prog)
	if err != nil {
		t.Fatalf("NewDFA failed: %v", err)
	}

	if dfa == nil {
		t.Fatal("NewDFA returned nil")
	}

	if dfa.TotalStates() < 1 {
		t.Errorf("DFA should have at least 1 state, got %d", dfa.TotalStates())
	}
}

func TestDFA_FoldCase(t *testing.T) {
	tests := []struct {
		pattern string
		inputs  map[string]bool
	}{
		{
			pattern: "(?i)a",
			inputs: map[string]bool{
				"a": true,
				"A": true,
				"b": false,
			},
		},
		{
			pattern: "(?i)[a-c]",
			inputs: map[string]bool{
				"a": true,
				"A": true,
				"b": true,
				"B": true,
				"c": true,
				"C": true,
				"d": false,
			},
		},
		{
			pattern: "(?i)\u00E0", // à
			inputs: map[string]bool{
				"\u00E0": true, // à
				"\u00C0": true, // À
				"a":      false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			re, err := syntax.Parse(tt.pattern, syntax.Perl)
			if err != nil {
				t.Fatalf("Parse failed: %v", err)
			}
			prog, err := syntax.Compile(re)
			if err != nil {
				t.Fatalf("Compile failed: %v", err)
			}

			dfa, err := ir.NewDFA(prog)
			if err != nil {
				t.Fatalf("NewDFA failed: %v", err)
			}

			for input, wantMatch := range tt.inputs {
				state := dfa.StartState()
				for i := 0; i < len(input); i++ {
					state = dfa.Next(state, int(input[i]))
					if state == ir.InvalidState {
						break
					}
				}
				isMatch := state != ir.InvalidState && dfa.IsAccepting(state)
				if isMatch != wantMatch {
					t.Errorf("for input %q, expected match %v, got %v", input, wantMatch, isMatch)
				}
			}
		})
	}
}
