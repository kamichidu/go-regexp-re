package regexp

import (
	"fmt"
	goregexp "regexp"
	"strings"
	"testing"
)

func BenchmarkCompile(b *testing.B) {
	b.Run("Go", func(b *testing.B) {
		for _, p := range goldenPatterns {
			b.Run(p.name, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					_ = goregexp.MustCompile(p.pattern)
				}
			})
		}
	})
	b.Run("Re", func(b *testing.B) {
		for _, p := range goldenPatterns {
			b.Run(p.name, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					_ = MustCompile(p.pattern)
				}
			})
		}
	})
}

func BenchmarkMatch(b *testing.B) {
	b.Run("Go", func(b *testing.B) {
		for _, p := range goldenPatterns {
			b.Run(p.name, func(b *testing.B) {
				r := goregexp.MustCompile(p.pattern)
				input := []byte(p.payload)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					r.Match(input)
				}
			})
		}
	})
	b.Run("Re", func(b *testing.B) {
		for _, p := range goldenPatterns {
			b.Run(p.name, func(b *testing.B) {
				r, err := Compile(p.pattern)
				if err != nil {
					b.Fatalf("Compile %s failed: %v", p.pattern, err)
				}
				input := []byte(p.payload)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					r.Match(input)
				}
			})
		}
	})
}

func BenchmarkSubmatch(b *testing.B) {
	b.Run("Go", func(b *testing.B) {
		for _, p := range goldenPatterns {
			b.Run(p.name, func(b *testing.B) {
				r := goregexp.MustCompile(p.pattern)
				input := []byte(p.payload)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					r.FindSubmatchIndex(input)
				}
			})
		}
	})
	b.Run("Re", func(b *testing.B) {
		for _, p := range goldenPatterns {
			b.Run(p.name, func(b *testing.B) {
				r, err := Compile(p.pattern)
				if err != nil {
					b.Fatalf("Compile %s failed: %v", p.pattern, err)
				}
				input := []byte(p.payload)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					r.FindSubmatchIndex(input)
				}
			})
		}
	})
}

func BenchmarkComplexity(b *testing.B) {
	lengths := []int{10, 100, 1000, 10000}
	pattern := "a*b"

	fn := func(b *testing.B, re interface{ Match([]byte) bool }) {
		for _, n := range lengths {
			input := []byte(strings.Repeat("a", n))
			b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					re.Match(input)
				}
			})
		}
	}
	b.Run("Go", func(b *testing.B) {
		fn(b, goregexp.MustCompile(pattern))
	})
	b.Run("Re", func(b *testing.B) {
		fn(b, MustCompile(pattern))
	})
}

func BenchmarkIP(b *testing.B) {
	cases := []struct {
		name    string
		pattern string
	}{
		{"Unanchored", `127.0.0.1`},
		{"Start", `^127.0.0.1`},
		{"End", `127.0.0.1$`},
		{"Exact", `^127.0.0.1$`},
		{"Word", `\b127.0.0.1\b`},
		{"Literal", `^127\.0\.0\.1$`},
	}
	input := []byte("127.0.0.1")

	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.Run("Go", func(b *testing.B) {
				r := goregexp.MustCompile(c.pattern)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					r.Match(input)
				}
			})
			b.Run("Re", func(b *testing.B) {
				r := MustCompile(c.pattern)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					r.Match(input)
				}
			})
		})
	}
}

func BenchmarkCCWarp(b *testing.B) {
	cases := []struct {
		name    string
		pattern string
		payload string
	}{
		// 1. Single Character repetition
		{"Equal", `a+`, strings.Repeat("a", 16384)},
		// 2. Single Range repetition
		{"SingleRange", `[0-9]+`, strings.Repeat("12345678", 2048)},
		// 3. Disjoint set repetition
		{"EqualSet", `[aeiou]+`, strings.Repeat("aeiouaei", 2048)},
		// 4. Matches everything (dot-all)
		{"AnyChar", `(?s).*`, strings.Repeat("ANYTHING", 2048)},
		// 5. Matches everything except NL
		{"AnyExceptNL", `.*`, strings.Repeat("NoNewLin", 2048)},
		// 6. Negated single character
		{"NotEqual", `[^"]+`, strings.Repeat("NoQuotes", 2048)},
		// 7. Negated single range
		{"NotSingleRange", `[^0-9]+`, strings.Repeat("NoDigits", 2048)},
		// 8. Negated small set
		{"NotEqualSet", `[^ "]+`, strings.Repeat("NoSpaceQ", 2048)},
		// 9. Standard Bitmask
		{"Bitmask", `[a-zA-Z0-9_]+`, strings.Repeat("Word1234", 2048)},
		// 10. Negated Bitmask
		{"NotBitmask", `[^a-z]+`, strings.Repeat("12345678", 2048)},
	}

	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			input := []byte(c.payload)
			b.Run("Go", func(b *testing.B) {
				r := goregexp.MustCompile(c.pattern)
				b.ReportAllocs()
				b.SetBytes(int64(len(input)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					r.Match(input)
				}
			})
			b.Run("Re", func(b *testing.B) {
				r := MustCompile(c.pattern)
				b.SetBytes(int64(len(input)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					r.Match(input)
				}
			})
		})
	}
}
