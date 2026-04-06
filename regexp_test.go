package regexp

import (
	"testing"
)

func TestCompile(t *testing.T) {
	cases := []struct {
		expr    string
		wantErr bool
	}{
		{"abc", false},
		{"(a|b)*c", false},
		{"[a-z]+", false},
		{"(a", true},
	}

	for _, tc := range cases {
		re, err := Compile(tc.expr)
		if (err != nil) != tc.wantErr {
			t.Errorf("Compile(%q) error = %v, wantErr %v", tc.expr, err, tc.wantErr)
		}
		if err == nil && re == nil {
			t.Errorf("Compile(%q) returned nil Regexp", tc.expr)
		}
	}
}

func TestMustCompile(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			// Expected no panic for valid regex
		}
	}()
	MustCompile("abc")
}

func TestMustCompilePanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("MustCompile should panic for invalid regex")
		}
	}()
	MustCompile("(a")
}

var (
	// goldenPatterns defines the standard test cases for this engine.
	goldenPatterns = []struct {
		name    string
		pattern string
		payload string
		want    bool
	}{
		// 1. Literal: Simple linear chain of states
		{"Literal", `Tokyo`, "Tokyo is the capital of Japan.", true},
		{"Literal_NoMatch", `Kyoto`, "Tokyo is the capital of Japan.", false},
		// 2. Alternation: Branching fan-out from common states
		{"Alternation", `apple|orange|banana|grape|peach`, "I like to eat a banana for breakfast.", true},
		// 3. Character Class: Multiple transitions between states (Dense edges)
		{"CharClass", `[0-9a-fA-F]+`, "The hash is 4d2b1a3e and it is correct.", true},
		// 4. Repetition (Greedy): Back-loops and potential state explosion
		{"Repetition", `a*b`, "This is a string with aaaaaaaaab inside.", true},
		{"Repetition_NoMatch", `a*b`, "This is a string with only ccccc.", false},
		// 5. NFA Hard Case: Traditional NFA-heavy structure (e.g., (a|b)*abb)
		{"NFA_Hard", `(a|b)*abb`, "ababababababb is a classic test case.", true},
		// 6. Long Prefix: Test SIMD (bytes.Index) optimization
		{"LongPrefix", `This is a long prefix with a.*match`, "This is a long prefix with a certain match at the end.", true},
		{"LongPrefix_NoMatch", `This is a long prefix with a.*match`, "This is a long prefix but it ends differently.", false},
		// 7. Middle Match: Ensure skip logic works when match is in the middle
		{"MiddleMatch", `Target`, "Some prefix before the Target and some suffix after.", true},
		// 8. Anchors: Correct handling of boundaries
		{"BeginText", `^abc`, "abcx", true},
		{"BeginText_NoMatch", `^abc`, "xabc", false},
		{"EndText", `abc$`, "xabc", true},
		{"EndText_NoMatch", `abc$`, "abcx", false},
		{"WordBoundary", `\babc\b`, " abc ", true},
		{"WordBoundary_NoMatch", `\babc\b`, "xabcx", false},
	}
)

func TestPatterns(t *testing.T) {
	for _, tc := range goldenPatterns {
		t.Run(tc.name, func(t *testing.T) {
			re := MustCompile(tc.pattern)
			got := re.MatchString(tc.payload)
			if got != tc.want {
				t.Errorf("MatchString(%q) = %v, want %v", tc.payload, got, tc.want)
			}
		})
	}
}

func TestRegexp_Multiline(t *testing.T) {
	// Enable multiline
	pattern := "(?m)^abc$"
	re := MustCompile(pattern)

	tests := []struct {
		input string
		match bool
	}{
		{"abc", true},
		{"\nabc", true},
		{"abc\n", true},
		{"x\nabc\ny", true},
		{"xabc", false},
		{"abcx", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := re.MatchString(tt.input); got != tt.match {
				t.Errorf("MatchString(%q, %q) = %v; want %v", pattern, tt.input, got, tt.match)
			}
		})
	}
}
