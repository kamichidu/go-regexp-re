package regexp

import (
	"github.com/kamichidu/go-regexp-re/internal/ir"
)

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
	totalBytes := len(b)
	pos := 0
	for i := 0; i < n; i++ {
		loc := re.findSubmatchIndexAt(b[pos:], pos, totalBytes)
		if loc == nil {
			break
		}
		deliver(loc[0:2])
		if pos >= totalBytes {
			break
		}
		advance := loc[1] - pos
		if advance == 0 {
			advance = 1 + ir.GetTrailingByteCount(b[pos])
		}
		pos += advance
		if pos > totalBytes {
			break
		}
	}
}

func (re *Regexp) allSubmatch(b []byte, n int, deliver func([]int)) {
	if n < 0 {
		n = len(b) + 1
	}
	totalBytes := len(b)
	pos := 0
	for i := 0; i < n; i++ {
		loc := re.findSubmatchIndexAt(b[pos:], pos, totalBytes)
		if loc == nil {
			break
		}
		deliver(loc)
		if pos >= totalBytes {
			break
		}
		advance := loc[1] - pos
		if advance == 0 {
			advance = 1 + ir.GetTrailingByteCount(b[pos])
		}
		pos += advance
		if pos > totalBytes {
			break
		}
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
