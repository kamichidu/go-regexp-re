package compat

import (
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
			re, err := regexp.Compile(pattern)
			if err != nil {
				return nil, err
			}
			return re, nil
		},
	})

	testsuite.Main(m)
}
