package regexp

import (
	"reflect"
	"testing"
)

func TestSparseTDFA_PathSelection(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
		want    []int
	}{
		{"a|ab", "ab", []int{0, 1}},
		{"a(b|c)d", "acd", []int{0, 3, 1, 2}},
		{"(a*)b", "aaab", []int{0, 4, 0, 3}},
	}

	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			re := MustCompile(tt.pattern)
			re.strategy = strategyExtended

			got := re.FindSubmatchIndex([]byte(tt.input))
			if got == nil {
				t.Fatal("Match failed")
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v; want %v", got, tt.want)
			}
		})
	}
}

func TestGreedyPriority(t *testing.T) {
	re := MustCompile("(a*)a")
	re.strategy = strategyExtended

	input := []byte("aaa")
	got := re.FindSubmatchIndex(input)
	if got == nil {
		t.Fatal("Match failed")
	}
	// (a*)a on aaa: a*="aa", a="a" -> [0, 3, 0, 2]
	want := []int{0, 3, 0, 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Greedy failed: expected %v, got %v", want, got)
	}
}

func TestPathSelectionWithHistory(t *testing.T) {
	re := MustCompile("a(b|c)d")
	re.strategy = strategyExtended

	input := []byte("acd")
	got := re.FindSubmatchIndex(input)
	// a(b|c)d on acd -> [0, 3, 1, 2]
	want := []int{0, 3, 1, 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Path selection failed: expected %v, got %v", want, got)
	}
}
