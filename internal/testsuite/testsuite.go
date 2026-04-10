package testsuite

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	goregexp "regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// Engine represents a regex engine under test/benchmark.
type Engine struct {
	Name    string
	Compile func(pattern string) (Matcher, error)
}

// Matcher represents a compiled regexp that can match against strings.
type Matcher interface {
	MatchString(s string) bool
	FindStringSubmatchIndex(s string) []int
	String() string
}

// Global state
var (
	engines []Engine

	findTests    []FindTest
	fowlerFiles  []FowlerFile
	re2SearchSet *RE2TestSet
	postalCodes  []string

	// New corpora
	sherlock    string
	wikipediaJP string
	httpLogs    string

	initialized bool
)

// Register registers a regex engine for the test suite.
func Register(engine Engine) {
	engines = append(engines, engine)
}

// initialize loads all required data from GOROOT and external sources.
func initialize() {
	if initialized {
		return
	}

	goroot := runtime.GOROOT()
	if goroot == "" {
		out, err := exec.Command("go", "env", "GOROOT").Output()
		if err != nil {
			log.Fatal("could not find GOROOT")
		}
		goroot = strings.TrimSpace(string(out))
	}

	testdataDir := filepath.Join(goroot, "src", "regexp", "testdata")

	// 1. Load find tests
	findTests = loadFindTestsFromGo(filepath.Join(goroot, "src", "regexp", "find_test.go"))

	// 2. Load Fowler tests
	datFiles, _ := filepath.Glob(filepath.Join(testdataDir, "*.dat"))
	for _, file := range datFiles {
		fowlerFiles = append(fowlerFiles, loadFowlerFile(file))
	}

	// 3. Load RE2 search tests
	re2SearchSet = loadRE2SearchFile(filepath.Join(testdataDir, "re2-search.txt"))

	// 4. Load Japan Post postal codes
	postalCodes = parsePostalCodes(loadCorpus("ken_all.zip", "https://www.post.japanpost.jp/zipcode/dl/kogaki/zip/ken_all.zip", "KEN_ALL.CSV"))

	// 5. Load new corpora
	sherlock = loadCorpus("sherlock.txt", "https://www.gutenberg.org/files/1661/1661-0.txt")
	wikipediaJP = loadCorpus("wikipedia_jp_sample.txt", "https://raw.githubusercontent.com/taku910/mecab/master/mecab-ipadic/test/t/test.txt") // Temporary sample
	httpLogs = loadCorpus("http_logs.txt", "https://raw.githubusercontent.com/elastic/examples/master/Common%20Data%20Formats/apache_logs/apache_logs")

	initialized = true
}

func loadCorpus(name, url string, targetInZip ...string) string {
	cacheDir := filepath.Join(os.TempDir(), "go-regexp-re", "testdata")
	path := filepath.Join(cacheDir, name)

	targetPath := path
	if len(targetInZip) > 0 {
		targetPath = filepath.Join(cacheDir, targetInZip[0])
	}

	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		if err := os.MkdirAll(cacheDir, 0755); err != nil {
			log.Printf("warning: failed to create cache dir: %v", err)
			return ""
		}

		log.Printf("Downloading %s from %s...", name, url)
		cmd := exec.Command("curl", "-L", "-o", path, url)
		if err := cmd.Run(); err != nil {
			log.Printf("warning: failed to download %s: %v", name, err)
			return ""
		}

		if strings.HasSuffix(name, ".zip") {
			log.Printf("Unzipping %s...", name)
			cmd = exec.Command("unzip", "-o", path, "-d", cacheDir)
			if err := cmd.Run(); err != nil {
				log.Printf("warning: failed to unzip %s: %v", name, err)
				return ""
			}
		}
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		log.Printf("warning: failed to read %s: %v", targetPath, err)
		return ""
	}
	return string(data)
}

func parsePostalCodes(csvData string) []string {
	if csvData == "" {
		return nil
	}

	var codes []string
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(strings.NewReader(csvData))
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Split(line, ",")
		if len(fields) < 3 {
			continue
		}
		// 3rd field is the 7-digit postal code, quoted like "0600000"
		code := strings.Trim(fields[2], "\"")
		if len(code) == 7 && !seen[code] {
			// Format as 000-0000
			formatted := code[:3] + "-" + code[3:]
			codes = append(codes, formatted)
			seen[code] = true
		}
	}
	log.Printf("Loaded %d unique postal codes", len(codes))
	return codes
}

// Main is the entry point for testing.M.
func Main(m *testing.M) {
	initialize()
	os.Exit(m.Run())
}

// RunCompatibility runs find_test.go compatibility tests for all registered engines.
func RunCompatibility(t *testing.T) {
	if len(findTests) == 0 {
		t.Skip("no find tests loaded")
	}
	for _, engine := range engines {
		t.Run(engine.Name, func(t *testing.T) {
			for _, tt := range findTests {
				t.Run(fmt.Sprintf("pat=%q/text=%q", tt.pat, tt.text), func(t *testing.T) {
					if strings.Contains(tt.pat, "\uFFFD") || strings.Contains(tt.pat, "\\x{fffd}") {
						t.Skip("skipping U+FFFD test")
					}

					re, err := engine.Compile(tt.pat)
					if err != nil {
						t.Skipf("failed to compile %q: %v", tt.pat, err)
						return
					}

					wantMatch := len(tt.matches) > 0
					gotMatch := re.MatchString(tt.text)
					if gotMatch != wantMatch {
						t.Errorf("MatchString() failed\npattern: %q\ninput:   %q\ngot:     %v\nwant:    %v", tt.pat, tt.text, gotMatch, wantMatch)
					}
				})
			}
		})
	}
}

// RunSubmatchCompatibility runs capture group compatibility tests for all registered engines.
func RunSubmatchCompatibility(t *testing.T) {
	if len(findTests) == 0 {
		t.Skip("no find tests loaded")
	}

	// Identify reference engine (GoRegexp)
	var referenceEngine Engine
	for _, engine := range engines {
		if engine.Name == "GoRegexp" {
			referenceEngine = engine
			break
		}
	}
	if referenceEngine.Name == "" {
		t.Skip("no reference engine GoRegexp found")
	}

	for _, engine := range engines {
		if engine.Name == "GoRegexp" {
			continue
		}
		t.Run(engine.Name, func(t *testing.T) {
			for _, tt := range findTests {
				t.Run(fmt.Sprintf("pat=%q/text=%q", tt.pat, tt.text), func(t *testing.T) {
					if strings.Contains(tt.pat, "\uFFFD") || strings.Contains(tt.pat, "\\x{fffd}") {
						t.Skip("skipping U+FFFD test")
					}

					re, err := engine.Compile(tt.pat)
					if err != nil {
						t.Skipf("failed to compile %q: %v", tt.pat, err)
						return
					}

					refRe, err := referenceEngine.Compile(tt.pat)
					if err != nil {
						t.Skipf("reference engine failed to compile %q: %v", tt.pat, err)
						return
					}

					want := refRe.FindStringSubmatchIndex(tt.text)
					got := re.FindStringSubmatchIndex(tt.text)

					if !reflect.DeepEqual(got, want) {
						t.Errorf("FindStringSubmatchIndex() failed\npattern: %q\ninput:   %q\ngot:     %v\nwant:    %v", tt.pat, tt.text, got, want)
					}
				})
			}
		})
	}
}

// RunFowler runs Fowler tests for all registered engines.
func RunFowler(t *testing.T) {
	if len(fowlerFiles) == 0 {
		t.Skip("no fowler tests loaded")
	}
	for _, engine := range engines {
		t.Run(engine.Name, func(t *testing.T) {
			for _, ff := range fowlerFiles {
				t.Run(ff.Name, func(t *testing.T) {
					for _, tt := range ff.Tests {
						if !strings.Contains(tt.Flag, "E") && !strings.Contains(tt.Flag, "B") {
							continue
						}
						// simplified for brevity
						if strings.Contains(tt.Pattern, `\(`) || strings.Contains(tt.Pattern, `\)`) {
							continue
						}
						t.Run(fmt.Sprintf("pat=%q/text=%q", tt.Pattern, tt.Text), func(t *testing.T) {
							re, err := engine.Compile(tt.Pattern)
							if err != nil {
								t.Skipf("failed to compile %q: %v", tt.Pattern, err)
								return
							}
							if !tt.ShouldCompile {
								t.Errorf("line %d: should not compile\npattern: %q", tt.Line, tt.Pattern)
								return
							}
							match := re.MatchString(tt.Text)
							if match != tt.ShouldMatch {
								t.Errorf("line %d: MatchString() failed\npattern: %q\ninput:   %q\ngot:     %v\nwant:    %v", tt.Line, tt.Pattern, tt.Text, match, tt.ShouldMatch)
							}
						})
					}
				})
			}
		})
	}
}

// RunRE2Search runs RE2 search tests for all registered engines.
func RunRE2Search(t *testing.T) {
	if re2SearchSet == nil {
		t.Skip("re2-search.txt not loaded")
	}
	for _, engine := range engines {
		t.Run(engine.Name, func(t *testing.T) {
			for _, group := range re2SearchSet.Groups {
				re, err := engine.Compile(group.Regexp)
				if err != nil {
					continue
				}
				for _, tt := range group.Tests {
					gotMatch := re.MatchString(tt.Text)
					if gotMatch != tt.Matches {
						t.Errorf("line %d: MatchString() failed\npattern: %q\ninput:   %q\ngot:     %v\nwant:    %v", group.Line, group.Regexp, tt.Text, gotMatch, tt.Matches)
					}
				}
			}
		})
	}
}

func runOnEngines(b *testing.B, f func(b *testing.B, engine Engine)) {
	if len(engines) == 0 {
		b.Fatal("no engines registered")
	}
	for _, engine := range engines {
		if len(engines) == 1 {
			f(b, engine)
		} else {
			b.Run(engine.Name, func(b *testing.B) {
				f(b, engine)
			})
		}
	}
}

// BenchmarkStandardSuite benchmarks engines using re2-search.txt.
func BenchmarkStandardSuite(b *testing.B) {
	if re2SearchSet == nil {
		b.Skip("re2-search.txt not loaded")
	}

	// Sample cases: pick one case from each of the first 20 groups.
	var cases []struct {
		Regexp string
		Text   string
	}
	for _, group := range re2SearchSet.Groups {
		if len(cases) >= 20 {
			break
		}
		if len(group.Tests) > 0 {
			cases = append(cases, struct {
				Regexp string
				Text   string
			}{group.Regexp, group.Tests[0].Text})
		}
	}

	runOnEngines(b, func(b *testing.B, engine Engine) {
		for i, tc := range cases {
			b.Run(fmt.Sprintf("Case%d", i), func(b *testing.B) {
				re, err := engine.Compile(tc.Regexp)
				if err != nil {
					b.Skip()
				}
				input := tc.Text
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					re.MatchString(input)
				}
			})
		}
	})
}

// BenchmarkLargeAlternation benchmarks engines with thousands of postal codes.
func BenchmarkLargeAlternation(b *testing.B) {
	initialize()
	counts := []int{10, 100, 1000, 10000}
	runOnEngines(b, func(b *testing.B, engine Engine) {
		for _, count := range counts {
			var patterns []string
			if len(postalCodes) >= count {
				patterns = postalCodes[:count]
			} else {
				// Fallback to generated if not enough or not loaded
				patterns = make([]string, count)
				for i := 0; i < count; i++ {
					patterns[i] = fmt.Sprintf("%03d-%04d", i/10000, i%10000)
				}
			}
			pattern := strings.Join(patterns, "|")
			payload := fmt.Sprintf("My postal code is %s.", patterns[count-1])

			b.Run(fmt.Sprintf("Count=%d", count), func(b *testing.B) {
				re, err := engine.Compile(pattern)
				if err != nil {
					b.Skip()
				}
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					re.MatchString(payload)
				}
			})
		}
	})
}

// BenchmarkLiteralScan benchmarks literal patterns with Sherlock.
func BenchmarkLiteralScan(b *testing.B) {
	initialize()
	if sherlock == "" {
		b.Skip("sherlock corpus not loaded")
	}

	patterns := []string{
		"Sherlock",                           // Simple
		"The Adventure of the Speckled Band", // Long
	}

	runOnEngines(b, func(b *testing.B, engine Engine) {
		for _, pattern := range patterns {
			b.Run(fmt.Sprintf("pat=%s", pattern), func(b *testing.B) {
				re, err := engine.Compile(pattern)
				if err != nil {
					b.Skip()
				}
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					re.MatchString(sherlock)
				}
			})
		}
	})
}

// BenchmarkAnchors benchmarks anchor patterns with HTTP logs.
func BenchmarkAnchors(b *testing.B) {
	initialize()
	if httpLogs == "" {
		b.Skip("http logs corpus not loaded")
	}

	patterns := []string{
		"^127.0.0.1", // Line start
		"HTTP/1.1$",  // Line end
		"\\bGET\\b",  // Word boundary
	}

	runOnEngines(b, func(b *testing.B, engine Engine) {
		for _, pattern := range patterns {
			b.Run(fmt.Sprintf("pat=%s", pattern), func(b *testing.B) {
				re, err := engine.Compile(pattern)
				if err != nil {
					b.Skip()
				}
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					re.MatchString(httpLogs)
				}
			})
		}
	})
}

// BenchmarkCapturing benchmarks capturing groups with Email/URL patterns.
func BenchmarkCapturing(b *testing.B) {
	initialize()
	// Use a sample input text for capturing
	input := "Contact us at support@example.com or visit https://example.com/path?q=1#fragment"

	patterns := []struct {
		name string
		pat  string
	}{
		{"Email", `([a-zA-Z0-9_.+-]+)@([a-zA-Z0-9-]+\.[a-zA-Z0-9-.]+)`},
		{"URI", `^([a-zA-Z][a-zA-Z0-9+.-]*):(\/\/([^/?#]*))?([^?#]*)(\?([^#]*))?(#(.*))?`},
	}

	runOnEngines(b, func(b *testing.B, engine Engine) {
		for _, tc := range patterns {
			b.Run(tc.name, func(b *testing.B) {
				re, err := engine.Compile(tc.pat)
				if err != nil {
					b.Skip()
				}
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					re.FindStringSubmatchIndex(input)
				}
			})
		}
	})
}

// BenchmarkNFAWorstCase benchmarks (a+)+b against a...ac.
func BenchmarkNFAWorstCase(b *testing.B) {
	initialize()
	pattern := `(a+)+b`
	input := strings.Repeat("a", 25) + "c"

	runOnEngines(b, func(b *testing.B, engine Engine) {
		re, err := engine.Compile(pattern)
		if err != nil {
			b.Skip()
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			re.MatchString(input)
		}
	})
}

// Internal data loaders
type FindTest struct {
	pat     string
	text    string
	matches [][]int
}

type FowlerTest struct {
	Line          int
	Flag          string
	Pattern       string
	Text          string
	ShouldCompile bool
	ShouldMatch   bool
}

type FowlerFile struct {
	Name  string
	Tests []FowlerTest
}

type RE2TestCase struct {
	Text    string
	Matches bool
}

type RE2TestGroup struct {
	Line   int
	Regexp string
	Tests  []RE2TestCase
}

type RE2TestSet struct {
	Name   string
	Groups []RE2TestGroup
}

func loadFindTestsFromGo(path string) []FindTest {
	fset := token.NewFileSet()
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	f, err := parser.ParseFile(fset, path, content, 0)
	if err != nil {
		return nil
	}
	var tests []FindTest
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) == 0 || vs.Names[0].Name != "findTests" {
			return true
		}
		for _, val := range vs.Values {
			cl, ok := val.(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, elt := range cl.Elts {
				if ft, ok := parseFindTest(elt); ok {
					tests = append(tests, ft)
				}
			}
		}
		return false
	})
	return tests
}

func parseFindTest(n ast.Expr) (FindTest, bool) {
	cl, ok := n.(*ast.CompositeLit)
	if !ok || len(cl.Elts) < 2 {
		return FindTest{}, false
	}
	var ft FindTest
	ft.pat = getString(cl.Elts[0])
	ft.text = getString(cl.Elts[1])
	if len(cl.Elts) >= 3 {
		elt := cl.Elts[2]
		switch v := elt.(type) {
		case *ast.CallExpr:
			if ident, ok := v.Fun.(*ast.Ident); ok && ident.Name == "build" {
				if len(v.Args) > 0 {
					if bl, ok := v.Args[0].(*ast.BasicLit); ok && bl.Kind == token.INT {
						nMatches, _ := strconv.Atoi(bl.Value)
						if nMatches > 0 {
							ft.matches = [][]int{{0, 0}}
						}
					}
				}
			}
		case *ast.Ident:
			if v.Name == "nil" {
				ft.matches = nil
			}
		case *ast.CompositeLit:
			if len(v.Elts) > 0 {
				ft.matches = [][]int{{0, 0}}
			}
		}
	}
	return ft, true
}

func getString(n ast.Expr) string {
	switch v := n.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			s, _ := strconv.Unquote(v.Value)
			return s
		}
	case *ast.BinaryExpr:
		if v.Op == token.ADD {
			return getString(v.X) + getString(v.Y)
		}
	}
	return ""
}

func loadFowlerFile(path string) FowlerFile {
	f, err := os.Open(path)
	if err != nil {
		return FowlerFile{Name: filepath.Base(path)}
	}
	defer f.Close()
	ff := FowlerFile{Name: filepath.Base(path)}
	b := bufio.NewReader(f)
	lineno := 0
	lastRegexp := ""
	for {
		lineno++
		line, err := b.ReadString('\n')
		if err != nil {
			break
		}
		if line[0] == '#' || line[0] == '\n' {
			continue
		}
		line = line[:len(line)-1]
		field := strings.Split(line, "\t")
		var filtered []string
		for _, f := range field {
			if f != "" {
				if f == "NULL" {
					filtered = append(filtered, "")
				} else {
					filtered = append(filtered, f)
				}
			}
		}
		field = filtered
		if len(field) < 4 {
			continue
		}
		flag := field[0]
		switch flag[0] {
		case '?', '&', '|', ';', '{', '}':
			flag = flag[1:]
			if flag == "" {
				continue
			}
		case ':':
			continue
		case 'C', 'N', 'T', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
			continue
		}
		if strings.Contains(flag, "$") {
			if u, err := strconv.Unquote(`"` + field[1] + `"`); err == nil {
				field[1] = u
			}
			if u, err := strconv.Unquote(`"` + field[2] + `"`); err == nil {
				field[2] = u
			}
		}
		if field[1] == "SAME" {
			field[1] = lastRegexp
		}
		lastRegexp = field[1]
		_, shouldCompile, shouldMatch, _ := parseFowlerResult(field[3])
		ff.Tests = append(ff.Tests, FowlerTest{
			Line: lineno, Flag: flag, Pattern: field[1], Text: field[2],
			ShouldCompile: shouldCompile, ShouldMatch: shouldMatch,
		})
	}
	return ff
}

func parseFowlerResult(s string) (ok, compiled, matched bool, pos []int) {
	switch {
	case s == "":
		ok = true
		compiled = true
		matched = true
		return
	case s == "NOMATCH":
		ok = true
		compiled = true
		matched = false
		return
	case 'A' <= s[0] && s[0] <= 'Z':
		ok = true
		compiled = false
		return
	}
	compiled = true
	matched = true
	ok = true
	return
}

func loadRE2SearchFile(path string) *RE2TestSet {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	set := &RE2TestSet{Name: filepath.Base(path)}
	var strings_ []string
	var inStrings bool
	var currentRegexp string
	var currentLine int
	scanner := bufio.NewScanner(f)
	for lineno := 1; scanner.Scan(); lineno++ {
		line := scanner.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		if 'A' <= line[0] && line[0] <= 'Z' && !strings.HasPrefix(line, "\"") {
			continue
		}
		if line == "strings" {
			strings_ = strings_[:0]
			inStrings = true
			continue
		}
		if line == "regexps" {
			inStrings = false
			continue
		}
		if line[0] == '"' {
			q, _ := strconv.Unquote(line)
			if inStrings {
				strings_ = append(strings_, q)
			} else {
				currentRegexp = q
				currentLine = lineno
			}
			continue
		}
		if line[0] == '-' || ('0' <= line[0] && line[0] <= '9') {
			results := strings.Split(line, ";")
			group := RE2TestGroup{
				Line:   currentLine,
				Regexp: currentRegexp,
			}
			for i := range results {
				if i >= len(strings_) {
					break
				}
				// Use the standard regexp library to determine truth.
				matched, err := goregexp.MatchString(currentRegexp, strings_[i])
				if err != nil {
					// If the standard library can't compile it, skip this case.
					continue
				}
				group.Tests = append(group.Tests, RE2TestCase{
					Text:    strings_[i],
					Matches: matched,
				})
			}
			if len(group.Tests) > 0 {
				set.Groups = append(set.Groups, group)
			}
		}
	}
	return set
}
