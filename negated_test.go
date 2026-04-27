package regexp

import (
	goregexp "regexp"
	"strings"
	"testing"
)

func TestCCWarpNegated(t *testing.T) {
	cases := []struct {
		pattern string
		input   string
		want    bool
	}{
		{`[^"]+`, "hello world", true},
		{`[^"]+`, `"`, false},
		{`[^ "]+`, "helloworld", true},
		{`[^ "]+`, " ", false},
		{`[^ "]+`, `"`, false},
	}

	for _, c := range cases {
		re, err := Compile(c.pattern)
		if err != nil {
			t.Errorf("Compile(%q) failed: %v", c.pattern, err)
			continue
		}
		if got := re.MatchString(c.input); got != c.want {
			t.Errorf("MatchString(%q, %q) = %v; want %v", c.pattern, c.input, got, c.want)
		}
	}
}

func BenchmarkCCWarpNegated(b *testing.B) {
	cases := []struct {
		name    string
		pattern string
		payload string
	}{
		{"NotQuote", `[^"]+`, strings.Repeat("A", 16384)},
		{"NotSpaceQuote", `[^ "]+`, strings.Repeat("A", 16384)},
	}

	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			input := []byte(c.payload)
			b.Run("Go", func(b *testing.B) {
				r := goregexp.MustCompile(c.pattern)
				b.ResetTimer()
				b.SetBytes(int64(len(input)))
				for i := 0; i < b.N; i++ {
					r.Match(input)
				}
			})
			b.Run("Re", func(b *testing.B) {
				r := MustCompile(c.pattern)
				b.ResetTimer()
				b.SetBytes(int64(len(input)))
				for i := 0; i < b.N; i++ {
					r.Match(input)
				}
			})
		})
	}
}
