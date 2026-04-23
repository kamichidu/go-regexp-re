package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/kamichidu/go-regexp-re/internal/ir"
	"github.com/kamichidu/go-regexp-re/syntax"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: regexp-dump \"{pattern}\"\n")
		fmt.Fprintf(os.Stderr, "\nAvailable options:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if len(args) != 1 {
		flag.Usage()
		os.Exit(1)
	}

	pattern := args[0]

	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing pattern: %v\n", err)
		os.Exit(1)
	}

	re = syntax.Simplify(re)
	prog, err := syntax.Compile(re)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error compiling pattern: %v\n", err)
		os.Exit(1)
	}

	dfa, err := ir.NewDFAWithMemoryLimit(context.Background(), re, prog, 64*1024*1024, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building DFA: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(ir.ToDOT(dfa))
}
