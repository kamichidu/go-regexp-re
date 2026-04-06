//go:build re2_search

package compat

import (
	"github.com/kamichidu/go-regexp-re/internal/testsuite"
	"testing"
)

func TestRE2Search(t *testing.T) {
	testsuite.RunRE2Search(t)
}
