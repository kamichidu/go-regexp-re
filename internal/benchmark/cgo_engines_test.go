//go:build cgo_engines

package benchmark

import (
	"errors"

	"github.com/Jemmic/go-pcre2"
	"github.com/flier/gohs/hyperscan"
	"github.com/kamichidu/go-regexp-re/internal/testsuite"
)

func init() {
	// Register PCRE2 (CGO)
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

	// Register Hyperscan (CGO)
	testsuite.Register(testsuite.Engine{
		Name: "Hyperscan-CGO",
		Compile: func(pattern string) (testsuite.Matcher, error) {
			p := hyperscan.NewPattern(pattern, 0)
			db, err := hyperscan.NewBlockDatabase(p)
			if err != nil {
				return nil, err
			}
			scratch, err := hyperscan.NewScratch(db)
			if err != nil {
				return nil, err
			}
			return &hyperscanMatcher{db: db, scratch: scratch, pattern: pattern}, nil
		},
	})
}

type pcre2Matcher struct {
	re      *pcre2.Regexp
	pattern string
}

func (m *pcre2Matcher) MatchString(s string) bool {
	matcher := m.re.MatcherString(s, 0)
	return matcher.MatchString(s, 0)
}

func (m *pcre2Matcher) FindStringSubmatchIndex(s string) []int {
	return nil
}

func (m *pcre2Matcher) String() string { return m.pattern }

type hyperscanMatcher struct {
	db      hyperscan.BlockDatabase
	scratch *hyperscan.Scratch
	pattern string
}

func (m *hyperscanMatcher) MatchString(s string) bool {
	matched := false
	stopErr := errors.New("stop")
	_ = m.db.Scan([]byte(s), m.scratch, func(id uint, from, to uint64, flags uint, context interface{}) error {
		matched = true
		return stopErr
	}, nil)
	return matched
}

func (m *hyperscanMatcher) FindStringSubmatchIndex(s string) []int { return nil }
func (m *hyperscanMatcher) String() string                         { return m.pattern }
