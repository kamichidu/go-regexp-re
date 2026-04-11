package ir_test

import (
	"strings"
	"testing"

	"github.com/kamichidu/go-regexp-re/internal/ir"
	"github.com/kamichidu/go-regexp-re/syntax"
)

func TestToDOT(t *testing.T) {
	re, _ := syntax.Parse("a(b|c)*d", syntax.Perl)
	prog, _ := syntax.Compile(re)
	dfa, _ := ir.NewDFA(prog)

	dot := ir.ToDOT(dfa)
	if !strings.HasPrefix(dot, "digraph DFA {") {
		t.Errorf("DOT output should start with 'digraph DFA {', got %q", dot[:min(len(dot), 20)])
	}
	if !strings.HasSuffix(strings.TrimSpace(dot), "}") {
		t.Errorf("DOT output should end with '}', got %q", dot[max(0, len(dot)-20):])
	}

	// Basic checks for expected elements
	if !strings.Contains(dot, "S0 (match)") {
		t.Error("DOT output should contain match state S0")
	}
	if !strings.Contains(dot, "shape=doublecircle") {
		t.Error("DOT output should contain doublecircle for accepting states")
	}
	if !strings.Contains(dot, "->") {
		t.Error("DOT output should contain transitions (->)")
	}
}
