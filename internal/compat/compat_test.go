package compat

import (
	"github.com/kamichidu/go-regexp-re/internal/testsuite"
	"testing"
)

func TestCompatibility(t *testing.T) {
	testsuite.RunCompatibility(t)
}
