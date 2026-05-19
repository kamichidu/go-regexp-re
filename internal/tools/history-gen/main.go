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

type BenchResult struct {
	Engine     string  `json:"engine"`
	Category   string  `json:"category"`
	Throughput float64 `json:"throughput"`
	S          float64 `json:"s"`
	B          float64 `json:"b"`
	L          float64 `json:"l"`
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

		var results []BenchResult
		if err := json.Unmarshal(data, &results); err != nil {
			continue
		}

		avg, min, max, ok := calculateMetrics(results)
		if ok {
			history = append(history, HistoryEntry{
				Date:       dateStr,
				SHA:        sha,
				AvgSpeedup: avg,
				MinSpeedup: min,
				MaxSpeedup: max,
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

func calculateMetrics(results []BenchResult) (avg, min, max float64, ok bool) {
	var logSum float64
	min = 1e18
	max = 0
	var count int

	// Group results by engine to facilitate pairing
	engineMap := make(map[string][]BenchResult)
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
			if speedup < min {
				min = speedup
			}
			if speedup > max {
				max = speedup
			}
			count++
		}
	}

	if count > 0 {
		avg = math.Exp(logSum / float64(count))
		return avg, min, max, true
	}
	return 0, 0, 0, false
}
