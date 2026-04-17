package regexp

import (
	"fmt"
	"reflect"
	goregexp "regexp"
	"runtime"
	"strings"
	"testing"
)

var goldenPatterns []struct {
	name    string
	pattern string
	payload string
	want    bool
}

func init() {
	benchPayload := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 100) // ~4.5KB

	goldenPatterns = []struct {
		name    string
		pattern string
		payload string
		want    bool
	}{
		// 1. Literal: Simple linear chain of states
		{"Literal", `Tokyo`, "Tokyo is the capital of Japan.", true},
		{"Literal_NoMatch", `Kyoto`, "Tokyo is the capital of Japan.", false},
		{"Literal_Long", `lazy dog.`, benchPayload, true},
		{"Literal_Long_NoMatch", `SHERLOCK`, benchPayload, false},
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
		{"BeginText_Long", `^The quick`, benchPayload, true},
		{"BeginText_NoMatch", `^abc`, "xabc", false},
		{"EndText", `abc$`, "xabc", true},
		{"EndText_NoMatch", `abc$`, "abcx", false},
		{"WordBoundary", `\babc\b`, " abc ", true},
		{"WordBoundary_NoMatch", `\babc\b`, "xabcx", false},
		{"PrefixAnchor", `abc\b`, "abc ", true},
		{"LongPrefixAnchor", `abc\b`, "some very long text before the abc ", true},
		// 9. Capturing (for Submatch benchmarking)
		{"CaptureEmail", `([a-zA-Z0-9_.+-]+)@([a-zA-Z0-9-]+\.[a-zA-Z0-9-.]+)`, "Contact us at support@example.com", true},
		{"CaptureURI", `^([a-zA-Z][a-zA-Z0-9+.-]*):(\/\/([^/?#]*))?([^?#]*)(\?([^#]*))?(#(.*))?`, "https://example.com/path?q=1#fragment", true},
	}
}

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

func TestRegexp_GoldenPatterns(t *testing.T) {
	for _, tc := range goldenPatterns {
		t.Run(tc.name, func(t *testing.T) {
			re := MustCompile(tc.pattern)
			got := re.MatchString(tc.payload)
			if got != tc.want {
				t.Errorf("MatchString(%q) = %v, want %v", tc.payload, got, tc.want)
			}
			// Explicitly trigger GC after heavy patterns like URI
			if tc.name == "CaptureURI" {
				runtime.GC()
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

func TestRegexp_FindSubmatchIndex(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
	}{
		// Basic matching and capturing
		{`a`, "a"},
		{`(a)`, "a"},
		{`(a)b`, "ab"},
		{`a(b)c`, "abc"},
		{`(a)(b)c`, "abc"},
		{`((a)(b)c)`, "abc"},
		{`(a|b)c`, "ac"},
		{`(a|b)c`, "bc"},

		// Quantifiers (Greedy vs Lazy)
		{`(a*)b`, "aaab"},
		{"a*", "aaaa"},
		{"a*?", "aaaa"},
		{"a+", "aaaa"},
		{"a+?", "aaaa"},
		{"a*a", "aaaa"},
		{"a*?a", "aaaa"},
		{"a*(a)", "aaaa"},
		{"a*?(a)", "aaaa"},
		{"(a*)", "aaaa"},
		{"(a*?)", "aaaa"},
		{"(a+)", "aaaa"},
		{"(a+?)", "aaaa"},
		{"(a*)a", "aaaa"},
		{"(a*?)a", "aaaa"},

		// Alternation and Priorities
		{"a|aa", "aaaa"},
		{"aa|a", "aaaa"},
		{"(a)|(aa)", "aaaa"},
		{"(aa)|(a)", "aaaa"},
		{`(a|ab)`, "ab"},
		{`(a|ab)b`, "abb"},
		{`a*(|(b))c*`, "aacc"},

		// Anchors and Boundaries
		{`^(a)b$`, "ab"},
		{`^(a)b`, "ab"},
		{`(a)b$`, "ab"},
		{`\b(abc)\b`, "abc"},
		{"$^", ""},
		{"$", "abcde"},

		// Dot and Repetition
		{"(.*)a", "baaa"},
		{"(.*?)a", "baaa"},
		{"(.*)", "abcd"},
		{"(.*?)", "abcd"},
		{"(.*).*", "ab"},

		// Nested and Multiple Groups
		{`(([^xyz]*)(d))`, "abcd"},
		{`(a*)(a*)`, "aaa"},
		{"a(b*)", "abbaab"},
		{`a(b*)b`, "abbb"},

		// Zero-length / Optional matches
		{`(a){0}`, ""},
		{`(a)?b`, "b"},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"/"+tt.input, func(t *testing.T) {
			re, err := Compile(tt.pattern)
			if err != nil {
				t.Fatalf("Compile(%q) failed: %v", tt.pattern, err)
			}
			got := re.FindStringSubmatchIndex(tt.input)
			stdRe := goregexp.MustCompile(tt.pattern)
			want := stdRe.FindSubmatchIndex([]byte(tt.input))

			if reflect.DeepEqual(got, want) {
				return // Success
			}

			// Failure diagnostics
			t.Errorf("FindStringSubmatchIndex(%q, %q) = %v; want %v", tt.pattern, tt.input, got, want)
		})
	}

	expectedErrorTests := []struct {
		pattern string
		input   string
	}{
		// Zero-length infinite loops (Epsilon Loops)
		{`(|a)*`, "a"},
		{`(|a)*`, ""},
	}

	for _, tt := range expectedErrorTests {
		t.Run("ExpectedError/"+tt.pattern, func(t *testing.T) {
			_, err := Compile(tt.pattern)
			if err == nil {
				t.Errorf("Compile(%q) should have failed with epsilon loop error", tt.pattern)
			}
		})
	}
	// Reclaim memory after many small DFA builds
	runtime.GC()
}

func TestRegexp_FindStringSubmatch(t *testing.T) {
	re := MustCompile(`(a)(b)c`)
	got := re.FindStringSubmatch("abc")
	want := []string{"abc", "a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FindStringSubmatch = %v; want %v", got, want)
	}
}

func TestHTTP11Anchor(t *testing.T) {
	pattern := `(?m)HTTP/1.1$`
	re := MustCompile(pattern)
	tests := []struct {
		input string
		want  bool
	}{
		{"HTTP/1.1", true},
		{"GET / HTTP/1.1", true},
		{"HTTP/1.1\n", true}, // $ matches before newline
		{"HTTP/1.1 ", false},
		{"HTTP/1.0", false},
	}
	for _, tt := range tests {
		if got := re.MatchString(tt.input); got != tt.want {
			t.Errorf("MatchString(%q) = %v; want %v", tt.input, got, tt.want)
		}
	}
}

func TestSpecializationPath(t *testing.T) {
	// Pattern that exceeds 62 instructions for DFA path
	var longAlt strings.Builder
	longAlt.WriteString("(")
	for i := 0; i < 300; i++ {
		if i > 0 {
			longAlt.WriteString("|")
		}
		fmt.Fprintf(&longAlt, "v%03d", i)
	}
	longAlt.WriteString(")")
	patternDFA := longAlt.String()

	tests := []struct {
		pattern string
		want    string // "literal", "bit-parallel", "dfa", "dfa-anchor"
	}{
		{"Tokyo", "literal"},
		{"^abc$", "literal"},
		{"abc$", "literal"},
		{"^abc", "literal"},
		{"a|b|c", "bit-parallel"},
		{"[a-z]", "bit-parallel"},
		{"a*", "bit-parallel"},
		{"(a|b)*", "bit-parallel"},
		{patternDFA, "dfa"},
		{"^a|b$", "bit-parallel"},
		{"\\bword\\b", "bit-parallel"},
	}

	for _, tt := range tests {
		re, err := Compile(tt.pattern)
		if err != nil {
			t.Errorf("Compile(%q) error: %v", tt.pattern, err)
			continue
		}

		var got string
		switch re.strategy {
		case strategyLiteral:
			got = "literal"
		case strategyBitParallel:
			got = "bit-parallel"
		case strategyFast:
			got = "dfa"
		case strategyExtended:
			got = "dfa-anchor"
		default:
			got = "unknown"
		}

		if got != tt.want {
			t.Errorf("Pattern %q: got path %q, want %q (Inst count: %d)", tt.pattern, got, tt.want, len(re.prog.Inst))
		}
	}
}
