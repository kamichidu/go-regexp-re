//go:build re2_search

package benchmark

import (
	"github.com/kamichidu/go-regexp-re/internal/testsuite"
	"testing"
)

func BenchmarkStandardSuite(b *testing.B) {
	testsuite.BenchmarkStandardSuite(b)
}
