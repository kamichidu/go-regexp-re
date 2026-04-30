package regexp

import (
	"github.com/kamichidu/go-regexp-re/internal/ir"
)

func (re *Regexp) Find(b []byte) []byte {
	loc := re.FindIndex(b)
	if loc == nil {
		return nil
	}
	return b[loc[0]:loc[1]]
}

func (re *Regexp) FindIndex(b []byte) []int {
	start, end, _ := re.findIndexAt(b, 0, len(b))
	if start < 0 {
		return nil
	}
	return []int{start, end}
}

func (re *Regexp) FindString(s string) string {
	loc := re.FindStringIndex(s)
	if loc == nil {
		return ""
	}
	return s[loc[0]:loc[1]]
}

func (re *Regexp) FindStringIndex(s string) []int {
	return re.FindIndex([]byte(s))
}

func (re *Regexp) FindAll(b []byte, n int) [][]byte {
	if n == 0 {
		return nil
	}
	var result [][]byte
	re.all(b, n, func(loc []int) {
		result = append(result, b[loc[0]:loc[1]])
	})
	return result
}

func (re *Regexp) FindAllIndex(b []byte, n int) [][]int {
	if n == 0 {
		return nil
	}
	var result [][]int
	re.all(b, n, func(loc []int) {
		result = append(result, loc)
	})
	return result
}

func (re *Regexp) FindAllString(s string, n int) []string {
	if n == 0 {
		return nil
	}
	b := []byte(s)
	var result []string
	re.all(b, n, func(loc []int) {
		result = append(result, string(b[loc[0]:loc[1]]))
	})
	return result
}

func (re *Regexp) FindAllStringIndex(s string, n int) [][]int {
	if n == 0 {
		return nil
	}
	return re.FindAllIndex([]byte(s), n)
}

func (re *Regexp) FindAllSubmatch(b []byte, n int) [][][]byte {
	if n == 0 {
		return nil
	}
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

func (re *Regexp) FindAllSubmatchIndex(b []byte, n int) [][]int {
	if n == 0 {
		return nil
	}
	var result [][]int
	re.allSubmatch(b, n, func(loc []int) {
		result = append(result, loc)
	})
	return result
}

func (re *Regexp) FindAllStringSubmatch(s string, n int) [][]string {
	if n == 0 {
		return nil
	}
	b := []byte(s)
	var result [][]string
	re.allSubmatch(b, n, func(loc []int) {
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
		start, end, _ := re.findIndexAt(b[pos:], pos, totalBytes)
		if start < 0 {
			break
		}
		deliver([]int{start, end})
		if pos >= totalBytes {
			break
		}
		advance := end - pos
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
			continue
		}
		result = append(result, s[start:m[0]])
		start = m[1]
	}
	result = append(result, s[start:])
	return result
}
