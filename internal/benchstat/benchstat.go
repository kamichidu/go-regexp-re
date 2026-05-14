package benchstat

import (
	"bufio"
	"strconv"
	"strings"
	"unicode"
)

// Report represents a structured benchstat output, categorized by metrics.
type Report struct {
	NsPerOp     *Section // Execution time (ns/op)
	MiBPerS     *Section // Throughput (MB/s)
	BytePerOp   *Section // Memory usage (B/op)
	AllocsPerOp *Section // Allocation count (allocs/op)
}

// Section holds all benchmark results for a specific metric.
type Section struct {
	Metric string // The metric name (e.g., "ns/op")
	Stats  []Stat
}

// Stat represents a single benchmark comparison result.
type Stat struct {
	Name     string   // Benchmark name
	Baseline float64  // Baseline value (normalized)
	Current  float64  // Current value (normalized)
	Delta    *float64 // Change percentage (positive for increase/regression)
	P        float64  // p-value
	N        int
}

type stateFn func(*parser) stateFn

type parser struct {
	scanner *bufio.Scanner
	report  *Report
	section *Section
	line    string
}

// ParseReport parses benchstat output into a structured Report using an FSM.
func ParseReport(input string) *Report {
	p := &parser{
		scanner: bufio.NewScanner(strings.NewReader(input)),
		report:  &Report{},
	}
	for state := stateSearchingHeader; state != nil; {
		state = state(p)
	}
	return p.report
}

func stateSearchingHeader(p *parser) stateFn {
	if !p.scanner.Scan() {
		return nil
	}
	p.line = p.scanner.Text()

	// Header line: "name  old ns/op  new ns/op  delta"
	if !strings.HasPrefix(p.line, "name") || !strings.Contains(p.line, "delta") {
		// Also support single column header: "name  ns/op"
		if !(strings.HasPrefix(p.line, "name") && (strings.Contains(p.line, "ns/op") || strings.Contains(p.line, "B/op"))) {
			return stateSearchingHeader
		}
	}

	switch {
	case strings.Contains(p.line, "ns/op"):
		p.report.NsPerOp = &Section{Metric: "ns/op"}
		p.section = p.report.NsPerOp
	case strings.Contains(p.line, "MB/s"):
		p.report.MiBPerS = &Section{Metric: "MB/s"}
		p.section = p.report.MiBPerS
	case strings.Contains(p.line, "B/op"):
		p.report.BytePerOp = &Section{Metric: "B/op"}
		p.section = p.report.BytePerOp
	case strings.Contains(p.line, "allocs/op"):
		p.report.AllocsPerOp = &Section{Metric: "allocs/op"}
		p.section = p.report.AllocsPerOp
	default:
		return stateSearchingHeader
	}

	return stateParsingStats
}

func stateParsingStats(p *parser) stateFn {
	if !p.scanner.Scan() {
		return nil
	}
	p.line = p.scanner.Text()
	trimmed := strings.TrimSpace(p.line)

	// A blank line strictly separates sections in benchstat.
	if trimmed == "" {
		return stateSearchingHeader
	}

	fields := strings.Fields(trimmed)
	if len(fields) < 2 {
		return stateParsingStats
	}

	// If it's a new header without a preceding blank line
	if fields[0] == "name" {
		return stateSearchingHeader
	}

	stat := Stat{Name: fields[0]}

	if len(fields) >= 10 && fields[2] == "±" {
		// A/B comparison format: [Name, Val1, ±, Err1, Val2, ±, Err2, Delta, (p=P, n=N+N)]
		stat.Baseline = parseValueUnit(fields[1])
		stat.Current = parseValueUnit(fields[4])

		if fields[7] != "~" {
			dStr := strings.TrimSuffix(fields[7], "%")
			d, _ := strconv.ParseFloat(dStr, 64)
			stat.Delta = &d
		}

		pStr := strings.TrimPrefix(fields[8], "(p=")
		stat.P, _ = strconv.ParseFloat(pStr, 64)

		nStr := strings.TrimPrefix(fields[9], "n=")
		if idx := strings.Index(nStr, "+"); idx != -1 {
			nStr = nStr[:idx]
		}
		stat.N, _ = strconv.Atoi(nStr)
	} else {
		// Single column format: [Name, Val, ±, Err]
		// Map the only data column to Baseline for architectural consistency.
		stat.Baseline = parseValueUnit(fields[1])
	}

	p.section.Stats = append(p.section.Stats, stat)
	return stateParsingStats
}

func parseValueUnit(s string) float64 {
	var val float64
	var unit string

	idx := strings.IndexFunc(s, func(r rune) bool {
		return !unicode.IsDigit(r) && r != '.'
	})

	if idx == -1 {
		val, _ = strconv.ParseFloat(s, 64)
		unit = ""
	} else {
		val, _ = strconv.ParseFloat(s[:idx], 64)
		unit = s[idx:]
	}

	// Handle SI prefixes and common units.
	multiplier := 1.0
	switch {
	case strings.HasSuffix(unit, "ns") || unit == "":
		multiplier = 1.0
	case strings.HasSuffix(unit, "µs"):
		multiplier = 1000
	case strings.HasSuffix(unit, "ms"):
		multiplier = 1000000
	case strings.HasSuffix(unit, "s"):
		multiplier = 1000000000
	case strings.HasSuffix(unit, "k"):
		multiplier = 1000
	case strings.HasSuffix(unit, "M"):
		multiplier = 1000000
	case strings.HasSuffix(unit, "G"):
		multiplier = 1000000000
	case strings.HasSuffix(unit, "KiB"):
		multiplier = 1024
	case strings.HasSuffix(unit, "MiB"):
		multiplier = 1024 * 1024
	case strings.HasSuffix(unit, "GiB"):
		multiplier = 1024 * 1024 * 1024
	case strings.HasSuffix(unit, "kB"):
		multiplier = 1024
	case strings.HasSuffix(unit, "MB"):
		multiplier = 1024 * 1024
	case strings.HasSuffix(unit, "GB"):
		multiplier = 1024 * 1024 * 1024
	case strings.HasSuffix(unit, "B"):
		multiplier = 1.0
	}
	return val * multiplier
}

// RatioScale is the multiplier used when comparing Ours vs Standard.
// A value of 1000.0 means parity (Ours == Standard).
const RatioScale = 1000.0

// ComputeRatio calculates the scaled ratio of two values (typically execution time).
// Ours / Std * 1000. Higher values mean Ours is slower.
func ComputeRatio(ours, std float64) float64 {
	if std == 0 {
		return 0
	}
	return (ours / std) * RatioScale
}

// ComputeRatioThroughput calculates the scaled ratio for throughput (MB/s).
// Std / Ours * 1000. This mirrors time-based ratio: higher values mean Ours is slower.
// Actually, to keep it consistent with "higher than 1000 means Ours is slower",
// for throughput it should be Std / Ours.
func ComputeRatioThroughput(ours, std float64) float64 {
	if ours == 0 {
		return 0
	}
	return (std / ours) * RatioScale
}
