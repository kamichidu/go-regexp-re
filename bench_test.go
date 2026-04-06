package regexp

import (
	goregexp "regexp"
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
