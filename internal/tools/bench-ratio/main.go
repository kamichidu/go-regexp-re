package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"text/tabwriter"
)

// Metric stores all supported benchmark metrics
type Metric struct {
	Ns     float64
	MBs    float64
	Bop    float64
	Allocs float64
}

// Full regex to capture all 4 metrics
var benchRe = regexp.MustCompile(`^Benchmark([^\s/]+)/(GoRegexp|GoRegexpRe)/([^\s-]+)(?:-\d+)?\s+\d+\s+([\d\.]+)\s+ns/op(?:\s+([\d\.]+)\s+MB/s)?(?:\s+([\d\.]+)\s+B/op)?(?:\s+([\d\.]+)\s+allocs/op)?`)

func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	// We use tabwriter for the columns, but values and units are joined with spaces
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)

	// groupName -> testName -> engineName -> []Metric
	data := make(map[string]map[string]map[string][]Metric)

	for scanner.Scan() {
		line := scanner.Text()
		matches := benchRe.FindStringSubmatch(line)
		if len(matches) >= 5 {
			groupName := matches[1]
			engine := matches[2]
			testName := matches[3]

			m := Metric{}
			m.Ns, _ = strconv.ParseFloat(matches[4], 64)
			if matches[5] != "" {
				m.MBs, _ = strconv.ParseFloat(matches[5], 64)
			}
			if matches[6] != "" {
				m.Bop, _ = strconv.ParseFloat(matches[6], 64)
			}
			if matches[7] != "" {
				m.Allocs, _ = strconv.ParseFloat(matches[7], 64)
			}

			if _, ok := data[groupName]; !ok {
				data[groupName] = make(map[string]map[string][]Metric)
			}
			if _, ok := data[groupName][testName]; !ok {
				data[groupName][testName] = make(map[string][]Metric)
			}
			data[groupName][testName][engine] = append(data[groupName][testName][engine], m)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading input: %w", err)
	}

	var groupNames []string
	for name := range data {
		groupNames = append(groupNames, name)
	}
	sort.Strings(groupNames)

	for _, groupName := range groupNames {
		testData := data[groupName]
		var testNames []string
		for name := range testData {
			testNames = append(testNames, name)
		}
		sort.Strings(testNames)

		for _, testName := range testNames {
			engines := testData[testName]
			stdResults := engines["GoRegexp"]
			reResults := engines["GoRegexpRe"]

			count := len(reResults)
			for i := 0; i < count; i++ {
				re := reResults[i]

				fmt.Fprintf(tw, "Benchmark%s/%s\t1", groupName, testName)

				// Time & Throughput: Ratio (Noise-resistant)
				if i < len(stdResults) {
					std := stdResults[i]
					fmt.Fprintf(tw, "\t%.6f ns/op", (re.Ns/std.Ns)*1000)
					if std.MBs > 0 && re.MBs > 0 {
						fmt.Fprintf(tw, "\t%.6f MB/s", (re.MBs/std.MBs)*100)
					}
				} else {
					fmt.Fprintf(tw, "\t0.000000 ns/op")
				}

				// Memory & Allocs: Absolute
				fmt.Fprintf(tw, "\t%.0f B/op", re.Bop)
				fmt.Fprintf(tw, "\t%.0f allocs/op", re.Allocs)
				fmt.Fprintln(tw)
			}
		}
	}

	return tw.Flush()
}
