package regexp

import (
	"reflect"
	goregexp "regexp"
	"testing"
)

func TestFindSubmatchIndex(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
	}{
		{`a`, "a"},
		{`(a)`, "a"},
		{`(a)b`, "ab"},
		{`a(b)c`, "abc"},
		{`(a)(b)c`, "abc"},
		{`(a*)b`, "aaab"},
		{`(a|b)c`, "ac"},
		{`(a|b)c`, "bc"},
		{`^(a)b$`, "ab"},
		{`^(a)b`, "ab"},
		{`(a)b$`, "ab"},
		{`\b(abc)\b`, "abc"},
		{`(|a)*`, "a"},
		{`(|a)*`, ""},
		{`(a|ab)`, "ab"},
		{`(a){0}`, ""},
		{`(a)?b`, "b"},
		{`(([^xyz]*)(d))`, "abcd"},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"/"+tt.input, func(t *testing.T) {
			re, err := Compile(tt.pattern)
			if err != nil {
				t.Fatalf("Compile(%q) failed: %v", tt.pattern, err)
			}
			got := re.FindStringSubmatchIndex(tt.input)
			stdRe := goregexp.MustCompile(tt.pattern)
			want := stdRe.FindSubmatchIndex([]byte(tt.input))

			if !reflect.DeepEqual(got, want) {
				t.Errorf("FindStringSubmatchIndex(%q, %q) = %v; want %v", tt.pattern, tt.input, got, want)
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
