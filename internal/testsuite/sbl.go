package testsuite

import (
	"encoding/json"
	"github.com/kamichidu/go-regexp-re/syntax"
	"math"
	"os"
	"sync"
)

// SBL represents the three axes of the performance landscape.
type SBL struct {
	S float64 `json:"s"` // Selectivity (0.0 to 1.0, 1.0 is most selective/rare)
	B float64 `json:"b"` // Branching Complexity (0.0 to 1.0, 1.0 is most complex)
	L float64 `json:"l"` // Locality/SIMD-friendliness (0.0 to 1.0, 1.0 is most local)
}

var (
	sblRegistry = make(map[string]SBL)
	sblMu       sync.Mutex
)

// RecordSBL registers the SBL metrics for a specific benchmark base name.
func RecordSBL(name string, s, b, l float64) {
	sblMu.Lock()
	defer sblMu.Unlock()
	sblRegistry[name] = SBL{S: s, B: b, L: l}
}

// ExportSBLRegistry writes the current registry to a JSON file.
func ExportSBLRegistry(path string) error {
	sblMu.Lock()
	defer sblMu.Unlock()

	data, err := json.MarshalIndent(sblRegistry, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// ComputeB calculates the Branching Complexity (B) from a regex AST.
func ComputeB(re *syntax.Regexp) float64 {
	if re == nil {
		return 0
	}

	var stats struct {
		Alts   int
		Quants int
		Depth  int
	}

	var walk func(*syntax.Regexp, int)
	walk = func(r *syntax.Regexp, depth int) {
		if depth > stats.Depth {
			stats.Depth = depth
		}

		switch r.Op {
		case syntax.OpAlternate:
			stats.Alts += len(r.Sub) - 1
		case syntax.OpStar, syntax.OpPlus, syntax.OpQuest, syntax.OpRepeat:
			stats.Quants++
		}

		for _, sub := range r.Sub {
			walk(sub, depth+1)
		}
	}
	walk(re, 0)

	// Formula based on @memo proposal
	rawB := float64(stats.Alts)*1.0 + float64(stats.Quants)*0.7 + float64(stats.Depth)*0.3

	// Sigmoid normalization to 0.0-1.0
	return 1.0 / (1.0 + math.Exp(-rawB/5.0))
}

// ComputeL calculates the Locality (L) traits from a regex AST.
// This is the pattern-side locality (literal density, jump expectedness).
func ComputeL(re *syntax.Regexp) float64 {
	if re == nil {
		return 0
	}

	totalLen := 0
	maxLiteral := 0

	var walk func(*syntax.Regexp)
	walk = func(r *syntax.Regexp) {
		switch r.Op {
		case syntax.OpLiteral:
			l := len(r.Rune)
			totalLen += l
			if l > maxLiteral {
				maxLiteral = l
			}
		case syntax.OpAnyChar, syntax.OpAnyCharNotNL, syntax.OpCharClass:
			totalLen++
		}
		for _, sub := range r.Sub {
			walk(sub)
		}
	}
	walk(re)

	if totalLen == 0 {
		return 0.5 // Neutral
	}

	contiguity := float64(maxLiteral) / float64(totalLen)
	return contiguity
}

// ComputeS calculates Selectivity (S) from match results.
func ComputeS(matchCount int, totalLength int) float64 {
	if totalLength == 0 {
		return 1.0
	}
	s := 1.0 - float64(matchCount)/float64(totalLength)
	if s < 0 {
		return 0
	}
	return s
}

// ComputeLInput calculates Locality (L) from input data traits.
func ComputeLInput(data []byte) float64 {
	if len(data) == 0 {
		return 0.5
	}
	// Simplified: compute character entropy
	counts := make([]int, 256)
	for _, b := range data {
		counts[b]++
	}

	entropy := 0.0
	for _, c := range counts {
		if c > 0 {
			p := float64(c) / float64(len(data))
			entropy -= p * math.Log2(p)
		}
	}

	// Max entropy for 256 chars is 8.0.
	// Low entropy means high locality/predictability.
	return 1.0 - (entropy / 8.0)
}
