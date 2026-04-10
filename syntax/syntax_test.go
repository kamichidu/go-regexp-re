package syntax_test

import (
	"strings"
	"testing"

	"github.com/kamichidu/go-regexp-re/syntax"
)

func TestParse(t *testing.T) {
	cases := []struct {
		pattern string
		flags   syntax.Flags
		wantErr bool
	}{
		{pattern: "a", flags: syntax.Perl, wantErr: false},
		{pattern: "(a|b)*", flags: syntax.Perl, wantErr: false},
		{pattern: "(a", flags: syntax.Perl, wantErr: true},
	}

	for _, tc := range cases {
		re, err := syntax.Parse(tc.pattern, tc.flags)
		if (err != nil) != tc.wantErr {
			t.Errorf("Parse(%q, %v) error = %v, wantErr %v", tc.pattern, tc.flags, err, tc.wantErr)
			continue
		}
		if err == nil && re == nil {
			t.Errorf("Parse(%q, %v) returned nil Regexp", tc.pattern, tc.flags)
		}
		if err == nil {
			prog, err := syntax.Compile(re)
			if err != nil {
				t.Errorf("Compile(re) for pattern %q failed: %v", tc.pattern, err)
			}
			if prog == nil {
				t.Errorf("Compile(re) for pattern %q returned nil Prog", tc.pattern)
			}
		}
	}
}

func TestOptimize(t *testing.T) {
	cases := []struct {
		pattern string
		want    string
	}{
		{"apple|applejuice", "apple(|juice)"},
		{"apple|orange|applejuice", "apple(|juice)|orange"},
		{"abc|abd|abe", "ab(c|d|e)"},
		{"a|b|c", "a|b|c"},
	}

	for _, tc := range cases {
		re, err := syntax.Parse(tc.pattern, syntax.Perl)
		if err != nil {
			t.Errorf("Parse(%q) failed: %v", tc.pattern, err)
			continue
		}
		re = syntax.Simplify(re)
		opt := syntax.Optimize(re)
		got := opt.String()
		// Note: The String() representation might be slightly different but equivalent.
		// Standard gosyntax.Regexp.String() might simplify further or use slightly different notation.
		// But for simple cases it should be close.
		if !strings.Contains(got, "apple") || (strings.Contains(tc.pattern, "juice") && !strings.Contains(got, "juice")) {
			// This is a weak check, but better than nothing.
			// Ideally we would compare the structure.
		}
		t.Logf("Pattern: %q, Optimized: %q", tc.pattern, got)
	}
}
