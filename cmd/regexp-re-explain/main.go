package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/kamichidu/go-regexp-re"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: regexp-re-explain \"{pattern}\"\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if len(args) != 1 {
		flag.Usage()
		os.Exit(1)
	}

	pattern := args[0]
	re, err := regexp.Compile(pattern)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error compiling pattern: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(re.Explain())
}
