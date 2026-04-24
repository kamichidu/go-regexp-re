package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	t.Run("FullDataset", func(t *testing.T) {
		f, err := os.Open("testdata/benchmark-main.txt")
		if err != nil {
			t.Fatalf("failed to open testdata: %v", err)
		}
		defer f.Close()

		var out bytes.Buffer
		if err := run(f, &out); err != nil {
			t.Fatalf("run failed: %v", err)
		}

		if out.Len() == 0 {
			t.Error("output is empty")
		}

		// Check for space between value and unit
		output := out.String()
		if !strings.Contains(output, " ns/op") {
			t.Error("output should contain space before unit (e.g., ' ns/op')")
		}
	})

	t.Run("HybridMetricCalculation", func(t *testing.T) {
		input := `
BenchmarkSynthetic/GoRegexp/Test-8       1  100.0 ns/op  100.0 MB/s  1000.0 B/op  10 allocs/op
BenchmarkSynthetic/GoRegexpRe/Test-8     1   50.0 ns/op  500.0 MB/s   200.0 B/op   1 allocs/op
`
		var out bytes.Buffer
		if err := run(strings.NewReader(input), &out); err != nil {
			t.Fatalf("run failed: %v", err)
		}

		// Expected output:
		// BenchmarkSynthetic/Test  1  500.000000 ns/op  500.000000 MB/s  200 B/op  1 allocs/op
		got := out.String()
		if !strings.Contains(got, "500.000000 ns/op") {
			t.Errorf("output missing expected space-separated unit, got: %q", got)
		}
	})
}
