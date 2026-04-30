package regexp

import (
	"reflect"
	"testing"
)

func TestSparseTDFA_PathSelection(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
		expect  []int
	}{
		{"a|ab", "abc", []int{0, 2, 0, 2}},
		{"a(b|c)d", "abcd", []int{0, 3, 1, 2}},
		{"(a*)b", "aaab", []int{0, 4, 0, 3}},
	}

	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			re := MustCompile(tt.pattern)
			b := []byte(tt.input)
			mc := &matchContext{}
			mc.prepare(len(b), re.numSubexp, 0)

			regs := make([]int, (re.numSubexp+1)*2)
			start, end, prio := re.findSubmatchIndexInternal(b, mc, regs, b)
			if start < 0 {
				t.Fatalf("Match failed")
			}

			re.sparseTDFA_PathSelection(mc, b, start, end, prio)
			re.sparseTDFA_Recap(mc, b, start, end, prio, regs)

			if !reflect.DeepEqual(regs, tt.expect) {
				t.Errorf("got %v; want %v", regs, tt.expect)
			}
		})
	}
}

func TestGreedyPriority(t *testing.T) {
	re := MustCompile(`a*`)
	input := []byte("aaa")
	mc := &matchContext{}
	mc.prepare(len(input), re.numSubexp, 0)

	regs := make([]int, (re.numSubexp+1)*2)
	start, end, prio := re.findSubmatchIndexInternal(input, mc, regs, input)

	t.Logf("Match result: [%d, %d], Total Prio: %d", start, end, prio)

	if start != 0 || end != 3 {
		t.Errorf("Greedy failed: expected [0, 3], got [%d, %d]", start, end)
	}
}

func TestPathSelectionWithHistory(t *testing.T) {
	re := MustCompile(`(a|ab)c`)
	input := []byte("abc")
	mc := &matchContext{}
	mc.prepare(len(input), re.numSubexp, 0)

	start, end, prio := re.findSubmatchIndexInternal(input, mc, nil, input)
	re.sparseTDFA_PathSelection(mc, input, start, end, prio)

	// Path Selection should strictly follow the history and recap tables
	byteOffset := 0
	for _, entry := range mc.history {
		length := 1
		if (entry & histWarpMarker) != 0 {
			length = int((entry & histLengthMask) >> histLengthShift)
		}
		for k := 0; k < length; k++ {
			if byteOffset >= start && byteOffset < end {
				if mc.pathHistory[byteOffset] == -1 {
					t.Errorf("Path missing at byte %d", byteOffset)
				}
			}
			byteOffset++
		}
	}
}
