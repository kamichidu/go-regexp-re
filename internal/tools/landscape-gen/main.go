package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/kamichidu/go-regexp-re"
)

type BenchResult struct {
	Engine     string  `json:"engine"`
	Category   string  `json:"category,omitempty"`
	S          float64 `json:"s"`
	B          float64 `json:"b"`
	L          float64 `json:"l"`
	Throughput float64 `json:"throughput"` // MB/s
	Regime     string  `json:"regime,omitempty"`
	TraceID    string  `json:"trace_id,omitempty"`
}

type Key struct {
	Engine   string
	Category string
	S        float64
	B        float64
	L        float64
}

type SBL struct {
	S       float64 `json:"s"`
	B       float64 `json:"b"`
	L       float64 `json:"l"`
	Pattern string  `json:"pattern"`
}

type BenchRegistry map[string]SBL

func main() {
	if len(os.Args) < 3 {
		fmt.Println("Usage: go run main.go <benchmark_output.txt> <output.json>")
		os.Exit(1)
	}

	benchFile, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Printf("Error opening benchmark file: %v\n", err)
		os.Exit(1)
	}
	defer benchFile.Close()

	outputFilePath := os.Args[2]
	traceDir := filepath.Join(filepath.Dir(outputFilePath), "trace")
	os.RemoveAll(traceDir)
	os.MkdirAll(traceDir, 0755)

	// Use the stable definitions file in the repo
	registryFile, err := os.ReadFile("internal/testsuite/sbl_definitions.json")
	if err != nil {
		fmt.Printf("Error opening SBL definitions: %v\n", err)
		os.Exit(1)
	}
	var registry BenchRegistry
	if err := json.Unmarshal(registryFile, &registry); err != nil {
		fmt.Printf("Error parsing SBL definitions: %v\n", err)
		os.Exit(1)
	}

	results := parseBenchmarks(benchFile, registry)

	// Enrich results with Regime and generate traces in parallel
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			r := &results[idx]
			if r.Engine != "GoRegexpRe" {
				return
			}

			sbl, ok := registry[r.Category]
			if !ok || sbl.Pattern == "" {
				return
			}

			// Use a stable TraceID based on category
			slug := strings.ToLower(r.Category)
			slug = strings.ReplaceAll(slug, "/", "-")
			slug = strings.ReplaceAll(slug, "=", "-")
			slug = strings.ReplaceAll(slug, " ", "-")
			slug = strings.ReplaceAll(slug, "(", "")
			slug = strings.ReplaceAll(slug, ")", "")
			slug = strings.ReplaceAll(slug, "[", "")
			slug = strings.ReplaceAll(slug, "]", "")
			slug = strings.ReplaceAll(slug, "+", "p")
			slug = strings.ReplaceAll(slug, "?", "q")
			slug = strings.ReplaceAll(slug, "*", "s")
			slug = strings.ReplaceAll(slug, "$", "e")
			slug = strings.ReplaceAll(slug, "^", "b")
			slug = strings.ReplaceAll(slug, "\\", "")
			slug = strings.ReplaceAll(slug, "|", "-alt-")
			r.TraceID = slug

			re, err := regexp.Compile(sbl.Pattern)
			if err != nil {
				return
			}

			r.Regime = re.Regime()

			// Generate trace metadata
			trace := struct {
				Category string  `json:"category"`
				Pattern  string  `json:"pattern"`
				Explain  string  `json:"explain"`
				S        float64 `json:"s"`
				B        float64 `json:"b"`
				L        float64 `json:"l"`
			}{
				Category: r.Category,
				Pattern:  sbl.Pattern,
				Explain:  re.Explain(),
				S:        r.S,
				B:        r.B,
				L:        r.L,
			}

			traceData, _ := json.MarshalIndent(trace, "", "  ")
			os.WriteFile(filepath.Join(traceDir, r.TraceID+".json"), traceData, 0644)
		}(i)
	}
	wg.Wait()

	output, _ := json.MarshalIndent(results, "", "  ")
	if err := os.WriteFile(outputFilePath, output, 0644); err != nil {
		fmt.Printf("Error writing output JSON: %v\n", err)
		os.Exit(1)
	}
}

func isEngine(s string) bool {
	return s == "GoRegexp" || s == "GoRegexpRe" || s == "Coregex" ||
		s == "Hyperscan-CGO" || s == "PCRE2-CGO" || s == "RE2-CGO" || s == "RE2-Wasm"
}

func parseBenchmarks(r io.Reader, registry BenchRegistry) []BenchResult {
	sums := make(map[Key]float64)
	counts := make(map[Key]int)

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "Benchmark") {
			continue
		}

		fields := strings.Fields(line)
		// Expected: BenchmarkName Iterations NsPerOp unit [Throughput unit] ...
		if len(fields) < 5 {
			continue
		}

		fullPath := fields[0]
		// Remove 'Benchmark' prefix
		fullPath = strings.TrimPrefix(fullPath, "Benchmark")

		var tp float64
		foundTP := false
		for i := 0; i < len(fields)-1; i++ {
			if fields[i+1] == "MB/s" {
				tp, _ = strconv.ParseFloat(fields[i], 64)
				foundTP = true
				break
			}
		}
		if !foundTP {
			continue
		}

		// Remove -N suffix
		if idx := strings.LastIndex(fullPath, "-"); idx != -1 {
			fullPath = fullPath[:idx]
		}

		parts := strings.Split(fullPath, "/")
		if len(parts) < 3 {
			continue
		}

		suite := parts[0]
		var engine, subName string

		// Identify engine and subName based on known engines
		p1 := parts[1]
		plast := parts[len(parts)-1]

		if isEngine(p1) {
			// Old format: Suite/Engine/SubName
			engine = p1
			subName = strings.Join(parts[2:], "/")
		} else if isEngine(plast) {
			// New format: Suite/SubName/Engine
			engine = plast
			subName = strings.Join(parts[1:len(parts)-1], "/")
		} else {
			// Fallback: assume new format
			engine = plast
			subName = strings.Join(parts[1:len(parts)-1], "/")
		}

		registryKey := suite + "/" + subName
		sbl, ok := registry[registryKey]
		if !ok {
			continue
		}

		// Include Category in the key to prevent incorrect aggregation across different patterns
		k := Key{Engine: engine, S: sbl.S, B: sbl.B, L: sbl.L, Category: registryKey}
		sums[k] += tp
		counts[k]++
	}

	var results []BenchResult
	for k, sum := range sums {
		results = append(results, BenchResult{
			Engine:     k.Engine,
			Category:   k.Category,
			S:          k.S,
			B:          k.B,
			L:          k.L,
			Throughput: sum / float64(counts[k]),
		})
	}

	// Sort results for consistent output
	sort.Slice(results, func(i, j int) bool {
		if results[i].Engine != results[j].Engine {
			return results[i].Engine < results[j].Engine
		}
		if results[i].Category != results[j].Category {
			return results[i].Category < results[j].Category
		}
		if results[i].S != results[j].S {
			return results[i].S > results[j].S
		}
		return results[i].B < results[j].B
	})

	return results
}
