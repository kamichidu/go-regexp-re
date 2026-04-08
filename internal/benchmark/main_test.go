package benchmark

import (
	goregexp "regexp"
	"testing"

	"github.com/kamichidu/go-regexp-re"
	"github.com/kamichidu/go-regexp-re/internal/testsuite"
	"github.com/wasilibs/go-re2"
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

	// Register RE2 (Wasm)
	testsuite.Register(testsuite.Engine{
		Name: "RE2-Wasm",
		Compile: func(pattern string) (testsuite.Matcher, error) {
			re, err := re2.Compile(pattern)
			if err != nil {
				return nil, err
			}
			return re, nil
		},
	})

	// Register RE2 (CGO)
	// requires -tags re2_cgo to use CGO version, otherwise this is same as Wasm version.
	testsuite.Register(testsuite.Engine{
		Name: "RE2-CGO",
		Compile: func(pattern string) (testsuite.Matcher, error) {
			re, err := re2.Compile(pattern)
			if err != nil {
				return nil, err
			}
			return re, nil
		},
	})

	testsuite.Main(m)
}
