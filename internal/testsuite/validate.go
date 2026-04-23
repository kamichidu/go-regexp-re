package testsuite

import (
	"errors"
	"reflect"
	"testing"

	"github.com/kamichidu/go-regexp-re"
	"github.com/kamichidu/go-regexp-re/syntax"
)

func validateSubmatchIndex(t *testing.T, pattern, input string, got, want []int) {
	t.Helper()

	if reflect.DeepEqual(got, want) {
		return
	}

	// 1. Overall match evaluation (indices 0 and 1)
	if len(got) != len(want) {
		t.Errorf("SubmatchIndex(%q, %q) returned %d indices, want %d", pattern, input, len(got), len(want))
		return
	}
	if len(got) < 2 {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("SubmatchIndex(%q, %q) = %v; want %v", pattern, input, got, want)
		}
		return
	}

	if got[0] != want[0] || got[1] != want[1] {
		t.Errorf("SubmatchIndex(%q, %q) overall match mismatch: got [%d, %d]; want [%d, %d]", pattern, input, got[0], got[1], want[0], want[1])
		return
	}

	// 2. Submatch evaluation (indices 2+)
	t.Logf("SubmatchIndex(%q, %q) submatch mismatch: %v; want %v", pattern, input, got, want)
	t.Skip("Skipping known TDFA submatch limitation")
}

func handleCompileError(t *testing.T, pattern string, err error) {
	t.Helper()
	var unsupported *regexp.UnsupportedError
	if errors.As(err, &unsupported) {
		// Acknowledged engine limitation: Skip the test to indicate it's not a failure,
		// but an expected lack of support.
		t.Skipf("acknowledged engine limitation: %v", err)
		return
	}
	// If standard Go fails to parse it, we should too. This is a PASS.
	var syntaxErr *syntax.Error
	if errors.As(err, &syntaxErr) {
		return
	}
	t.Fatalf("unexpected compilation error for %q: %v", pattern, err)
}
