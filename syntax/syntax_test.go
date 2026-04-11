package syntax

import (
	"testing"
)

func TestOptimize(t *testing.T) {
	tests := []struct {
		pattern string
		want    string
	}{
		{
			pattern: "apple|apply",
			want:    "appl[ey]",
		},
		{
			pattern: "bananas|apples",
			want:    "(?:bananas|apple)(?:)",
		},
		{
			pattern: "a(b|c)d|a(b|c)e",
			want:    "a(?:([bc])d|([bc])e)",
		},
		{
			pattern: "abc|abd",
			want:    "ab[cd]",
		},
		{
			pattern: "abc|def|ghi",
			want:    "(?:abc|de|gh)(?:)",
		},
		{
			pattern: "test|testing",
			want:    "test(?:(?:)|in)(?:)",
		},
		{
			pattern: "abcde|fbcde",
			want:    "(?:abcde|fbcd)(?:)",
		},
	}

	for _, tt := range tests {
		re, err := Parse(tt.pattern, Perl)
		if err != nil {
			t.Errorf("Parse(%q) failed: %v", tt.pattern, err)
			continue
		}
		re = Simplify(re)
		re = Optimize(re)
		got := re.String()
		if got != tt.want {
			t.Errorf("Optimize(%q) = %q, want %q", tt.pattern, got, tt.want)
		}
	}
}
