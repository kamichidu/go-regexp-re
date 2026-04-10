//go:build goregexpre

package benchmark

import (
	"github.com/kamichidu/go-regexp-re"
	"github.com/kamichidu/go-regexp-re/internal/testsuite"
)

func init() {
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
}
