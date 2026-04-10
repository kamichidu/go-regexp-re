//go:build pcre2cgo

package benchmark

import (
	"github.com/Jemmic/go-pcre2"
	"github.com/kamichidu/go-regexp-re/internal/testsuite"
)

func init() {
	testsuite.Register(testsuite.Engine{
		Name: "PCRE2-CGO",
		Compile: func(pattern string) (testsuite.Matcher, error) {
			re, err := pcre2.Compile(pattern, 0)
			if err != nil {
				return nil, err
			}
			return &pcre2Matcher{re: re, pattern: pattern}, nil
		},
	})
}

type pcre2Matcher struct {
	re      *pcre2.Regexp
	pattern string
}

func (m *pcre2Matcher) MatchString(s string) bool {
	matcher := m.re.MatcherString(s, 0)
	return matcher.Matches()
}

func (m *pcre2Matcher) FindStringSubmatchIndex(s string) []int {
	matcher := m.re.MatcherString(s, 0)
	if !matcher.Matches() {
		return nil
	}
	n := matcher.Groups()
	res := make([]int, 0, (n+1)*2)
	for i := 0; i <= n; i++ {
		indices := matcher.GroupIndices(i)
		if indices == nil {
			res = append(res, -1, -1)
		} else {
			res = append(res, indices[0], indices[1])
		}
	}
	return res
}

func (m *pcre2Matcher) String() string { return m.pattern }
