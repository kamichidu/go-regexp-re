package regexp

import (
	"reflect"
	"testing"
)

func TestFindSubmatchIndex(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
		want    []int
	}{
		{`a`, "a", []int{0, 1}},
		{`(a)`, "a", []int{0, 1, 0, 1}},
		{`(a)b`, "ab", []int{0, 2, 0, 1}},
		{`a(b)c`, "abc", []int{0, 3, 1, 2}},
		{`(a)(b)c`, "abc", []int{0, 3, 0, 1, 1, 2}},
		{`(a*)b`, "aaab", []int{0, 4, 0, 3}},
		{`(a|b)c`, "ac", []int{0, 2, 0, 1}},
		{`(a|b)c`, "bc", []int{0, 2, 0, 1}},
		{`^(a)b$`, "ab", []int{0, 2, 0, 1}},
		{`^(a)b`, "ab", []int{0, 2, 0, 1}},
		{`(a)b$`, "ab", []int{0, 2, 0, 1}},
		{`\b(abc)\b`, "abc", []int{0, 3, 0, 3}},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"/"+tt.input, func(t *testing.T) {
			re, err := Compile(tt.pattern)
			if err != nil {
				t.Fatalf("Compile(%q) failed: %v", tt.pattern, err)
			}
			got := re.FindStringSubmatchIndex(tt.input)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("FindStringSubmatchIndex(%q, %q) = %v; want %v", tt.pattern, tt.input, got, tt.want)
			}
		})
	}
}

func TestFindStringSubmatch(t *testing.T) {
	re := MustCompile(`(a)(b)c`)
	got := re.FindStringSubmatch("abc")
	want := []string{"abc", "a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FindStringSubmatch = %v; want %v", got, want)
	}
}
