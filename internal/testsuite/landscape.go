package testsuite

import (
	"fmt"
	"github.com/kamichidu/go-regexp-re/syntax"
	"math/rand"
	"strings"
	"testing"
)

// BenchmarkLandscape sweeps S (Selectivity), B (Complexity), and L (Locality).
func BenchmarkLandscape(b *testing.B) {
	sValues := []float64{0.01, 0.1, 0.5, 0.9}
	bValues := []int{1, 10, 50}
	lValues := []float64{0.1, 0.9}

	for _, s := range sValues {
		for _, bv := range bValues {
			for _, l := range lValues {
				pattern := generatePattern(bv)
				input := generateInput(1*1024*1024, s, l)

				ast, err := syntax.Parse(pattern, syntax.Perl)
				if err != nil {
					continue
				}

				// Compute actual SBL metrics
				b_val := ComputeB(ast)
				l_val := ComputeL(ast)

				benchName := fmt.Sprintf("S=%.2f/B=%d/L=%.2f", s, bv, l)
				RecordSBL("Landscape/"+benchName, s, b_val, l_val)

				runOnEngines(b, func(b *testing.B, engine Engine) {
					re, err := engine.Compile(pattern)
					if err != nil {
						return
					}
					b.Run(benchName, func(b *testing.B) {
						b.SetBytes(int64(len(input)))
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							re.MatchString(input)
						}
					})

				})
			}
		}
	}
}

func generatePattern(complexity int) string {
	if complexity <= 1 {
		return "abc"
	}
	alts := make([]string, complexity)
	for i := 0; i < complexity; i++ {
		alts[i] = fmt.Sprintf("alt%d", i)
	}
	return strings.Join(alts, "|")
}

func generateInput(size int, s float64, l float64) string {
	res := make([]byte, size)
	r := rand.New(rand.NewSource(42))

	for i := 0; i < size; i++ {
		res[i] = 'x'
	}

	matchCount := int(float64(size) * s)
	if matchCount == 0 {
		return string(res)
	}

	clusterSize := int(1.0 / (1.01 - l))
	if clusterSize < 1 {
		clusterSize = 1
	}

	for i := 0; i < matchCount; {
		start := r.Intn(size - clusterSize)
		for j := 0; j < clusterSize && i < matchCount; j++ {
			res[start+j] = 'a'
			i++
		}
	}

	return string(res)
}
