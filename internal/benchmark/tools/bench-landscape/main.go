package main

import (
	"flag"
	"fmt"
	_ "github.com/kamichidu/go-regexp-re/internal/benchmark" // Register engines
	"github.com/kamichidu/go-regexp-re/internal/benchmark/landscape"
	"github.com/kamichidu/go-regexp-re/internal/testsuite"
	"log"
	"os"
)

func main() {
	output := flag.String("o", "landscape.json", "Output file")
	flag.Parse()

	// Use representative patterns
	patterns := []string{
		"abc",
		"a|b|c",
		"a*b+",
		"[a-z]+",
		"(a|b)*c",
		"Sherlock",
		"^127.0.0.1",
		"([a-z]+)@([a-z]+).com",
	}

	// We need to make sure engines are registered.
	// In internal/benchmark/engine_*.go, they are registered via init().
	// But testsuite.Register uses a global slice.

	runner := &landscape.Runner{
		Engines:  testsuite.GetEngines(),
		Patterns: patterns,
	}

	fmt.Println("Running landscape benchmarks...")
	l := runner.Run()

	f, err := os.Create(*output)
	if err != nil {
		log.Fatalf("failed to create output file: %v", err)
	}
	defer f.Close()

	if err := l.ExportJSON(f); err != nil {
		log.Fatalf("failed to export JSON: %v", err)
	}

	fmt.Printf("Landscape data exported to %s\n", *output)

	// Also export a grid for visualization
	grid := l.BuildGrid(10)
	fmt.Printf("Generated %d grid points for visualization.\n", len(grid))
}
