package regexp

import (
	"fmt"
	"testing"
	goregexp "regexp"
	"reflect"
)

func TestUserRepro(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
	}{
		{`a*(|(b))c*`, "aacc"},
		{`a*?`, "aaaa"},
	}

	for _, tt := range tests {
		re, err := Compile(tt.pattern)
		if err != nil {
			t.Errorf("Compile(%q) failed: %v", tt.pattern, err)
			continue
		}
		got := re.FindStringSubmatchIndex(tt.input)
		want := goregexp.MustCompile(tt.pattern).FindStringSubmatchIndex(tt.input)
		fmt.Printf("Pattern: %s, Input: %s, Got: %v, Want: %v\n", tt.pattern, tt.input, got, want)
		
		if !reflect.DeepEqual(got, want) {
			t.Errorf("FindStringSubmatchIndex(%q, %q) = %v, want %v", tt.pattern, tt.input, got, want)
		}
	}
}
