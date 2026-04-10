//go:build re2cgo

package benchmark

import (
	"github.com/kamichidu/go-regexp-re/internal/testsuite"
	"github.com/wasilibs/go-re2"
)

func init() {
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
}
