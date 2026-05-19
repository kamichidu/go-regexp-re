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
	AvgSpeedup float64 `json:"avg_speedup"`
	MinSpeedup float64 `json:"min_speedup"`
	MaxSpeedup float64 `json:"max_speedup"`
	File       string  `json:"file"`
}

func main() {
	if len(os.Args) < 3 {
		fmt.Println("Usage: go run main.go <history_dir> <output_summary.json>")
		os.Exit(1)
	}

	historyDir := os.Args[1]
	outputFile := os.Args[2]

	files, err := os.ReadDir(historyDir)
	if err != nil {
		fmt.Printf("Error reading history dir: %v\n", err)
		os.Exit(1)
	}

	var history []HistoryEntry

	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".json") || f.Name() == "history.json" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(historyDir, f.Name()))
		if err != nil {
			continue
		}

		// benchmark-YYYYMMDD-HHMMSS-SHA.json
		parts := strings.Split(strings.TrimSuffix(f.Name(), ".json"), "-")
		if len(parts) < 4 {
			continue
		}
		dateStr := fmt.Sprintf("%s %s", parts[1][:4]+"-"+parts[1][4:6]+"-"+parts[1][6:], parts[2][:2]+":"+parts[2][2:4]+":"+parts[2][4:])
		sha := parts[3]

		var results []struct {
			Engine     string  `json:"engine"`
			Category   string  `json:"category"`
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
			Category   string  `json:"category"`
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
			// Find matching standard result by category, then by closest SBL
			var stdTp float64 = -1
			for _, std := range stdResults {
				if re.Category != "" && std.Category != "" {
					if re.Category == std.Category {
						stdTp = std.Throughput
						break
					}
				} else if math.Abs(std.S-re.S) < 0.01 && math.Abs(std.B-re.B) < 0.01 && math.Abs(std.L-re.L) < 0.01 {
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

		if count > 0 {
			avg := math.Exp(logSum / float64(count))
			history = append(history, HistoryEntry{
				Date:       dateStr,
				SHA:        sha,
				AvgSpeedup: avg,
				MinSpeedup: minSpeedup,
				MaxSpeedup: maxSpeedup,
				File:       f.Name(),
			})
		}
	}

	sort.Slice(history, func(i, j int) bool {
		return history[i].Date < history[j].Date
	})

	output, _ := json.MarshalIndent(history, "", "  ")
	os.WriteFile(outputFile, output, 0644)
	fmt.Printf("Generated %s with %d entries\n", outputFile, len(history))
}
