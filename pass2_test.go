package regexp

import (
	"fmt"
	"github.com/kamichidu/go-regexp-re/internal/ir"
	"testing"
)

func TestPass2PathIdentity(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
	}{
		{`a|b`, "a"},
		{`(a|b)c`, "ac"},
		{`a(b|c)d`, "abd"},
		{`a*b`, "aab"},
		{`(あ|い)う`, "あう"},
	}

	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			re, err := Compile(tt.pattern)
			if err != nil {
				t.Fatalf("Compile failed: %v", err)
			}
			b := []byte(tt.input)

			// Pass 1: Naked Discovery
			mc := matchContextPool.Get().(*matchContext)
			defer matchContextPool.Put(mc)
			mc.prepare(len(b))

			regs := make([]int, (re.numSubexp+1)*2)
			start, end, prio := re.findSubmatchIndexInternal(b, mc, regs)
			if start < 0 {
				t.Fatalf("Pass 1 failed to find match")
			}

			fmt.Printf("\n--- Dissecting Pass 2 for %q ---\n", tt.pattern)
			fmt.Printf("Match: [%d, %d], Final Priority: %d\n", start, end, prio)

			// Pass 2 Simulation: Trace Path Identity from start to end
			// In a real implementation, this would be a table-driven loop.
			// Here we check if the recorded history in mc.history contains enough info.

			currPrio := 0 // Relative priority at start (always 0 for winning path at start position)
			for i := start; i < end; {
				state := mc.history[i]
				sidx := int(state & ir.StateIDMask)
				byteVal := b[i]

				fmt.Printf("Pos %d: State S%d, Byte 0x%02X\n", i, sidx, byteVal)

				// Here we need to find the transition that leads to the next state
				// with a valid priority update.
				// In our current DFA, recapTables[0] stores RecapEntry which has
				// InputPriority and NextPriority.

				off := (sidx << 8) | int(byteVal)
				entries := re.dfa.RecapTables()[0].Transitions[off]

				found := false
				for _, entry := range entries {
					// Check if this entry matches our current priority
					if int(entry.InputPriority) == currPrio {
						fmt.Printf("  -> Transition found: NextPriority=%d\n", entry.NextPriority)
						currPrio = int(entry.NextPriority)
						found = true
						break
					}
				}

				if !found {
					t.Errorf("Pass 2 error: No valid path identity transition at pos %d (State S%d, Prio %d)", i, sidx, currPrio)
					break
				}

				// Handle Multi-byte Warp pointer advancement
				rawNext := re.dfa.Transitions()[off]
				if byteVal < 0x80 || (rawNext&ir.WarpStateFlag) == 0 {
					i++
				} else {
					// Warp skip
					// Note: Pass 2 must advance the same way as Pass 1
					// and use the same priority accumulation rules.
					skip := 1 + (ir.GetTrailingByteCount(byteVal))
					fmt.Printf("  -> Warp skip: %d bytes\n", skip)
					i += skip
				}
			}

			if currPrio != (prio % ir.SearchRestartPenalty) {
				t.Errorf("Pass 2 error: Priority mismatch at end. Got %d, want %d", currPrio, prio%ir.SearchRestartPenalty)
			}
		})
	}
}
