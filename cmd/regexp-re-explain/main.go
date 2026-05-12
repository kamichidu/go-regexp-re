package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/kamichidu/go-regexp-re"
	"github.com/kamichidu/go-regexp-re/internal/ir"
)

type memoryFlag int

func (m *memoryFlag) Set(s string) error {
	s = strings.ToLower(s)
	multiplier := 1
	if strings.HasSuffix(s, "k") {
		multiplier = 1024
		s = strings.TrimSuffix(s, "k")
	} else if strings.HasSuffix(s, "m") {
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(s, "m")
	} else if strings.HasSuffix(s, "g") {
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "g")
	}

	val, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("invalid memory format: %s", s)
	}

	*m = memoryFlag(val * multiplier)
	return nil
}

func (m *memoryFlag) String() string {
	return strconv.Itoa(int(*m))
}

func main() {
	var maxMemory memoryFlag = memoryFlag(ir.MaxDFAMemory)
	flag.Var(&maxMemory, "m", "Max memory limit for DFA construction (e.g., 64m, 1g)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: regexp-re-explain [-m memory_limit] \"{pattern}\"\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if len(args) != 1 {
		flag.Usage()
		os.Exit(1)
	}

	pattern := args[0]
	re, err := regexp.CompileWithOptions(pattern, regexp.CompileOptions{
		MaxMemory: int(maxMemory),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error compiling pattern: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(re.Explain())
}
