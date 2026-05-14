package main

import (
	"strings"
	"testing"

	"github.com/kamichidu/go-regexp-re/internal/benchstat"
)

func TestParseBenchstat(t *testing.T) {
	output := `
name      old MB/s   new MB/s   delta
Improve   1000 ± 5%  1200 ± 5%  +20.00%  (p=0.001 n=10+10)
Regress   1000 ± 2%  800 ± 2%   -20.00%  (p=0.001 n=10+10)

name      old allocs/op  new allocs/op  delta
Other     1.00 ± 0%      1.01 ± 0%      +1.00%   (p=0.001 n=10+10)
`
	report := benchstat.ParseReport(output)
	checker := &RegressionChecker{Threshold: 1.1} // 10% threshold for throughput

	// 1. Test relative regression logic (CheckRelative)
	relRegressions := checker.CheckRelative(report)
	// MiBPerS: Regress(-20%) IS a regression. Improve(+20%) is NOT.
	// Allocs: Other(+1%) IS a regression because of zero-tolerance.
	if len(relRegressions) != 2 {
		t.Errorf("expected 2 relative regressions (Regress throughput and Other allocs), got %d: %v", len(relRegressions), relRegressions)
	}

	foundRegress := false
	foundOther := false
	for _, msg := range relRegressions {
		if strings.Contains(msg, "Regress") {
			foundRegress = true
		}
		if strings.Contains(msg, "Other") {
			foundOther = true
		}
		if strings.Contains(msg, "Improve") {
			t.Errorf("Improvement (positive delta in ratio) was incorrectly flagged as regression: %s", msg)
		}
	}
	if !foundRegress || !foundOther {
		t.Errorf("missing expected regressions: regress=%v, other=%v", foundRegress, foundOther)
	}

	// 2. Test absolute regression logic (CheckAbsolute)
	absReport := &benchstat.Report{
		MiBPerS: &benchstat.Section{
			Metric: "MB/s",
			Stats: []benchstat.Stat{
				{Name: "SlowThroughput", Baseline: 800.0},
				{Name: "FastThroughput", Baseline: 1200.0},
			},
		},
		AllocsPerOp: &benchstat.Section{
			Metric: "allocs/op",
			Stats: []benchstat.Stat{
				{Name: "ManyAllocs", Baseline: 2.0},
			},
		},
	}
	absRegressions := checker.CheckAbsolute(absReport)
	// Only SlowThroughput should be flagged (Baseline 800 <= 1000/1.1)
	if len(absRegressions) != 1 || !strings.Contains(absRegressions[0], "SlowThroughput") {
		t.Errorf("expected 1 absolute regression in SlowThroughput, got %v", absRegressions)
	}
}

func TestIsRegression(t *testing.T) {
	checker := &RegressionChecker{Threshold: 1.1}

	// Test throughput ratio check (CheckAbsolute)
	report := &benchstat.Report{
		MiBPerS: &benchstat.Section{
			Metric: "MB/s",
			Stats: []benchstat.Stat{
				{Name: "R1", Baseline: 800.0},  // Regression (ratio < 909)
				{Name: "R2", Baseline: 1000.0}, // Pass
			},
		},
	}
	regs := checker.CheckAbsolute(report)
	if len(regs) != 1 || !strings.Contains(regs[0], "R1") {
		t.Errorf("failed ratio regression check: %v", regs)
	}

	// Test allocs relative check (CheckRelative)
	reportAllocs := &benchstat.Report{
		MiBPerS: &benchstat.Section{Metric: "MB/s"},
		AllocsPerOp: &benchstat.Section{
			Metric: "allocs/op",
			Stats: []benchstat.Stat{
				{Name: "A1", Delta: ptr(1.0)}, // Regression (zero-tolerance)
				{Name: "A2", Delta: ptr(0.0)}, // Pass
			},
		},
	}
	regsAllocs := checker.CheckRelative(reportAllocs)
	if len(regsAllocs) != 1 || !strings.Contains(regsAllocs[0], "A1") {
		t.Errorf("failed relative allocation regression check: %v", regsAllocs)
	}
}

func ptr(f float64) *float64 { return &f }
