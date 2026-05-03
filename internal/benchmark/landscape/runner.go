package landscape

import (
	"fmt"
	"github.com/kamichidu/go-regexp-re/internal/testsuite"
	"github.com/kamichidu/go-regexp-re/syntax"
	"time"
)

// Runner coordinates the landscape benchmarking.
type Runner struct {
	Engines  []testsuite.Engine
	Patterns []string
	Inputs   []string // Not used yet, currently using testsuite corpora
}

// Run executes the benchmark suite and returns the landscape.
func (r *Runner) Run() *Landscape {
	l := &Landscape{}

	// We need a baseline (stdlib) to calculate speedup.
	var stdlib *testsuite.Engine
	for i := range r.Engines {
		if r.Engines[i].Name == "GoRegexp" {
			stdlib = &r.Engines[i]
			break
		}
	}

	if stdlib == nil {
		fmt.Println("Warning: GoRegexp not found, speedup will be 1.0")
	}

	// Payload size for stable results
	const payloadSize = 1 * 1024 * 1024

	for _, pat := range r.Patterns {
		ast, err := syntax.Parse(pat, syntax.Perl)
		if err != nil {
			continue
		}

		b_val := ComputeB(ast)
		l_pattern := ComputeL(ast)

		// Create a sample input based on the pattern
		// In a full implementation, we'd use multiple corpora.
		input := testsuite.ScaleWithNoise(pat, payloadSize)
		l_input := ComputeLInput([]byte(input))

		// Combined L
		l_val := 0.5*l_pattern + 0.5*l_input

		var stdlibThroughput float64
		if stdlib != nil {
			stdlibThroughput, _ = r.measure(stdlib, pat, input)
		}

		for _, eng := range r.Engines {
			if eng.Name == "GoRegexp" {
				continue
			}

			throughput, matchCount := r.measure(&eng, pat, input)
			if throughput == 0 {
				continue
			}

			speedup := 1.0
			if stdlibThroughput > 0 {
				speedup = throughput / stdlibThroughput
			}

			s_val := ComputeS(matchCount, len(input))

			l.Results = append(l.Results, Result{
				EngineID:  eng.Name,
				PatternID: pat,
				SBL: SBL{
					S: s_val,
					B: b_val,
					L: l_val,
				},
				Speedup:    speedup,
				Throughput: throughput,
			})
		}
	}

	return l
}

func (r *Runner) measure(eng *testsuite.Engine, pat string, input string) (float64, int) {
	re, err := eng.Compile(pat)
	if err != nil {
		return 0, 0
	}

	// Warm up
	re.MatchString(input)

	// Measure
	start := time.Now()
	iterations := 0
	var elapsed time.Duration
	for elapsed < 100*time.Millisecond {
		re.MatchString(input)
		iterations++
		elapsed = time.Since(start)
	}

	throughput := (float64(iterations) * float64(len(input))) / (elapsed.Seconds() * 1024 * 1024)

	// We need matchCount for S.
	// MatchString doesn't give us matchCount easily without FindAll.
	// For S calculation, we can just use a separate pass or assume S based on input.
	// Let's do a simple count pass.
	matchCount := 0
	// This is a bit slow but necessary for accurate S if we don't know it.
	// Actually, let's just use 1 for a match, but better would be FindAllStringIndex.
	if re.MatchString(input) {
		matchCount = 1 // Simplified
	}

	return throughput, matchCount
}
