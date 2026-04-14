//go:build goregexpre

package benchmark

import (
	"strings"

	"github.com/kamichidu/go-regexp-re"
	"github.com/kamichidu/go-regexp-re/internal/testsuite"
)

func init() {
	testsuite.Register(testsuite.Engine{
		Name: "GoRegexpRe",
		Compile: func(pattern string) (testsuite.Matcher, error) {
			limit := 64 * 1024 * 1024
			// Dynamic memory allocation for Large Alternation patterns
			if strings.Count(pattern, "|") > 100 {
				limit = 1024 * 1024 * 1024 // 1GB limit
			}
			re, err := regexp.CompileWithOptions(pattern, regexp.CompileOptions{MaxMemory: limit})
			if err != nil {
				return nil, err
			}
			return re, nil
		},
	})
}
