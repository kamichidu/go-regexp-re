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
				// We still need the loop for b.N, but we use it sparingly if it's slow.
				// However, standard benchmark practice is to run b.N times.
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
				// Use the cache if available from tests, or just compile once here.
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
		{"Digits", `[0-9]+`, strings.Repeat("1234567890123456", 1024)}, // 16KB of digits
		{"Word", `[a-zA-Z0-9_]+`, strings.Repeat("word1234_5678_word", 1024)},
		{"Dot", `.*`, strings.Repeat("hello_swar_warp_8bytes", 1024)},
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
			b.Run("Re-Submatch", func(b *testing.B) {
				r := MustCompile(c.pattern)
				b.ResetTimer()
				b.SetBytes(int64(len(input)))
				for i := 0; i < b.N; i++ {
					r.FindSubmatchIndex(input)
				}
			})
		})
	}
}
