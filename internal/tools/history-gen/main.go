package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type HistoryEntry struct {
	Date       string  `json:"date"`
	SHA        string  `json:"sha"`
	AvgSpeedup float64 `json:"avg_speedup"` // Geometric Mean
	MinSpeedup float64 `json:"min_speedup"`
	MaxSpeedup float64 `json:"max_speedup"`
	File       string  `json:"file"`
}

type BenchResult struct {
	Engine     string  `json:"engine"`
	Throughput float64 `json:"throughput"`
	S          float64 `json:"s"`
	B          float64 `json:"b"`
	L          float64 `json:"l"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run main.go <history_dir> <output_history.json>")
		os.Exit(1)
	}

	historyDir := os.Args[1]
	outputFile := os.Args[2]

	files, _ := filepath.Glob(filepath.Join(historyDir, "*.json"))
	var history []HistoryEntry

	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}

		var results []struct {
			Engine     string  `json:"engine"`
			Throughput float64 `json:"throughput"`
			S          float64 `json:"s"`
			B          float64 `json:"b"`
			L          float64 `json:"l"`
		}
		if err := json.Unmarshal(data, &results); err != nil {
			continue
		}

		// Calculate metrics for this snapshot
		var logSum float64
		var minSpeedup float64 = 1e18
		var maxSpeedup float64 = 0
		var count int

		// Group results by engine to facilitate pairing
		engineMap := make(map[string][]struct {
			Engine     string  `json:"engine"`
			Throughput float64 `json:"throughput"`
			S          float64 `json:"s"`
			B          float64 `json:"b"`
			L          float64 `json:"l"`
		})
		for _, r := range results {
			engineMap[r.Engine] = append(engineMap[r.Engine], r)
		}

		ourResults := engineMap["GoRegexpRe"]
		stdResults := engineMap["GoRegexp"]

		for _, re := range ourResults {
			// Find matching standard result by closest SBL
			var stdTp float64 = -1
			for _, std := range stdResults {
				if math.Abs(std.S-re.S) < 0.01 && math.Abs(std.B-re.B) < 0.01 && math.Abs(std.L-re.L) < 0.01 {
					stdTp = std.Throughput
					break
				}
			}

			if stdTp > 0 {
				speedup := re.Throughput / stdTp

				logSum += math.Log(speedup)
				if speedup < minSpeedup {
					minSpeedup = speedup
				}
				if speedup > maxSpeedup {
					maxSpeedup = speedup
				}
				count++
			}
		}

		avg := 0.0
		if count > 0 {
			avg = math.Exp(logSum / float64(count))
		} else {
			minSpeedup = 0
		}
		// Extract date and SHA from filename
		base := filepath.Base(file)
		parts := strings.Split(strings.TrimSuffix(base, ".json"), "-")
		dateStr := ""
		sha := ""
		if len(parts) >= 4 {
			// parts[1] is 20260424, parts[2] is 170513
			d := parts[1]
			t := parts[2]
			if len(d) == 8 && len(t) == 6 {
				dateStr = fmt.Sprintf("%s-%s-%s %s:%s:%s", d[:4], d[4:6], d[6:8], t[:2], t[2:4], t[4:6])
			} else {
				dateStr = parts[1] + " " + parts[2]
			}
			sha = parts[3]
		}

		history = append(history, HistoryEntry{
			Date:       dateStr,
			SHA:        sha,
			AvgSpeedup: avg,
			MinSpeedup: minSpeedup,
			MaxSpeedup: maxSpeedup,
			File:       base,
		})
	}

	sort.Slice(history, func(i, j int) bool {
		return history[i].Date < history[j].Date
	})

	output, _ := json.MarshalIndent(history, "", "  ")
	os.WriteFile(outputFile, output, 0644)
	fmt.Printf("Generated %s with %d entries\n", outputFile, len(history))
}
