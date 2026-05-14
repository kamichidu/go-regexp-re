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

	// Collect names of parsed sections for debug
	var parsed []string
	if report.NsPerOp != nil {
		parsed = append(parsed, "ns/op")
	}
	if report.MiBPerS != nil {
		parsed = append(parsed, "MB/s")
	}
	if report.BytePerOp != nil {
		parsed = append(parsed, "B/op")
	}
	if report.AllocsPerOp != nil {
		parsed = append(parsed, "allocs/op")
	}

	if report.MiBPerS == nil {
		panic(fmt.Sprintf("throughput (MB/s) section is missing from benchstat output (parsed: %v)", parsed))
	}
	if report.AllocsPerOp == nil {
		panic(fmt.Sprintf("allocations (allocs/op) section is missing from benchstat output (parsed: %v)", parsed))
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

	// 1. Check Throughput (as scaled ratio: Ours/Std * 1000)
	// Higher is better. Regression if Ours < Std / Threshold.
	if report.MiBPerS != nil {
		limit := benchstat.RatioScale / c.Threshold
		for _, stat := range report.MiBPerS.Stats {
			if stat.Baseline <= limit+epsilon {
				speedup := stat.Baseline / benchstat.RatioScale
				msg := fmt.Sprintf("[Absolute Regression] %s (%.2f) below threshold in MB/s (Speedup: %.2fx)", stat.Name, stat.Baseline, speedup)
				regressions = append(regressions, msg)
			}
		}
	}

	return regressions
}

func (c *RegressionChecker) CheckRelative(report *benchstat.Report) []string {
	var regressions []string
	const epsilon = 1e-9
	thresholdPercent := (c.Threshold - 1.0) * 100.0

	// 1. Check Throughput (Higher is better, negative delta is regression)
	for _, stat := range report.MiBPerS.Stats {
		// Regression if throughput decreased by more than threshold
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
