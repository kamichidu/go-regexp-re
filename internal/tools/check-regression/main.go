package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kamichidu/go-regexp-re/internal/benchstat"
)

var (
	threshold = flag.Float64("threshold", 1.1, "Regression threshold multiplier (e.g. 1.1 for 10%)")
	mode      = flag.String("mode", "relative", "Check mode: absolute (Ours vs Standard) or relative (Current vs Baseline)")
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags] <benchstat_output.txt>\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		flag.Usage()
		os.Exit(1)
	}

	benchstatPath := args[0]

	checker := &RegressionChecker{
		Threshold: *threshold,
		Mode:      *mode,
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
	}

	if err := checker.Run(benchstatPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

type RegressionChecker struct {
	Threshold float64
	Mode      string
	Stdout    io.Writer
	Stderr    io.Writer
}

func (c *RegressionChecker) Run(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading benchstat file: %w", err)
	}

	report := benchstat.ParseReport(string(data))
	if report.MiBPerS == nil {
		panic("throughput (MB/s) section is missing from benchstat output")
	}
	if report.AllocsPerOp == nil {
		panic("allocations (allocs/op) section is missing from benchstat output")
	}

	var regressions []string

	switch c.Mode {
	case "absolute":
		regressions = c.CheckAbsolute(report)
	case "relative":
		regressions = c.CheckRelative(report)
	default:
		return fmt.Errorf("unknown mode: %s", c.Mode)
	}

	if len(regressions) > 0 {
		fmt.Fprintf(c.Stderr, "::error::Significant %s Performance Regressions Detected (threshold=%.2f):\n", strings.Title(c.Mode), c.Threshold)
		for _, msg := range regressions {
			fmt.Fprintln(c.Stderr, msg)
		}
		return fmt.Errorf("%s performance regression detected", c.Mode)
	}

	fmt.Fprintf(c.Stdout, "No significant %s regressions detected.\n", c.Mode)
	return nil
}

func (c *RegressionChecker) CheckAbsolute(report *benchstat.Report) []string {
	var regressions []string
	const epsilon = 1e-9

	// 1. Check Throughput (as scaled ratio: Std/Ours * 1000)
	// We only check throughput for absolute regression because some benchmarks
	// (like Capturing/Email) naturally have 1 alloc/op which would fail a strict absolute threshold.
	limit := c.Threshold * benchstat.RatioScale
	for _, stat := range report.MiBPerS.Stats {
		if stat.Baseline >= limit-epsilon {
			speedup := benchstat.RatioScale / stat.Baseline
			msg := fmt.Sprintf("[Absolute Regression] %s (%.2f) exceeded threshold in MB/s (Speedup: %.2fx)", stat.Name, stat.Baseline, speedup)
			regressions = append(regressions, msg)
		}
	}

	return regressions
}

func (c *RegressionChecker) CheckRelative(report *benchstat.Report) []string {
	var regressions []string
	const epsilon = 1e-9
	thresholdPercent := (c.Threshold - 1.0) * 100.0

	// 1. Check Throughput (Negative delta is regression, respects -threshold)
	for _, stat := range report.MiBPerS.Stats {
		if stat.Delta != nil && *stat.Delta <= -thresholdPercent+epsilon {
			msg := fmt.Sprintf("[Relative Regression] %s decreased by %.2f%% in MB/s (p=%.3f)", stat.Name, -*stat.Delta, stat.P)
			regressions = append(regressions, msg)
		}
	}

	// 2. Check Allocations (Zero tolerance: any increase is regression)
	for _, stat := range report.AllocsPerOp.Stats {
		if stat.Delta != nil && *stat.Delta > epsilon {
			msg := fmt.Sprintf("[Relative Regression] %s increased by %.2f%% in allocs/op (p=%.3f)", stat.Name, *stat.Delta, stat.P)
			regressions = append(regressions, msg)
		}
	}

	return regressions
}
