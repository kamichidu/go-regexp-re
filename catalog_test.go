//go:build catalog

package regexp

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

func TestCatalog(t *testing.T) {
	f, err := os.Open("testdata/regexp-patterns.dat")
	if err != nil {
		t.Fatalf("failed to open catalog: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var okCompiled, okFailed, errCaught, errMissed, memCaught, memMissed int
	var failedPatterns []string

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) < 2 {
			continue
		}
		marker := parts[0]
		pattern := parts[1]

		_, err := Compile(pattern)

		switch marker {
		case "OK":
			if err != nil {
				okFailed++
				failedPatterns = append(failedPatterns, "FAILED OK: "+pattern+" (Error: "+err.Error()+")")
			} else {
				okCompiled++
			}
		case "ERR":
			if err == nil {
				errMissed++
				failedPatterns = append(failedPatterns, "MISSED ERR: "+pattern+" (Compiled but expected error)")
			} else {
				errCaught++
			}
		case "MEM":
			if err != nil && (strings.Contains(err.Error(), "pattern too large") || strings.Contains(err.Error(), "state explosion")) {
				memCaught++
			} else if err == nil {
				memMissed++
				failedPatterns = append(failedPatterns, "MISSED MEM: "+pattern+" (Compiled but expected memory limit error)")
			} else {
				memMissed++
				failedPatterns = append(failedPatterns, "MISSED MEM: "+pattern+" (Expected memory limit error, but got: "+err.Error()+")")
			}
		}
	}

	t.Logf("--- Catalog GRASP Result ---")
	t.Logf("OK Patterns:  %d compiled, %d failed", okCompiled, okFailed)
	t.Logf("ERR Patterns: %d caught, %d missed", errCaught, errMissed)
	t.Logf("MEM Patterns: %d caught, %d missed", memCaught, memMissed)
	t.Logf("Total: %d patterns processed", okCompiled+okFailed+errCaught+errMissed+memCaught+memMissed)

	if len(failedPatterns) > 0 {
		t.Errorf("Catalog mismatch found:\n%s", strings.Join(failedPatterns, "\n"))
	}
}
