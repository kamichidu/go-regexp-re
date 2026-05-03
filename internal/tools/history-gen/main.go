package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type HistoryEntry struct {
	Date       string  `json:"date"`
	SHA        string  `json:"sha"`
	AvgSpeedup float64 `json:"avg_speedup"`
	File       string  `json:"file"`
}

type BenchResult struct {
	Engine     string  `json:"engine"`
	Throughput float64 `json:"throughput"`
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

		// Calculate avg speedup for this snapshot
		// Re vs Go
		var totalSpeedup float64
		var count int
		
		reResults := make(map[string]float64)
		goResults := make(map[string]float64)

		for _, r := range results {
			key := fmt.Sprintf("%.2f/%.2f/%.2f", r.S, r.B, r.L)
			if r.Engine == "GoRegexpRe" {
				reResults[key] = r.Throughput
			} else if r.Engine == "GoRegexp" {
				goResults[key] = r.Throughput
			}
		}

		for k, reTp := range reResults {
			if goTp, ok := goResults[k]; ok && goTp > 0 {
				totalSpeedup += reTp / goTp
				count++
			}
		}

		avg := 0.0
		if count > 0 {
			avg = totalSpeedup / float64(count)
		}

		// Extract date and SHA from filename: benchmark-20260424-000000-7ea0637.json
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
