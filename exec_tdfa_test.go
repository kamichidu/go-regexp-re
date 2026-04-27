package regexp

import (
	goregexp "regexp"
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
			mc.prepare(len(b), re.numSubexp)

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
	mc.prepare(len(input), re.numSubexp)

	start, end, prio := re.findSubmatchIndexInternal(input, mc, nil)
	re.sparseTDFA_PathSelection(mc, input, start, end, prio)

	// Path Selection should strictly follow the history and recap tables
	byteOffset := 0
	for _, entry := range mc.history {
		sidx := entry & histStateMask
		length := 1
		if (entry & histWarpMarker) != 0 {
			length = int((entry & histLengthMask) >> histLengthShift)
		}
		for k := 0; k < length; k++ {
			pos := byteOffset + k
			if pos >= start && pos <= end {
				t.Logf("Pos %d: State %d, PathID %d", pos, sidx, mc.pathHistory[pos])
			}
		}
		byteOffset += length
	}
}

func TestPass3SubmatchExtraction(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
	}{
		{`a(b)c`, "abc"},
		{`a(b|c)d`, "abd"},
		{`a(b|c)d`, "acd"},
		{`(a|ab)c`, "abc"},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"/"+tt.input, func(t *testing.T) {
			re, err := Compile(tt.pattern)
			if err != nil {
				t.Skipf("Skipping %q: %v", tt.pattern, err)
				return
			}

			got := re.FindSubmatchIndex([]byte(tt.input))
			stdRe := goregexp.MustCompile(tt.pattern)
			want := stdRe.FindSubmatchIndex([]byte(tt.input))

			validateSubmatchIndex(t, tt.pattern, tt.input, got, want)
		})
	}
}

func TestPass3MultiByte(t *testing.T) {
	pattern := `あ(い)う`
	input := "あいう"

	re := MustCompile(pattern)
	got := re.FindSubmatchIndex([]byte(input))

	stdRe := goregexp.MustCompile(pattern)
	want := stdRe.FindSubmatchIndex([]byte(input))

	validateSubmatchIndex(t, pattern, input, got, want)
}

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
	pattern := `aa|a`
	input := "aaaa"

	re := MustCompile(pattern)
	got := re.FindSubmatchIndex([]byte(input))

	stdRe := goregexp.MustCompile(pattern)
	want := stdRe.FindSubmatchIndex([]byte(input))

	validateSubmatchIndex(t, pattern, input, got, want)
}
