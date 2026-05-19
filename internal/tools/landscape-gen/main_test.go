package main

import (
	"strings"
	"testing"
)

func TestParseBenchmarks(t *testing.T) {
	registry := BenchRegistry{
		"StandardSuite/a+": {S: 0.1, B: 0.5, L: 0.8},
		"Landscape/S=0.01/B=1/L=0.10": {S: 0.01, B: 1.0, L: 0.1},
	}

	input := `
BenchmarkStandardSuite/a+/GoRegexpRe-2   1000   100 ns/op   1000.0 MB/s
BenchmarkStandardSuite/GoRegexp/a+-2   1000   200 ns/op   500.0 MB/s
BenchmarkLandscape/S=0.01/B=1/L=0.10/GoRegexpRe-2   500   500 ns/op   200.0 MB/s
BenchmarkLandscape/GoRegexp/S=0.01/B=1/L=0.10-2   500   1000 ns/op   100.0 MB/s
`

	results := parseBenchmarks(strings.NewReader(input), registry)

	expectedCount := 4
	if len(results) != expectedCount {
		t.Errorf("expected %d results, got %d", expectedCount, len(results))
	}

	// Verify specific cases
	findResult := func(engine, category string) *BenchResult {
		for _, r := range results {
			if r.Engine == engine && r.Category == category {
				return &r
			}
		}
		return nil
	}

	tests := []struct {
		engine     string
		category   string
		throughput float64
	}{
		{"GoRegexpRe", "StandardSuite/a+", 1000.0},
		{"GoRegexp", "StandardSuite/a+", 500.0},
		{"GoRegexpRe", "Landscape/S=0.01/B=1/L=0.10", 200.0},
		{"GoRegexp", "Landscape/S=0.01/B=1/L=0.10", 100.0},
	}

	for _, tt := range tests {
		r := findResult(tt.engine, tt.category)
		if r == nil {
			t.Errorf("could not find result for %s/%s", tt.engine, tt.category)
			continue
		}
		if r.Throughput != tt.throughput {
			t.Errorf("%s/%s: expected throughput %.1f, got %.1f", tt.engine, tt.category, tt.throughput, r.Throughput)
		}
	}
}

func TestParseBenchmarks_Averaging(t *testing.T) {
	registry := BenchRegistry{
		"Suite/Test": {S: 0.5, B: 0.5, L: 0.5},
	}

	input := `
BenchmarkSuite/Test/GoRegexpRe-2   1000   100 ns/op   100.0 MB/s
BenchmarkSuite/Test/GoRegexpRe-2   1000   100 ns/op   200.0 MB/s
`

	results := parseBenchmarks(strings.NewReader(input), registry)

	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}

	if results[0].Throughput != 150.0 {
		t.Errorf("expected averaged throughput 150.0, got %.1f", results[0].Throughput)
	}
}

func TestIsEngine(t *testing.T) {
	engines := []string{"GoRegexp", "GoRegexpRe", "Coregex", "Hyperscan-CGO", "PCRE2-CGO", "RE2-CGO", "RE2-Wasm"}
	for _, e := range engines {
		if !isEngine(e) {
			t.Errorf("expected %s to be recognized as an engine", e)
		}
	}
	if isEngine("NotAnEngine") {
		t.Error("expected NotAnEngine to not be recognized")
	}
}
