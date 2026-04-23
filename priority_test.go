package regexp

import (
	goregexp "regexp"
	"testing"
)

func TestPriorityGreedyLoop(t *testing.T) {
	// a* has a loop path (Prio 0) and an exit path (Prio 1).
	// Greedy matching should prioritize Prio 0 to match as long as possible.
	re := MustCompile(`a*`)
	input := []byte("aaa")
	mc := &matchContext{}
	mc.prepare(len(input), re.numSubexp)

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
	pattern := `a|aa`
	input := "aa"

	re := MustCompile(pattern)
	got := re.FindSubmatchIndex([]byte(input))

	stdRe := goregexp.MustCompile(pattern)
	want := stdRe.FindSubmatchIndex([]byte(input))

	validateSubmatchIndex(t, pattern, input, got, want)
}

func TestPriorityAbsoluteTracking(t *testing.T) {
	// Mandate 2.12: Verification of Priority Normalization & Absolute Tracking
	pattern := `(a*)b`
	input := "aaab"

	re := MustCompile(pattern)
	got := re.FindSubmatchIndex([]byte(input))

	stdRe := goregexp.MustCompile(pattern)
	want := stdRe.FindSubmatchIndex([]byte(input))

	validateSubmatchIndex(t, pattern, input, got, want)
}
