package compat

import (
	"testing"

	"github.com/kamichidu/go-regexp-re/internal/testsuite"
)

func TestCompatibility(t *testing.T) {
	testsuite.RunCompatibility(t)
}

func TestSubmatchCompatibility(t *testing.T) {
	testsuite.RunSubmatchCompatibility(t)
}
