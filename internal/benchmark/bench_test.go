package benchmark

import (
	"github.com/kamichidu/go-regexp-re/internal/testsuite"
	"testing"
)

func BenchmarkStandardSuite(b *testing.B) {
	testsuite.BenchmarkStandardSuite(b)
}

func BenchmarkLargeAlternation(b *testing.B) {
	testsuite.BenchmarkLargeAlternation(b)
}

func BenchmarkLiteralScan(b *testing.B) {
	testsuite.BenchmarkLiteralScan(b)
}

func BenchmarkAnchors(b *testing.B) {
	testsuite.BenchmarkAnchors(b)
}

func BenchmarkCapturing(b *testing.B) {
	testsuite.BenchmarkCapturing(b)
}

func BenchmarkNFAWorstCase(b *testing.B) {
	testsuite.BenchmarkNFAWorstCase(b)
}
