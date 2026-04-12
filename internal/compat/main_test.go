package compat

import (
	"strings"
	"testing"

	"github.com/kamichidu/go-regexp-re"
	"github.com/kamichidu/go-regexp-re/internal/testsuite"
	goregexp "regexp"
)

func TestMain(m *testing.M) {
	// Register Go Standard Regexp
	testsuite.Register(testsuite.Engine{
		Name: "GoRegexp",
		Compile: func(pattern string) (testsuite.Matcher, error) {
			re, err := goregexp.Compile(pattern)
			if err != nil {
				return nil, err
			}
			return re, nil
		},
	})

	// Register go-regexp-re
	testsuite.Register(testsuite.Engine{
		Name: "GoRegexpRe",
		Compile: func(pattern string) (testsuite.Matcher, error) {
			limit := 64 * 1024 * 1024
			// If the pattern is very large (likely a large alternation), increase limit
			if len(pattern) > 1000 || strings.Count(pattern, "|") > 100 {
				limit = 512 * 1024 * 1024
			}
			re, err := regexp.CompileWithOption(pattern, regexp.CompileOption{MaxMemory: limit})
			if err != nil {
				return nil, err
			}
			return re, nil
		},
	})

	testsuite.EnableCompatibilityReport = true
	testsuite.Main(m)
}
