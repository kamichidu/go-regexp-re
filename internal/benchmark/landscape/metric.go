package landscape

import (
	"github.com/kamichidu/go-regexp-re/internal/testsuite"
	"github.com/kamichidu/go-regexp-re/syntax"
)

// SBL represents the three axes of the performance landscape.
type SBL = testsuite.SBL

// ComputeB calculates the Branching Complexity (B) from a regex AST.
func ComputeB(re *syntax.Regexp) float64 {
	return testsuite.ComputeB(re)
}

// ComputeL calculates the Locality (L) traits from a regex AST.
func ComputeL(re *syntax.Regexp) float64 {
	return testsuite.ComputeL(re)
}

// ComputeS calculates Selectivity (S) from match results.
func ComputeS(matchCount int, totalLength int) float64 {
	return testsuite.ComputeS(matchCount, totalLength)
}

// ComputeLInput calculates Locality (L) from input data traits.
func ComputeLInput(data []byte) float64 {
	return testsuite.ComputeLInput(data)
}
