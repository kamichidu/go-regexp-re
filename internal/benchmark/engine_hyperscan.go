//go:build hyperscan

package benchmark

import (
	"errors"

	"github.com/flier/gohs/hyperscan"
	"github.com/kamichidu/go-regexp-re/internal/testsuite"
)

func init() {
	testsuite.Register(testsuite.Engine{
		Name: "Hyperscan-CGO",
		Compile: func(pattern string) (testsuite.Matcher, error) {
			p := hyperscan.NewPattern(pattern, hyperscan.SomLeftMost)
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

func (m *hyperscanMatcher) FindStringSubmatchIndex(s string) []int {
	var res []int
	stopErr := errors.New("stop")
	_ = m.db.Scan([]byte(s), m.scratch, func(id uint, from, to uint64, flags uint, context interface{}) error {
		res = []int{int(from), int(to)}
		return stopErr
	}, nil)
	return res
}

func (m *hyperscanMatcher) String() string {
	return m.pattern
}
