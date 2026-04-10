//go:build goregexp

package benchmark

import (
	goregexp "regexp"

	"github.com/kamichidu/go-regexp-re/internal/testsuite"
)

func init() {
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
}
