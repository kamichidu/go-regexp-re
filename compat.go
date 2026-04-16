package regexp

import (
	"io"
	"strings"
	"unicode/utf8"
)

// Match reports whether the byte slice b contains any match of the regular expression pattern.
func Match(pattern string, b []byte) (matched bool, err error) {
	re, err := Compile(pattern)
	if err != nil {
		return false, err
	}
	return re.Match(b), nil
}

// MatchString reports whether the string s contains any match of the regular expression pattern.
func MatchString(pattern string, s string) (matched bool, err error) {
	re, err := Compile(pattern)
	if err != nil {
		return false, err
	}
	return re.MatchString(s), nil
}

// MatchReader reports whether the text read from r contains any match of the regular expression pattern.
// Note: This implementation reads all data from the reader into memory before matching.
// It is provided for interface compatibility with the standard library but is less memory-efficient
// than other match methods.
func MatchReader(pattern string, r io.RuneReader) (matched bool, err error) {
	re, err := Compile(pattern)
	if err != nil {
		return false, err
	}
	return re.MatchReader(r), nil
}

// QuoteMeta returns a string that escapes all regular expression metacharacters
// inside the argument text; the returned string is a regular expression matching
// the literal text.
func QuoteMeta(s string) string {
	// A copy of the implementation from the standard library for compatibility.
	b := make([]byte, 2*len(s))
	j := 0
	for i := 0; i < len(s); i++ {
		if special(s[i]) {
			b[j] = '\\'
			j++
		}
		b[j] = s[i]
		j++
	}
	return string(b[0:j])
}

func special(b byte) bool {
	return strings.ContainsRune(`\.+*?()|[]{}^$`, rune(b))
}

// Find returns a slice holding the text of the leftmost match in b of the regular expression.
func (re *Regexp) Find(b []byte) []byte {
	loc := re.FindIndex(b)
	if loc == nil {
		return nil
	}
	return b[loc[0]:loc[1]]
}

// FindIndex returns a two-element slice of integers defining the location of the leftmost match in b.
func (re *Regexp) FindIndex(b []byte) []int {
	a := re.FindSubmatchIndex(b)
	if a == nil {
		return nil
	}
	return a[0:2]
}

// FindString returns a string holding the text of the leftmost match in s of the regular expression.
func (re *Regexp) FindString(s string) string {
	loc := re.FindStringIndex(s)
	if loc == nil {
		return ""
	}
	return s[loc[0]:loc[1]]
}

// FindStringIndex returns a two-element slice of integers defining the location of the leftmost match in s.
func (re *Regexp) FindStringIndex(s string) []int {
	return re.FindIndex([]byte(s))
}

// FindAll returns a slice of all successive matches of the expression.
func (re *Regexp) FindAll(b []byte, n int) [][]byte {
	var result [][]byte
	re.all(b, n, func(loc []int) {
		result = append(result, b[loc[0]:loc[1]])
	})
	return result
}

// FindAllIndex is the 'All' version of FindIndex.
func (re *Regexp) FindAllIndex(b []byte, n int) [][]int {
	var result [][]int
	re.all(b, n, func(loc []int) {
		result = append(result, loc)
	})
	return result
}

// FindAllString is the 'All' version of FindString.
func (re *Regexp) FindAllString(s string, n int) []string {
	var result []string
	re.all([]byte(s), n, func(loc []int) {
		result = append(result, s[loc[0]:loc[1]])
	})
	return result
}

// FindAllStringIndex is the 'All' version of FindStringIndex.
func (re *Regexp) FindAllStringIndex(s string, n int) [][]int {
	return re.FindAllIndex([]byte(s), n)
}

// FindAllSubmatch is the 'All' version of FindSubmatch.
func (re *Regexp) FindAllSubmatch(b []byte, n int) [][][]byte {
	var result [][][]byte
	re.allSubmatch(b, n, func(loc []int) {
		sub := make([][]byte, len(loc)/2)
		for i := range sub {
			if loc[2*i] >= 0 {
				sub[i] = b[loc[2*i]:loc[2*i+1]]
			}
		}
		result = append(result, sub)
	})
	return result
}

// FindAllSubmatchIndex is the 'All' version of FindSubmatchIndex.
func (re *Regexp) FindAllSubmatchIndex(b []byte, n int) [][]int {
	var result [][]int
	re.allSubmatch(b, n, func(loc []int) {
		result = append(result, loc)
	})
	return result
}

// FindAllStringSubmatch is the 'All' version of FindStringSubmatch.
func (re *Regexp) FindAllStringSubmatch(s string, n int) [][]string {
	var result [][]string
	re.allSubmatch([]byte(s), n, func(loc []int) {
		sub := make([]string, len(loc)/2)
		for i := range sub {
			if loc[2*i] >= 0 {
				sub[i] = s[loc[2*i]:loc[2*i+1]]
			}
		}
		result = append(result, sub)
	})
	return result
}

// FindAllStringSubmatchIndex is the 'All' version of FindStringSubmatchIndex.
func (re *Regexp) FindAllStringSubmatchIndex(s string, n int) [][]int {
	return re.FindAllSubmatchIndex([]byte(s), n)
}

func (re *Regexp) all(b []byte, n int, deliver func([]int)) {
	if n < 0 {
		n = len(b) + 1
	}
	offset := 0
	for i := 0; i < n; i++ {
		loc := re.FindIndex(b)
		if loc == nil {
			break
		}
		deliver([]int{offset + loc[0], offset + loc[1]})
		if len(b) == 0 {
			break
		}
		advance := loc[1]
		if advance == 0 {
			_, width := utf8.DecodeRune(b)
			advance = width
		}
		if advance > len(b) {
			break
		}
		b = b[advance:]
		offset += advance
	}
}

func (re *Regexp) allSubmatch(b []byte, n int, deliver func([]int)) {
	if n < 0 {
		n = len(b) + 1
	}
	numRegs := (re.numSubexp + 1) * 2
	offset := 0
	for i := 0; i < n; i++ {
		loc := re.FindSubmatchIndex(b)
		if loc == nil {
			break
		}
		locCopy := make([]int, numRegs)
		for j := range locCopy {
			if j < len(loc) && loc[j] >= 0 {
				locCopy[j] = loc[j] + offset
			} else {
				locCopy[j] = -1
			}
		}
		deliver(locCopy)
		if len(b) == 0 {
			break
		}
		advance := loc[1]
		if advance == 0 {
			_, width := utf8.DecodeRune(b)
			advance = width
		}
		if advance > len(b) {
			break
		}
		b = b[advance:]
		offset += advance
	}
}

// Split slices s into substrings separated by the expression and returns a slice of
// the substrings between those expression matches.
func (re *Regexp) Split(s string, n int) []string {
	if n == 0 {
		return nil
	}

	if n < 0 {
		n = len(s) + 1
	}

	var result []string
	start := 0
	matches := re.FindAllStringIndex(s, -1)
	for _, m := range matches {
		if n > 0 && len(result) >= n-1 {
			break
		}
		if m[1] == m[0] && (start > 0 && m[0] == start || m[0] == 0) {
			// This matches standard library's edge case skipping.
			continue
		}
		result = append(result, s[start:m[0]])
		start = m[1]
	}
	result = append(result, s[start:])
	return result
}

// SubexpNames returns the names of the parenthesized subexpressions in this Regexp.
func (re *Regexp) SubexpNames() []string {
	return re.subexpNames
}

// SubexpIndex returns the index of the first subexpression with the given name.
func (re *Regexp) SubexpIndex(name string) int {
	if name == "" {
		return -1
	}
	for i, n := range re.SubexpNames() {
		if i > 0 && name == n {
			return i
		}
	}
	return -1
}

// Expand appends the template to dst, replacing variables of the form $n or ${name}
// in the template with corresponding submatches from src.
func (re *Regexp) Expand(dst []byte, template []byte, src []byte, match []int) []byte {
	return re.expand(dst, string(template), src, match)
}

// ExpandString is like Expand but the template and source are strings.
func (re *Regexp) ExpandString(dst []byte, template string, src string, match []int) []byte {
	return re.expand(dst, template, []byte(src), match)
}

func (re *Regexp) expand(dst []byte, template string, src []byte, match []int) []byte {
	for i := 0; i < len(template); i++ {
		ch := template[i]
		if ch == '$' && i+1 < len(template) {
			i++
			if template[i] == '$' {
				dst = append(dst, '$')
				continue
			}
			var name string
			if template[i] == '{' {
				start := i + 1
				end := strings.IndexByte(template[start:], '}')
				if end >= 0 {
					name = template[start : start+end]
					i = start + end
				}
			} else {
				start := i
				for i < len(template) && (('a' <= template[i] && template[i] <= 'z') || ('A' <= template[i] && template[i] <= 'Z') || ('0' <= template[i] && template[i] <= '9') || template[i] == '_') {
					i++
				}
				name = template[start:i]
				i--
			}

			if name != "" {
				index := -1
				if isDigit(name[0]) {
					index = 0
					for j := 0; j < len(name); j++ {
						if isDigit(name[j]) {
							index = index*10 + int(name[j]-'0')
						} else {
							index = -1
							break
						}
					}
				} else {
					index = re.SubexpIndex(name)
				}
				if index >= 0 && index*2+1 < len(match) {
					if match[index*2] >= 0 {
						dst = append(dst, src[match[index*2]:match[index*2+1]]...)
					}
				}
			}
		} else {
			dst = append(dst, ch)
		}
	}
	return dst
}

func isDigit(b byte) bool {
	return '0' <= b && b <= '9'
}

// ReplaceAll returns a copy of src, replacing matches of the Regexp with the replacement text repl.
func (re *Regexp) ReplaceAll(src, repl []byte) []byte {
	var result []byte
	last := 0
	for _, loc := range re.FindAllSubmatchIndex(src, -1) {
		result = append(result, src[last:loc[0]]...)
		result = re.Expand(result, repl, src, loc)
		last = loc[1]
	}
	result = append(result, src[last:]...)
	return result
}

// ReplaceAllString returns a copy of src, replacing matches of the Regexp with the replacement string repl.
func (re *Regexp) ReplaceAllString(src, repl string) string {
	return string(re.ReplaceAll([]byte(src), []byte(repl)))
}

// ReplaceAllFunc returns a copy of src in which all matches of the Regexp have been replaced by the return value of function repl applied to the matched byte slice.
func (re *Regexp) ReplaceAllFunc(src []byte, repl func([]byte) []byte) []byte {
	var result []byte
	last := 0
	for _, loc := range re.FindAllIndex(src, -1) {
		result = append(result, src[last:loc[0]]...)
		result = append(result, repl(src[loc[0]:loc[1]])...)
		last = loc[1]
	}
	result = append(result, src[last:]...)
	return result
}

// ReplaceAllStringFunc returns a copy of src in which all matches of the Regexp have been replaced by the return value of function repl applied to the matched substring.
func (re *Regexp) ReplaceAllStringFunc(src string, repl func(string) string) string {
	return string(re.ReplaceAllFunc([]byte(src), func(b []byte) []byte {
		return []byte(repl(string(b)))
	}))
}

// ReplaceAllLiteral returns a copy of src, replacing matches of the Regexp with the replacement bytes repl.
func (re *Regexp) ReplaceAllLiteral(src, repl []byte) []byte {
	var result []byte
	last := 0
	for _, loc := range re.FindAllIndex(src, -1) {
		result = append(result, src[last:loc[0]]...)
		result = append(result, repl...)
		last = loc[1]
	}
	result = append(result, src[last:]...)
	return result
}

// ReplaceAllLiteralString returns a copy of src, replacing matches of the Regexp with the replacement string repl.
func (re *Regexp) ReplaceAllLiteralString(src, repl string) string {
	return string(re.ReplaceAllLiteral([]byte(src), []byte(repl)))
}

// MarshalText implements encoding.TextMarshaler.
func (re *Regexp) MarshalText() ([]byte, error) {
	return []byte(re.expr), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (re *Regexp) UnmarshalText(text []byte) error {
	newRe, err := Compile(string(text))
	if err != nil {
		return err
	}
	*re = *newRe
	return nil
}

// MatchReader reports whether the text read from r contains any match of the regular expression re.
// Note: This implementation reads all data from the reader into memory before matching.
// It is provided for interface compatibility with the standard library but is less memory-efficient
// than other match methods.
func (re *Regexp) MatchReader(r io.RuneReader) bool {
	var b strings.Builder
	for {
		rn, _, err := r.ReadRune()
		if err != nil {
			break
		}
		b.WriteRune(rn)
	}
	return re.MatchString(b.String())
}
