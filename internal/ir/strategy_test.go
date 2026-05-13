package ir

import (
	"context"
	"regexp/syntax"
	"testing"
)

func TestSearchStrategySelection(t *testing.T) {
	tests := []struct {
		pattern   string
		wantStrat SearchStrategy
		wantKern  CCWarpKernel
	}{
		{
			pattern:   "abc",
			wantStrat: SearchStrategyLiteral,
			wantKern:  CCWarpEqual, // 'a'
		},
		{
			pattern:   "[a-z]",
			wantStrat: SearchStrategySearchWarp,
			wantKern:  CCWarpSingleRange,
		},
		{
			pattern:   "a|z",
			wantStrat: SearchStrategySearchWarp,
			wantKern:  CCWarpBitmask,
		},
		{
			pattern:   "000-0000|111-1111",
			wantStrat: SearchStrategySearchWarp,
			wantKern:  CCWarpSingleRange, // '0' and '1' are contiguous
		},
		{
			pattern:   "000-0000|222-2222",
			wantStrat: SearchStrategySearchWarp,
			wantKern:  CCWarpBitmask, // '0' and '2' are not contiguous
		},
		{
			pattern:   "(abc|def)ghi",
			wantStrat: SearchStrategyLiteral, // 'ghi' is a mandatory fixed-distance anchor
			wantKern:  CCWarpBitmask,         // searchWarp still built from 'a' and 'd'
		},
		{
			pattern:   "abc|def|ghi",
			wantStrat: SearchStrategySearchWarp,
			wantKern:  CCWarpBitmask, // 'a', 'd', 'g' are not contiguous
		},
		{
			pattern:   `^([^!]+!)?([^!]+)$`,
			wantStrat: SearchStrategySearchWarp,
			wantKern:  CCWarpNotSingleRange, // [^!]
		},
	}

	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			re, _ := syntax.Parse(tt.pattern, syntax.Perl)
			prog, _ := syntax.Compile(re)
			dfa, err := NewDFAWithMemoryLimit(context.Background(), re, prog, 1024*1024, true)
			if err != nil {
				t.Fatalf("Failed to build DFA: %v", err)
			}

			if dfa.SearchStrategy() != tt.wantStrat {
				t.Errorf("Pattern %q: Strategy = %v; want %v", tt.pattern, dfa.SearchStrategy(), tt.wantStrat)
			}
			if CCWarpKernel(dfa.SearchWarp().Kernel) != tt.wantKern {
				t.Errorf("Pattern %q: Kernel = %v; want %v", tt.pattern, CCWarpKernel(dfa.SearchWarp().Kernel), tt.wantKern)
			}
		})
	}
}

func TestVariableDistancePivot(t *testing.T) {
	pattern := ".*@example\\.com"
	re, _ := syntax.Parse(pattern, syntax.Perl)
	prog, _ := syntax.Compile(re)
	dfa, err := NewDFAWithMemoryLimit(context.Background(), re, prog, 1024*1024, true)
	if err != nil {
		t.Fatalf("Failed to build DFA: %v", err)
	}

	if dfa.SearchStrategy() != SearchStrategyLiteral {
		t.Errorf("Pattern %q: Strategy = %v; want SearchStrategyLiteral", pattern, dfa.SearchStrategy())
	}
}
