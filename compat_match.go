package regexp

import (
	"io"
	"unsafe"
)

func (re *Regexp) Match(b []byte) bool {
	start, _, _ := re.findIndexAt(b, 0, len(b), b)
	return start >= 0
}

func (re *Regexp) MatchString(s string) bool {
	b := unsafe.Slice(unsafe.StringData(s), len(s))
	return re.Match(b)
}

// MatchReader reports whether the regular expression
// matches the text read by the RuneReader.
//
// Deprecated: This method performs a full memory load of the reader's content
// before processing and is not truly streaming.
func (re *Regexp) MatchReader(r io.RuneReader) bool {
	return re.Match(readAll(r))
}

func Match(pattern string, b []byte) (matched bool, err error) {
	re, err := Compile(pattern)
	if err != nil {
		return false, err
	}
	return re.Match(b), nil
}

func MatchString(pattern string, s string) (matched bool, err error) {
	re, err := Compile(pattern)
	if err != nil {
		return false, err
	}
	return re.MatchString(s), nil
}

// MatchReader reports whether the regular expression pattern
// matches the text read by the RuneReader.
//
// Deprecated: This function performs a full memory load of the reader's content
// before processing and is not truly streaming.
func MatchReader(pattern string, r io.RuneReader) (matched bool, err error) {
	re, err := Compile(pattern)
	if err != nil {
		return false, err
	}
	return re.MatchReader(r), nil
}
