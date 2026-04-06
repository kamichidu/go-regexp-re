package syntax_test

import (
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
