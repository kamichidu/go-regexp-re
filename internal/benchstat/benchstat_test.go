package benchstat

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseReport_TestData(t *testing.T) {
	path := filepath.Join("testdata", "benchstat_delta.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read testdata: %v", err)
	}

	report := ParseReport(string(data))

	if report.NsPerOp == nil {
		t.Fatalf("expected NsPerOp section to be parsed")
	}

	// benchstat_delta.txt has 11 ns/op stats
	if len(report.NsPerOp.Stats) != 11 {
		t.Errorf("expected 11 stats in NsPerOp, got %d", len(report.NsPerOp.Stats))
	}

	var foundRegression bool
	for _, stat := range report.NsPerOp.Stats {
		if stat.Name == "RegressionCase" {
			foundRegression = true
			if stat.Baseline != 100.0 || stat.Current != 120.0 {
				t.Errorf("incorrect values for RegressionCase: baseline=%.1f, current=%.1f", stat.Baseline, stat.Current)
			}
			if stat.Delta == nil || *stat.Delta != 20.0 {
				t.Errorf("expected 20.0%% delta for RegressionCase, got %v", stat.Delta)
			}
		}
		if stat.Name == "SearchWarp" {
			// 35.4ms -> 35400000ns
			if stat.Baseline != 35400000 {
				t.Errorf("expected 35400000ns baseline for SearchWarp, got %.1f", stat.Baseline)
			}
			if stat.Delta == nil || *stat.Delta != -98.85 {
				t.Errorf("expected -98.85%% delta for SearchWarp, got %v", stat.Delta)
			}
		}
	}

	if !foundRegression {
		t.Errorf("RegressionCase not found in results")
	}
}

func TestComputeRatio(t *testing.T) {
	tests := []struct {
		ours, std float64
		want      float64
	}{
		{50, 100, 500.0},
		{200, 100, 2000.0},
		{100, 100, 1000.0},
		{100, 0, 0.0},
	}
	for _, tt := range tests {
		if got := ComputeRatio(tt.ours, tt.std); got != tt.want {
			t.Errorf("ComputeRatio(%.1f, %.1f) = %.1f, want %.1f", tt.ours, tt.std, got, tt.want)
		}
	}
}

func TestComputeRatioThroughput(t *testing.T) {
	tests := []struct {
		ours, std float64
		want      float64
	}{
		{200, 100, 2000.0}, // 2x faster throughput -> ratio 2000
		{50, 100, 500.0},   // 0.5x throughput -> ratio 500
		{100, 100, 1000.0},
		{0, 100, 0.0},
	}
	for _, tt := range tests {
		if got := ComputeRatioThroughput(tt.ours, tt.std); got != tt.want {
			t.Errorf("ComputeRatioThroughput(%.1f, %.1f) = %.1f, want %.1f", tt.ours, tt.std, got, tt.want)
		}
	}
}

func TestParseValueUnit(t *testing.T) {
	tests := []struct {
		input string
		want  float64
	}{
		{"500", 500},
		{"500ns", 500},
		{"1.5µs", 1500},
		{"2.0ms", 2000000},
		{"1s", 1000000000},
		{"10B", 10},
		{"1KiB", 1024},
		{"1MB", 1048576},
		{"1MiB", 1048576},
		{"1GB", 1073741824},
		{"5.00", 5},
	}

	for _, tt := range tests {
		if got := parseValueUnit(tt.input); got != tt.want {
			t.Errorf("parseValueUnit(%q) = %.1f, want %.1f", tt.input, got, tt.want)
		}
	}
}
