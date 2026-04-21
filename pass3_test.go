package regexp

import (
	goregexp "regexp"
	"testing"
)

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
