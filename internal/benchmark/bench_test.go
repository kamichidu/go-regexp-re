package benchmark

import (
	"github.com/kamichidu/go-regexp-re/internal/testsuite"
	"testing"
)

func BenchmarkLargeAlternation(b *testing.B) {
	testsuite.BenchmarkLargeAlternation(b)
}
