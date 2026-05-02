//go:build coregex

package benchmark

import (
	"github.com/coregx/coregex"
	"github.com/kamichidu/go-regexp-re/internal/testsuite"
)

func init() {
	testsuite.Register(testsuite.Engine{
		Name: "Coregex",
		Compile: func(pattern string) (testsuite.Matcher, error) {
			re, err := coregex.Compile(pattern)
			if err != nil {
				return nil, err
			}
			return re, nil
		},
	})
}
