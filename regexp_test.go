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
	wantIdx []int // Optional: explicit expectation for Mandate 2.19.1
}

func init() {
	benchPayload := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 100) // ~4.5KB

	goldenPatterns = []struct {
		name    string
		pattern string
		payload string
		want    bool
		wantIdx []int
	}{
		// 1. Literal: Simple linear chain of states
		{"Literal", `Tokyo`, "Tokyo is the capital of Japan.", true, nil},
		{"Literal_NoMatch", `Kyoto`, "Tokyo is the capital of Japan.", false, nil},
		{"Literal_Long", `lazy dog.`, benchPayload, true, nil},
		{"Literal_Long_NoMatch", `SHERLOCK`, benchPayload, false, nil},
		// 2. Alternation: Branching fan-out from common states
		{"Alternation", `apple|orange|banana|grape|peach`, "I like to eat a banana for breakfast.", true, nil},
		// 3. Character Class: Multiple transitions between states (Dense edges)
		{"CharClass", `[0-9a-fA-F]+`, "The hash is 4d2b1a3e and it is correct.", true, nil},
		// 4. Repetition (Greedy): Back-loops and potential state explosion
		{"Repetition", `a*b`, "This is a string with aaaaaaaaab inside.", true, nil},
		{"Repetition_NoMatch", `a*b`, "This is a string with only ccccc.", false, nil},
		// 5. NFA Hard Case: Traditional NFA-heavy structure (e.g., (a|b)*abb)
		{"NFA_Hard", `(a|b)*abb`, "ababababababb is a classic test case.", true, nil},
		// 6. Long Prefix: Test SIMD (bytes.Index) optimization
		{"LongPrefix", `This is a long prefix with a.*match`, "This is a long prefix with a certain match at the end.", true, []int{0, 42}},
		{"LongPrefix_NoMatch", `This is a long prefix with a.*match`, "This is a long prefix but it ends differently.", false, nil},
		// 7. Middle Match: Ensure skip logic works when match is in the middle
		{"MiddleMatch", `Target`, "Some prefix before the Target and some suffix after.", true, nil},
		// 8. Anchors: Correct handling of boundaries
		{"BeginText", `^abc`, "abcx", true, nil},
		{"BeginText_Long", `^The quick`, benchPayload, true, nil},
		{"BeginText_NoMatch", `^abc`, "xabc", false, nil},
		{"EndText", `abc$`, "xabc", true, nil},
		{"EndText_NoMatch", `abc$`, "abcx", false, nil},
		{"WordBoundary", `\babc\b`, " abc ", true, nil},
		{"WordBoundary_NoMatch", `\babc\b`, "xabcx", false, nil},
		{"PrefixAnchor", `abc\b`, "abc ", true, nil},
		{"LongPrefixAnchor", `abc\b`, "some very long text before the abc ", true, nil},
		// 9. Capturing (for Submatch benchmarking)
		{"CaptureEmail", `([a-zA-Z0-9_.+-]+)@([a-zA-Z0-9-]+\.[a-zA-Z0-9-.]+)`, "Contact us at support@example.com", true, nil},
		{"CaptureURI", `^([a-zA-Z][a-zA-Z0-9+.-]*):(\/\/([^/?#]*))?([^?#]*)(\?([^#]*))?(#(.*))?`, "https://example.com/path?q=1#fragment", true, nil},
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

			if tc.want && tc.wantIdx != nil {
				gotIdx := re.FindStringSubmatchIndex(tc.payload)
				if !reflect.DeepEqual(gotIdx, tc.wantIdx) {
					t.Errorf("FindStringSubmatchIndex(%q) = %v, want %v", tc.pattern, gotIdx, tc.wantIdx)
				}
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
		want    []int // Optional: if nil, use standard library
	}{
		// Basic matching and capturing
		{`a`, "a", nil},
		{`(a)`, "a", nil},
		{`(a)b`, "ab", nil},
		{`a(b)c`, "abc", nil},
		{`(a)(b)c`, "abc", nil},
		{`((a)(b)c)`, "abc", nil},
		{`(a|b)c`, "ac", nil},
		{`(a|b)c`, "bc", nil},

		// Quantifiers (Greedy vs Lazy)
		{`(a*)b`, "aaab", nil},
		{"a*", "aaaa", nil},
		{"a*?", "aaaa", nil},
		{"a+", "aaaa", nil},
		{"a+?", "aaaa", nil},
		{"a*a", "aaaa", nil},
		{"a*?a", "aaaa", nil},
		{"a*?(a)", "aaaa", nil},
		{"(a*)", "aaaa", nil},
		{"(a*?)", "aaaa", nil},
		{"(a+)", "aaaa", nil},
		{"(a+?)", "aaaa", nil},
		{"(a*?)a", "aaaa", nil},

		// Alternation and Priorities
		{"a|aa", "aaaa", nil},
		{"(a)|(aa)", "aaaa", nil},
		{"(a|ab)", "ab", nil},
		{"(a|ab)b", "abb", nil},

		// Anchors and Boundaries
		{`^(a)b$`, "ab", nil},
		{`^(a)b`, "ab", nil},
		{`(a)b$`, "ab", nil},
		{`\b(abc)\b`, "abc", nil},
		{"$^", "", nil},
		{"$", "abcde", nil},

		// Dot and Repetition (Mandate 2.19.1: '.' is byte-oriented)
		{"(.*?)a", "baaa", []int{0, 2, 0, 1}},
		{"(.*)", "abcd", []int{0, 4, 0, 4}},
		{"(.*?)", "abcd", []int{0, 0, 0, 0}},
		{"(.*).*", "ab", []int{0, 2, 0, 2}},

		// Nested and Multiple Groups
		{"a(b*)", "abbaab", nil},
		{"(a*)(a*)", "aaa", nil},

		// Zero-length / Optional matches
		{`(a){0}`, "", nil},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"/"+tt.input, func(t *testing.T) {
			re, err := Compile(tt.pattern)
			if err != nil {
				t.Fatalf("Compile(%q) failed: %v", tt.pattern, err)
			}
			got := re.FindStringSubmatchIndex(tt.input)

			want := tt.want
			if want == nil {
				stdRe := goregexp.MustCompile(tt.pattern)
				want = stdRe.FindSubmatchIndex([]byte(tt.input))
			}

			validateSubmatchIndex(t, tt.pattern, tt.input, got, want)
		})
	}

	knownSubmatchBugTests := []struct {
		pattern string
		input   string
		want    []int
	}{
		// Alternation Prefix Conflicts
		{"aa|a", "aaaa", nil},
		{"(aa)|(a)", "aaaa", nil},

		// Quantifier Overlaps (Supported but have known submatch bugs)
		{`a*(a)`, "aaaa", nil},
		{`(a*)a`, "aaaa", nil},
		{"(.*)a", "baaa", []int{0, 4, 0, 3}},
		{`a(b*)b`, "abbb", nil},
		{`(([^xyz]*)(d))`, "abcd", nil},
		{`(a)?b`, "b", nil},
	}

	for _, tt := range knownSubmatchBugTests {
		t.Run("KnownBug/"+tt.pattern+"/"+tt.input, func(t *testing.T) {
			re, err := Compile(tt.pattern)
			if err != nil {
				t.Fatalf("Compile(%q) failed: %v", tt.pattern, err)
			}
			got := re.FindStringSubmatchIndex(tt.input)

			want := tt.want
			if want == nil {
				stdRe := goregexp.MustCompile(tt.pattern)
				want = stdRe.FindSubmatchIndex([]byte(tt.input))
			}

			validateSubmatchIndex(t, tt.pattern, tt.input, got, want)
		})
	}

	expectedErrorPatterns := []string{
		// 1. Epsilon Loops (Infinite loops on empty match)
		`(|a)*`,
		`(a|)*`,
		`((a*)*)`,
		`((a?)*)`,

		// 2. Empty alternatives in captures (Ambiguous tag propagation)
		`(|a)`,   // Prefix empty
		`(a|)`,   // Suffix empty
		`(a||b)`, // Middle empty
		`((a|))`, // Nested empty

		// 3. Optional empty captures (OpQuest where body can match empty and has capture)
		`(a*)?`,
		`(a?|b?)?`,
		`((a|b)*)?`,

		// 4. Truly unsupported complex structures
		`a*(|(b))c*`,
	}

	for _, pattern := range expectedErrorPatterns {
		t.Run("ExpectedError/"+pattern, func(t *testing.T) {
			_, err := Compile(pattern)
			if err != nil {
				return // Success
			}
			t.Errorf("Compile(%q) should have failed with compatibility error", pattern)
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
		want    string // "literal", "dfa", "dfa-anchor"
	}{
		{"Tokyo", "literal"},
		{"^abc$", "literal"},
		{"abc$", "literal"},
		{"^abc", "literal"},
		{"a|b|c", "dfa"},
		{"[a-z]", "dfa"},
		{"a*", "dfa"},
		{"(a|b)*", "dfa"},
		{patternDFA, "dfa"},
		{"^a|b$", "dfa-anchor"},
		{"\\bword\\b", "dfa-anchor"},
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
