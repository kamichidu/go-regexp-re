package regexp

import (
	"testing"
)

func TestPriorityGreedyLoop(t *testing.T) {
	// a* は a を繰り返すパス(Prio 0)と、空文字で抜けるパス(Prio 1)がある。
	// Greedy であれば Prio 0 を最優先し、可能な限り長くマッチすべき。
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

	// 履歴を確認
	for i := 0; i <= len(input); i++ {
		t.Logf("Pos %d: State %d", i, mc.history[i])
	}
}

func TestPriorityAlternation(t *testing.T) {
	// (a|aa) の leftmost-first ループ
	// aa が後にあっても、標準Goでは a が優先される(非Greedy的振る舞い)か、
	// あるいは全体の最長一致を優先するかはパターンの構造に依存。
	// syntax.Simplify を通すと (?:aa|a) と最適化されることもある。
	re := MustCompile(`a|aa`)
	input := []byte("aa")

	got := re.FindSubmatchIndex(input)
	t.Logf("Pattern a|aa on 'aa': %v", got)
	// Go標準: "a|aa" on "aa" -> "a" [0, 1]
	if got[1] != 1 {
		t.Errorf("Leftmost-first failed: expected end 1, got %d", got[1])
	}
}

func TestPriorityAbsoluteTracking(t *testing.T) {
	// 憲法 2.12: Priority Normalization & Absolute Tracking の検証
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
