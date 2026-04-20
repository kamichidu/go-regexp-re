package regexp

import (
	"github.com/kamichidu/go-regexp-re/internal/ir"
	"testing"
)

func TestPass2PathIdentity(t *testing.T) {
	tests := []struct {
		pattern           string
		input             string
		expectedPrioChain []int16
	}{
		{`(a)|(b)`, "a", []int16{0, 0}},
		{`(a)|(b)`, "b", []int16{1, 0}},
		{`a((b)|(c))d`, "abd", []int16{0, 0, 0, 0}},
		{`a((b)|(c))d`, "acd", []int16{0, 1, 0, 0}},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"/"+tt.input, func(t *testing.T) {
			re, err := Compile(tt.pattern)
			if err != nil {
				t.Fatalf("Compile failed: %v", err)
			}

			b := []byte(tt.input)
			mc := &matchContext{}
			mc.prepare(len(b))

			regs := make([]int, (re.numSubexp+1)*2)
			start, end, prio := re.findSubmatchIndexInternal(b, mc, regs)
			if start < 0 {
				t.Fatalf("Match failed")
			}

			re.sparseTDFA_PathSelection(mc, b, start, end, prio)

			for i := start; i <= end; i++ {
				if int16(mc.pathHistory[i]) != tt.expectedPrioChain[i-start] {
					t.Errorf("At pos %d, expected pathID %d, got %d", i, tt.expectedPrioChain[i-start], mc.pathHistory[i])
				}
			}
		})
	}
}

func TestPass2PathBranching(t *testing.T) {
	// (a|ab)c leftmost-first rule
	re := MustCompile(`(a|ab)c`)
	input := []byte("abc")
	mc := &matchContext{}
	mc.prepare(len(input))

	start, end, prio := re.findSubmatchIndexInternal(input, mc, nil)
	re.sparseTDFA_PathSelection(mc, input, start, end, prio)

	// Path Selection should strictly follow the history and recap tables
	for i := start; i <= end; i++ {
		state := mc.history[i]
		sidx := uint32(state) & uint32(ir.StateIDMask)
		t.Logf("Pos %d: State %d, PathID %d", i, sidx, mc.pathHistory[i])
	}
}
