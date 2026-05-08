package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
)

type BenchResult struct {
	Engine     string  `json:"engine"`
	Category   string  `json:"category,omitempty"`
	S          float64 `json:"s"`
	B          float64 `json:"b"`
	L          float64 `json:"l"`
	Throughput float64 `json:"throughput"` // MB/s
}

type Key struct {
	Engine   string
	Category string
	S        float64
	B        float64
	L        float64
}

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

	// Use the stable definitions file in the repo
	registryFile, err := os.ReadFile("internal/testsuite/sbl_definitions.json")
	if err != nil {
		fmt.Printf("Error opening SBL definitions: %v\n", err)
		os.Exit(1)
	}
	var registry map[string]struct {
		S float64 `json:"s"`
		B float64 `json:"b"`
		L float64 `json:"l"`
	}
	if err := json.Unmarshal(registryFile, &registry); err != nil {
		fmt.Printf("Error parsing SBL definitions: %v\n", err)
		os.Exit(1)
	}

	// Regex for standard benchmarks (Suite/Engine/SubName)
	reBench := regexp.MustCompile(`Benchmark(\w+)/(\w+)/(.+)-\d+\s+\d+\s+[\d.]+ ns/op\s+([\d.]+) MB/s`)

	sums := make(map[Key]float64)
	counts := make(map[Key]int)

	scanner := bufio.NewScanner(benchFile)
	for scanner.Scan() {
		line := scanner.Text()

		if m := reBench.FindStringSubmatch(line); len(m) == 5 {
			suite := m[1]
			engine := m[2]
			subName := m[3]
			tp, _ := strconv.ParseFloat(m[4], 64)

			registryKey := suite + "/" + subName
			sbl, ok := registry[registryKey]
			if !ok {
				continue
			}

			k := Key{Engine: engine, S: sbl.S, B: sbl.B, L: sbl.L, Category: registryKey}
			sums[k] += tp
			counts[k]++
		}
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
		if results[i].S != results[j].S {
			return results[i].S > results[j].S
		}
		return results[i].B < results[j].B
	})

	output, _ := json.MarshalIndent(results, "", "  ")
	if err := os.WriteFile(outputFilePath, output, 0644); err != nil {
		fmt.Printf("Error writing output JSON: %v\n", err)
		os.Exit(1)
	}
}
