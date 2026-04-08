package regexp

import (
	"reflect"
	goregexp "regexp"
	"strings"
	"testing"
)

func TestMatch(t *testing.T) {
	pattern := `a+b`
	input := []byte("caaaab")
	got, err := Match(pattern, input)
	want, _ := goregexp.Match(pattern, input)
	if got != want || err != nil {
		t.Errorf("Match(%q, %q) = %v, %v; want %v, nil", pattern, string(input), got, err, want)
	}
}

func TestMatchString(t *testing.T) {
	pattern := `a+b`
	input := "caaaab"
	got, err := MatchString(pattern, input)
	want, _ := goregexp.MatchString(pattern, input)
	if got != want || err != nil {
		t.Errorf("MatchString(%q, %q) = %v, %v; want %v, nil", pattern, input, got, err, want)
	}
}

func TestQuoteMeta(t *testing.T) {
	s := `\.+*?()|[]{}^$`
	got := QuoteMeta(s)
	want := goregexp.QuoteMeta(s)
	if got != want {
		t.Errorf("QuoteMeta(%q) = %q; want %q", s, got, want)
	}
}

func TestFind(t *testing.T) {
	re := MustCompile(`a+`)
	input := []byte("baab")
	got := re.Find(input)
	want := goregexp.MustCompile(`a+`).Find(input)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Find(%q) = %q; want %q", string(input), string(got), string(want))
	}
}

func TestFindIndex(t *testing.T) {
	re := MustCompile(`a+`)
	input := []byte("baab")
	got := re.FindIndex(input)
	want := goregexp.MustCompile(`a+`).FindIndex(input)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FindIndex(%q) = %v; want %v", string(input), got, want)
	}
}

func TestFindString(t *testing.T) {
	re := MustCompile(`a+`)
	input := "baab"
	got := re.FindString(input)
	want := goregexp.MustCompile(`a+`).FindString(input)
	if got != want {
		t.Errorf("FindString(%q) = %q; want %q", input, got, want)
	}
}

func TestFindStringIndex(t *testing.T) {
	re := MustCompile(`a+`)
	input := "baab"
	got := re.FindStringIndex(input)
	want := goregexp.MustCompile(`a+`).FindStringIndex(input)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FindStringIndex(%q) = %v; want %v", input, got, want)
	}
}

func TestFindAll(t *testing.T) {
	re := MustCompile(`a`)
	input := []byte("banana")
	got := re.FindAll(input, -1)
	want := goregexp.MustCompile(`a`).FindAll(input, -1)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FindAll(%q) = %q; want %q", string(input), got, want)
	}
}

func TestFindAllString(t *testing.T) {
	re := MustCompile(`a`)
	input := "banana"
	got := re.FindAllString(input, -1)
	want := goregexp.MustCompile(`a`).FindAllString(input, -1)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FindAllString(%q) = %v; want %v", input, got, want)
	}
}

func TestFindAllSubmatchIndex(t *testing.T) {
	re := MustCompile(`a(n)`)
	input := []byte("banana")
	got := re.FindAllSubmatchIndex(input, -1)
	want := goregexp.MustCompile(`a(n)`).FindAllSubmatchIndex(input, -1)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FindAllSubmatchIndex(%q) = %v; want %v", string(input), got, want)
	}
}

func TestSplit(t *testing.T) {
	re := MustCompile(`a`)
	input := "banana"
	got := re.Split(input, -1)
	want := goregexp.MustCompile(`a`).Split(input, -1)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Split(%q) = %v; want %v", input, got, want)
	}
}

func TestReplaceAll(t *testing.T) {
	re := MustCompile(`a`)
	input := []byte("banana")
	repl := []byte("o")
	got := re.ReplaceAll(input, repl)
	want := goregexp.MustCompile(`a`).ReplaceAll(input, repl)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReplaceAll(%q, %q) = %q; want %q", string(input), string(repl), string(got), string(want))
	}
}

func TestReplaceAllString(t *testing.T) {
	re := MustCompile(`a(n)`)
	input := "banana"
	repl := "o$1"
	got := re.ReplaceAllString(input, repl)
	want := goregexp.MustCompile(`a(n)`).ReplaceAllString(input, repl)
	if got != want {
		t.Errorf("ReplaceAllString(%q, %q) = %q; want %q", input, repl, got, want)
	}
}

func TestReplaceAllLiteralString(t *testing.T) {
	re := MustCompile(`a(n)`)
	input := "banana"
	repl := "o$1"
	got := re.ReplaceAllLiteralString(input, repl)
	want := goregexp.MustCompile(`a(n)`).ReplaceAllLiteralString(input, repl)
	if got != want {
		t.Errorf("ReplaceAllLiteralString(%q, %q) = %q; want %q", input, repl, got, want)
	}
}

func TestReplaceAllNamed(t *testing.T) {
	re := MustCompile(`(?P<vowel>[aeiou])`)
	input := "banana"
	repl := "(${vowel})"
	got := re.ReplaceAllString(input, repl)
	want := goregexp.MustCompile(`(?P<vowel>[aeiou])`).ReplaceAllString(input, repl)
	if got != want {
		t.Errorf("ReplaceAllString (named) = %q; want %q", got, want)
	}
}

func TestSubexpNames(t *testing.T) {
	re := MustCompile(`(?P<first>a)(?P<second>b)`)
	got := re.SubexpNames()
	want := goregexp.MustCompile(`(?P<first>a)(?P<second>b)`).SubexpNames()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SubexpNames() = %v; want %v", got, want)
	}

	if got[0] != "" {
		t.Errorf("SubexpNames()[0] should be empty string, got %q", got[0])
	}

	index := re.SubexpIndex("second")
	wantIndex := goregexp.MustCompile(`(?P<first>a)(?P<second>b)`).SubexpIndex("second")
	if index != wantIndex {
		t.Errorf("SubexpIndex(%q) = %d; want %d", "second", index, wantIndex)
	}
}

func TestExpand(t *testing.T) {
	re := MustCompile(`a(n)`)
	input := []byte("banana")
	match := re.FindSubmatchIndex(input)

	cases := []struct {
		template string
		want     string
	}{
		{"$1", "n"},
		{"${1}", "n"},
		{"${1}0", "n0"},
		{"$$1", "$1"},
		{"$0", "an"},
	}

	for _, tc := range cases {
		got := string(re.Expand(nil, []byte(tc.template), input, match))
		if got != tc.want {
			t.Errorf("Expand(%q) = %q; want %q", tc.template, got, tc.want)
		}
	}
}

func TestFindAllEmpty(t *testing.T) {
	re := MustCompile(`a*`)
	input := "b"
	got := re.FindAllStringIndex(input, -1)
	want := goregexp.MustCompile(`a*`).FindAllStringIndex(input, -1)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FindAllStringIndex(`a*`, %q) = %v; want %v", input, got, want)
	}
}

func TestLiteralPrefix(t *testing.T) {
	re := MustCompile(`abc.*`)
	prefix, complete := re.LiteralPrefix()
	if prefix != "abc" || complete != false {
		t.Errorf("LiteralPrefix() = %q, %v; want %q, %v", prefix, complete, "abc", false)
	}

	re2 := MustCompile(`abc`)
	prefix2, complete2 := re2.LiteralPrefix()
	if prefix2 != "abc" || complete2 != true {
		t.Errorf("LiteralPrefix() = %q, %v; want %q, %v", prefix2, complete2, "abc", true)
	}
}

func TestMarshal(t *testing.T) {
	re := MustCompile(`a+b`)
	text, _ := re.MarshalText()
	if string(text) != `a+b` {
		t.Errorf("MarshalText() = %q; want %q", string(text), `a+b`)
	}

	var re2 Regexp
	if err := re2.UnmarshalText([]byte(`c+d`)); err != nil {
		t.Errorf("UnmarshalText() error: %v", err)
	}
	if re2.String() != `c+d` {
		t.Errorf("UnmarshalText() result = %q; want %q", re2.String(), `c+d`)
	}
}

func TestExpandAmbiguous(t *testing.T) {
	re := MustCompile(`a(n)`)
	input := []byte("banana")
	match := re.FindSubmatchIndex(input)

	// In Go standard regexp, $1x means group 1 followed by 'x'
	// because 'x' is not a digit.
	template := "$1x"
	got := string(re.Expand(nil, []byte(template), input, match))
	want := string(goregexp.MustCompile(`a(n)`).Expand(nil, []byte(template), input, match))
	if got != want {
		t.Errorf("Expand(%q) = %q; want %q", template, got, want)
	}
}

func TestMatchReader(t *testing.T) {
	cases := []struct {
		pattern string
		input   string
		want    bool
	}{
		{`abc`, "abc", true},
		{`abc`, "xabcy", true},
		{`^abc`, "abc", true},
		{`^abc`, "xabc", false},
		{`abc$`, "abc", true},
		{`abc$`, "abcx", false},
		{`\babc\b`, "abc", true},
		{`\babc\b`, "xabcy", false},
		{`日本語`, "日本語", true},
	}

	for _, tc := range cases {
		re := MustCompile(tc.pattern)
		got := re.MatchReader(strings.NewReader(tc.input))
		if got != tc.want {
			t.Errorf("MatchReader(%q, %q) = %v; want %v", tc.pattern, tc.input, got, tc.want)
		}

		got2, _ := MatchReader(tc.pattern, strings.NewReader(tc.input))
		if got2 != tc.want {
			t.Errorf("MatchReader function(%q, %q) = %v; want %v", tc.pattern, tc.input, got2, tc.want)
		}
	}
}
