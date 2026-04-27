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

var engines = []string{
	"GoRegexp",
	"GoRegexpRe",
	"Hyperscan-CGO",
	"PCRE2-CGO",
	"RE2-CGO",
}

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

func run(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	
	// testKey -> engine -> EngineData
	data := make(map[string]map[string]*EngineData)
	var testKeys []string

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

		// Identify engine and testKey
		var engine, testKey string
		foundEngine := false
		for _, e := range engines {
			// Look for /Engine/ or /Engine-GOMAXPROCS
			if strings.Contains(fullName, "/"+e+"/") {
				engine = e
				testKey = strings.Replace(fullName, "/"+e+"/", "/", 1)
				foundEngine = true
				break
			}
			if strings.HasSuffix(fullName, "/"+e) || strings.Contains(fullName, "/"+e+"-") {
				engine = e
				// Remove engine and anything after it (like -2)
				re := regexp.MustCompile("/"+regexp.QuoteMeta(e)+"(?:-\\d+)?$")
				testKey = re.ReplaceAllString(fullName, "")
				foundEngine = true
				break
			}
		}

		if !foundEngine {
			continue
		}

		// Strip GOMAXPROCS suffix from testKey if it exists at the very end
		testKey = regexp.MustCompile(`-\d+$`).ReplaceAllString(testKey, "")

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
		fmt.Printf("| %s |", tk)
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
		fmt.Printf("| %s |", tk)
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
