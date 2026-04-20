package regexp

import (
	"testing"
)

func TestPriorityGreedyLoop(t *testing.T) {
	// a* has a loop path (Prio 0) and an exit path (Prio 1).
	// Greedy matching should prioritize Prio 0 to match as long as possible.
	re := MustCompile(`a*`)
	input := []byte("aaa")
	mc := &matchContext{}
	mc.prepare(len(input))

	regs := make([]int, (re.numSubexp+1)*2)
	start, end, prio := re.findSubmatchIndexInternal(input, mc, regs)

	t.Logf("Match result: [%d, %d], Total Prio: %d", start, end, prio)

	if start != 0 || end != 3 {
		t.Errorf("Greedy failed: expected [0, 3], got [%d, %d]", start, end)
	}

	// Verify history trace
	for i := 0; i <= len(input); i++ {
		t.Logf("Pos %d: State %d", i, mc.history[i])
	}
}

func TestPriorityAlternation(t *testing.T) {
	// (a|aa) leftmost-first loop behavior.
	// In standard Go, 'a' is prioritized even if 'aa' follows, or longest match is preferred
	// depending on the exact AST structure. syntax.Simplify might optimize it to (?:aa|a).
	re := MustCompile(`a|aa`)
	input := []byte("aa")

	got := re.FindSubmatchIndex(input)
	t.Logf("Pattern a|aa on 'aa': %v", got)
	// Go standard: "a|aa" on "aa" -> "a" [0, 1]
	if got[1] != 1 {
		t.Errorf("Leftmost-first failed: expected end 1, got %d", got[1])
	}
}

func TestPriorityAbsoluteTracking(t *testing.T) {
	// Mandate 2.12: Verification of Priority Normalization & Absolute Tracking
	re := MustCompile(`(a*)b`)
	input := []byte("aaab")

	mc := &matchContext{}
	mc.prepare(len(input))
	start, end, prio := re.findSubmatchIndexInternal(input, mc, nil)

	t.Logf("Match (a*)b: [%d, %d], Prio: %d", start, end, prio)

	if start != 0 || end != 4 {
		t.Errorf("Absolute tracking failed: expected [0, 4], got [%d, %d]", start, end)
	}
}
