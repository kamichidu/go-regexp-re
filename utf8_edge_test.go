package regexp

import (
	goregexp "regexp"
	"testing"
)

func TestUTF8EdgeCases(t *testing.T) {
	tests := []struct {
		pattern   string
		input     string
		want      bool
		expectErr bool
		name      string
	}{
		{
			pattern:   "あ",
			input:     "い",
			want:      false,
			expectErr: false,
			name:      "Lead-byte collision (same E3 prefix)",
		},
		{
			pattern:   "あ+",
			input:     "あいあ",
			want:      true,
			expectErr: false,
			name:      "Multi-byte repetition with collision",
		},
		{
			pattern:   "あ",
			input:     "\xe3\x81\x00",
			want:      false,
			expectErr: false,
			name:      "Invalid trailing bytes",
		},
		{
			pattern:   "^.あ.$",
			input:     "いあう",
			want:      true,
			expectErr: false,
			name:      "Dot between multi-byte (Junction_Dot)",
		},
		{
			pattern:   "([あいう])+",
			input:     "あいう",
			want:      true,
			expectErr: false,
			name:      "Multi-byte character class repetition",
		},
		{
			pattern:   "あ{2}",
			input:     "ああ",
			want:      true,
			expectErr: false,
			name:      "Multi-byte counted repetition",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			re, err := CompileWithOptions(tt.pattern, CompileOptions{
				forceStrategy: strategyFast,
				MaxMemory:     64 * 1024 * 1024,
			})
			if err != nil {
				if tt.expectErr {
					t.Skipf("Compilation failed as expected: %v", err)
					return
				}
				t.Fatalf("Compile(%q) failed: %v", tt.pattern, err)
			}
			if re.strategy != strategyFast && re.strategy != strategyExtended {
				t.Errorf("Strategy was not forced to DFA: got %v", re.strategy)
			}
			got := re.MatchString(tt.input)

			// Compare with standard library to highlight deviations
			stdRe := goregexp.MustCompile(tt.pattern)
			stdWant := stdRe.MatchString(tt.input)

			if got != tt.want {
				t.Errorf("Match(%q, %q) = %v, want %v (Known engine limitation)", tt.pattern, tt.input, got, tt.want)
			}

			if got != stdWant {
				t.Errorf("Standard library discrepancy for %q: Go std says %v, we say %v", tt.pattern, stdWant, got)
			}
		})
	}
}

func TestUTF8SubmatchEdgeCases(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
		name    string
	}{
		{
			pattern: "(あ)(い)(う)",
			input:   "あいう",
			name:    "Sequential multi-byte capture",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			re := MustCompile(tt.pattern)
			got := re.FindStringSubmatchIndex(tt.input)

			stdRe := goregexp.MustCompile(tt.pattern)
			want := stdRe.FindStringSubmatchIndex(tt.input)

			validateSubmatchIndex(t, tt.pattern, tt.input, got, want)
		})
	}

	knownBugSubmatchTests := []struct {
		pattern string
		input   string
		name    string
	}{
		{
			pattern: ".*(あ).*",
			input:   "いあう",
			name:    "Greedy skip with multi-byte capture",
		},
	}

	for _, tt := range knownBugSubmatchTests {
		t.Run("KnownBug/"+tt.name, func(t *testing.T) {
			re, err := Compile(tt.pattern)
			if err != nil {
				t.Fatalf("Compile(%q) failed: %v", tt.pattern, err)
			}
			got := re.FindStringSubmatchIndex(tt.input)
			stdRe := goregexp.MustCompile(tt.pattern)
			want := stdRe.FindSubmatchIndex([]byte(tt.input))

			validateSubmatchIndex(t, tt.pattern, tt.input, got, want)
		})
	}
}
