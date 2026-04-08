package regexp

import (
	"fmt"
	goregexp "regexp"
	"strings"
	"testing"
)

func BenchmarkCompileGo(b *testing.B) {
	for _, p := range goldenPatterns {
		b.Run(p.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = goregexp.MustCompile(p.pattern)
			}
		})
	}
}

func BenchmarkCompileRe(b *testing.B) {
	for _, p := range goldenPatterns {
		b.Run(p.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = MustCompile(p.pattern)
			}
		})
	}
}

func BenchmarkMatchGo(b *testing.B) {
	for _, p := range goldenPatterns {
		if !p.want {
			continue
		}
		b.Run(p.name, func(b *testing.B) {
			r := goregexp.MustCompile(p.pattern)
			input := []byte(p.payload)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r.Match(input)
			}
		})
	}
}

func BenchmarkMatchRe(b *testing.B) {
	for _, p := range goldenPatterns {
		if !p.want {
			continue
		}
		b.Run(p.name, func(b *testing.B) {
			r := MustCompile(p.pattern)
			input := []byte(p.payload)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r.Match(input)
			}
		})
	}
}

func BenchmarkSubmatchRe(b *testing.B) {
	for _, p := range goldenPatterns {
		if !p.want {
			continue
		}
		b.Run(p.name, func(b *testing.B) {
			r := MustCompile(p.pattern)
			input := []byte(p.payload)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r.FindSubmatchIndex(input)
			}
		})
	}
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
