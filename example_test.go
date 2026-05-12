package regexp_test

import (
	"fmt"
	"strings"

	"github.com/kamichidu/go-regexp-re"
)

func Example() {
	pattern := `a(b+)c`
	src := "abbc"
	repl := "X"

	// Package-level functions
	{
		matched, _ := regexp.Match(pattern, []byte(src))
		fmt.Printf("Match: %v\n", matched)

		matchedString, _ := regexp.MatchString(pattern, src)
		fmt.Printf("MatchString: %v\n", matchedString)

		matchedReader, _ := regexp.MatchReader(pattern, strings.NewReader(src))
		fmt.Printf("MatchReader: %v\n", matchedReader)

		re, _ := regexp.Compile(pattern)
		fmt.Printf("Compile: %v\n", re.String())

		reMust := regexp.MustCompile(pattern)
		fmt.Printf("MustCompile: %v\n", reMust.String())

		quoted := regexp.QuoteMeta(`[a-z]`)
		fmt.Printf("QuoteMeta: %s\n", quoted)
	}

	re := regexp.MustCompile(pattern)

	// Regexp methods
	{
		fmt.Printf("String: %s\n", re.String())
		fmt.Printf("NumSubexp: %d\n", re.NumSubexp())
		fmt.Printf("SubexpNames: %q\n", re.SubexpNames())
		fmt.Printf("SubexpIndex: %d\n", re.SubexpIndex("foo"))

		prefix, complete := re.LiteralPrefix()
		fmt.Printf("LiteralPrefix: %q, %v\n", prefix, complete)

		fmt.Printf("Match: %v\n", re.Match([]byte(src)))
		fmt.Printf("MatchString: %v\n", re.MatchString(src))
		fmt.Printf("MatchReader: %v\n", re.MatchReader(strings.NewReader(src)))

		fmt.Printf("Find: %q\n", re.Find([]byte(src)))
		fmt.Printf("FindIndex: %v\n", re.FindIndex([]byte(src)))
		fmt.Printf("FindString: %q\n", re.FindString(src))
		fmt.Printf("FindStringIndex: %v\n", re.FindStringIndex(src))
		fmt.Printf("FindReaderIndex: %v\n", re.FindReaderIndex(strings.NewReader(src)))

		fmt.Printf("FindSubmatch: %q\n", re.FindSubmatch([]byte(src)))
		fmt.Printf("FindSubmatchIndex: %v\n", re.FindSubmatchIndex([]byte(src)))
		fmt.Printf("FindStringSubmatch: %q\n", re.FindStringSubmatch(src))
		fmt.Printf("FindStringSubmatchIndex: %v\n", re.FindStringSubmatchIndex(src))
		fmt.Printf("FindReaderSubmatchIndex: %v\n", re.FindReaderSubmatchIndex(strings.NewReader(src)))

		fmt.Printf("FindAll: %q\n", re.FindAll([]byte(src), -1))
		fmt.Printf("FindAllIndex: %v\n", re.FindAllIndex([]byte(src), -1))
		fmt.Printf("FindAllString: %q\n", re.FindAllString(src, -1))
		fmt.Printf("FindAllStringIndex: %v\n", re.FindAllStringIndex(src, -1))

		fmt.Printf("FindAllSubmatch: %q\n", re.FindAllSubmatch([]byte(src), -1))
		fmt.Printf("FindAllSubmatchIndex: %v\n", re.FindAllSubmatchIndex([]byte(src), -1))
		fmt.Printf("FindAllStringSubmatch: %q\n", re.FindAllStringSubmatch(src, -1))
		fmt.Printf("FindAllStringSubmatchIndex: %v\n", re.FindAllStringSubmatchIndex(src, -1))

		fmt.Printf("ReplaceAll: %q\n", re.ReplaceAll([]byte(src), []byte(repl)))
		fmt.Printf("ReplaceAllString: %q\n", re.ReplaceAllString(src, repl))
		fmt.Printf("ReplaceAllLiteral: %q\n", re.ReplaceAllLiteral([]byte(src), []byte(repl)))
		fmt.Printf("ReplaceAllLiteralString: %q\n", re.ReplaceAllLiteralString(src, repl))
		fmt.Printf("ReplaceAllFunc: %q\n", re.ReplaceAllFunc([]byte(src), func(b []byte) []byte { return b }))
		fmt.Printf("ReplaceAllStringFunc: %q\n", re.ReplaceAllStringFunc(src, func(s string) string { return s }))

		fmt.Printf("Split: %q\n", re.Split(src, -1))

		marshaled, _ := re.MarshalText()
		fmt.Printf("MarshalText: %s\n", marshaled)

		var re2 regexp.Regexp
		_ = re2.UnmarshalText(marshaled)
		fmt.Printf("UnmarshalText: %s\n", re2.String())

		dst := []byte("Initial: ")
		template := "Captured: $1"
		match := re.FindSubmatchIndex([]byte(src))
		fmt.Printf("Expand: %q\n", re.Expand(dst, []byte(template), []byte(src), match))
		fmt.Printf("ExpandString: %q\n", re.ExpandString(dst, template, src, match))

		reCopy := re.Copy()
		fmt.Printf("Copy: %v\n", reCopy.String())
	}

	// Methods specifically known to be excluded from the "Core API Subset"
	// These will fail compilation if uncommented.
	{
		// re.Longest()
		// regexp.CompilePOSIX(pattern)
		// regexp.MustCompilePOSIX(pattern)
	}

	// Output:
	// Match: true
	// MatchString: true
	// MatchReader: true
	// Compile: a(b+)c
	// MustCompile: a(b+)c
	// QuoteMeta: \[a-z\]
	// String: a(b+)c
	// NumSubexp: 1
	// SubexpNames: ["" ""]
	// SubexpIndex: -1
	// LiteralPrefix: "a", false
	// Match: true
	// MatchString: true
	// MatchReader: true
	// Find: "abbc"
	// FindIndex: [0 4]
	// FindString: "abbc"
	// FindStringIndex: [0 4]
	// FindReaderIndex: [0 4]
	// FindSubmatch: ["abbc" "bb"]
	// FindSubmatchIndex: [0 4 1 3]
	// FindStringSubmatch: ["abbc" "bb"]
	// FindStringSubmatchIndex: [0 4 1 3]
	// FindReaderSubmatchIndex: [0 4 1 3]
	// FindAll: ["abbc"]
	// FindAllIndex: [[0 4]]
	// FindAllString: ["abbc"]
	// FindAllStringIndex: [[0 4]]
	// FindAllSubmatch: [["abbc" "bb"]]
	// FindAllSubmatchIndex: [[0 4 1 3]]
	// FindAllStringSubmatch: [["abbc" "bb"]]
	// FindAllStringSubmatchIndex: [[0 4 1 3]]
	// ReplaceAll: "X"
	// ReplaceAllString: "X"
	// ReplaceAllLiteral: "X"
	// ReplaceAllLiteralString: "X"
	// ReplaceAllFunc: "abbc"
	// ReplaceAllStringFunc: "abbc"
	// Split: ["" ""]
	// MarshalText: a(b+)c
	// UnmarshalText: a(b+)c
	// Expand: "Initial: Captured: bb"
	// ExpandString: "Initial: Captured: bb"
	// Copy: a(b+)c
}
