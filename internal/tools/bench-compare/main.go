package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Result struct {
	NsPerOp float64
	MBs     float64
}

type EngineData struct {
	Results []Result
}

func (e *EngineData) Avg() Result {
	if len(e.Results) == 0 {
		return Result{}
	}
	var sumNs, sumMBs float64
	for _, r := range e.Results {
		sumNs += r.NsPerOp
		sumMBs += r.MBs
	}
	return Result{
		NsPerOp: sumNs / float64(len(e.Results)),
		MBs:     sumMBs / float64(len(e.Results)),
	}
}

// Benchmark regex
// Groups: 1:Full Name, 2:NsPerOp, 3:MBs
var benchLineRe = regexp.MustCompile(`^Benchmark([^\s]+)\s+\d+\s+([\d\.]+)\s+ns/op(?:\s+([\d\.]+)\s+MB/s)?`)

func main() {
	var input io.Reader = os.Stdin
	if len(os.Args) > 1 {
		f, err := os.Open(os.Args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening file: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		input = f
	}

	if err := run(input); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func escapeMarkdown(s string) string {
	return strings.ReplaceAll(s, "|", "\\|")
}

func run(r io.Reader) error {
	scanner := bufio.NewScanner(r)

	// testKey -> engine -> EngineData
	data := make(map[string]map[string]*EngineData)
	var testKeys []string
	var engines []string
	engineSeen := make(map[string]bool)

	for scanner.Scan() {
		line := scanner.Text()
		m := benchLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		fullName := m[1]
		ns, _ := strconv.ParseFloat(m[2], 64)
		var mbs float64
		if m[3] != "" {
			mbs, _ = strconv.ParseFloat(m[3], 64)
		}

		// Identify engine and testKey from fullName like "StandardSuite/Engine/TestCase"
		// or "Landscape/Engine#01/TestCase"
		parts := strings.Split(fullName, "/")
		if len(parts) < 3 {
			continue
		}

		// Engine name is in parts[1], but might have #NN suffix
		engine := parts[1]
		engine = regexp.MustCompile(`#\d+$`).ReplaceAllString(engine, "")

		// testKey is parts[0] + rest
		testKey := parts[0] + "/" + strings.Join(parts[2:], "/")

		// Strip GOMAXPROCS suffix from testKey if it exists at the very end
		testKey = regexp.MustCompile(`-\d+$`).ReplaceAllString(testKey, "")

		if !engineSeen[engine] {
			engineSeen[engine] = true
			engines = append(engines, engine)
		}

		if data[testKey] == nil {
			data[testKey] = make(map[string]*EngineData)
			testKeys = append(testKeys, testKey)
		}
		if data[testKey][engine] == nil {
			data[testKey][engine] = &EngineData{}
		}
		data[testKey][engine].Results = append(data[testKey][engine].Results, Result{NsPerOp: ns, MBs: mbs})
	}

	sort.Strings(testKeys)
	// Sort engines for consistent columns: GoRegexp and GoRegexpRe first, then others alphabetically
	sort.Slice(engines, func(i, j int) bool {
		priority := func(name string) int {
			switch name {
			case "GoRegexp":
				return 0
			case "GoRegexpRe":
				return 1
			default:
				return 2
			}
		}
		pi, pj := priority(engines[i]), priority(engines[j])
		if pi != pj {
			return pi < pj
		}
		return engines[i] < engines[j]
	})

	// 1. Output Table
	fmt.Println("## Benchmark Comparison (Average ns/op)")
	fmt.Print("| Test Case |")
	for _, e := range engines {
		fmt.Printf(" %s |", e)
	}
	fmt.Println()
	fmt.Print("|---|")
	for range engines {
		fmt.Print("---|")
	}
	fmt.Println()

	for _, tk := range testKeys {
		fmt.Printf("| %s |", escapeMarkdown(tk))
		for _, e := range engines {
			if ed, ok := data[tk][e]; ok {
				avg := ed.Avg()
				fmt.Printf(" %.2f |", avg.NsPerOp)
			} else {
				fmt.Print(" N/A |")
			}
		}
		fmt.Println()
	}

	fmt.Println("\n## Throughput Comparison (Average MB/s)")
	fmt.Print("| Test Case |")
	for _, e := range engines {
		fmt.Printf(" %s |", e)
	}
	fmt.Println()
	fmt.Print("|---|")
	for range engines {
		fmt.Print("---|")
	}
	fmt.Println()

	for _, tk := range testKeys {
		fmt.Printf("| %s |", escapeMarkdown(tk))
		for _, e := range engines {
			if ed, ok := data[tk][e]; ok {
				avg := ed.Avg()
				if avg.MBs > 0 {
					fmt.Printf(" %.2f |", avg.MBs)
				} else {
					fmt.Print(" - |")
				}
			} else {
				fmt.Print(" N/A |")
			}
		}
		fmt.Println()
	}

	// 2. Output Mermaid Graphs
	// One graph per scenario, engines on the x-axis
	fmt.Println("\n## Performance Graphs (MB/s, higher is better)")

	for _, tk := range testKeys {
		var activeEngines []string
		var values []string
		hasThroughput := false

		for _, e := range engines {
			if ed, ok := data[tk][e]; ok {
				avg := ed.Avg()
				if avg.MBs > 0 {
					activeEngines = append(activeEngines, "\""+e+"\"")
					values = append(values, fmt.Sprintf("%.2f", avg.MBs))
					hasThroughput = true
				}
			}
		}

		if !hasThroughput {
			continue
		}

		fmt.Printf("\n### %s\n", tk)
		fmt.Println("```mermaid")
		fmt.Println("xychart-beta")
		fmt.Printf("    title \"%s (MB/s)\"\n", tk)
		fmt.Printf("    x-axis [%s]\n", strings.Join(activeEngines, ", "))
		fmt.Println("    y-axis \"MB/s\"")
		fmt.Printf("    bar [%s]\n", strings.Join(values, ", "))
		fmt.Println("```")
	}

	return nil
}
