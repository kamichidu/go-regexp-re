package regexp

import (
	"io"
	"strings"
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
