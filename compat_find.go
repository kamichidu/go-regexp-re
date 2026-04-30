package regexp

import (
	"unsafe"

	"github.com/kamichidu/go-regexp-re/internal/ir"
)

func (re *Regexp) FindIndex(b []byte) []int {
	start, end, _ := re.findIndexAt(b, 0, len(b), b)
	if start < 0 {
		return nil
	}
	return []int{start, end}
}

func (re *Regexp) Find(b []byte) []byte {
	loc := re.FindIndex(b)
	if loc == nil {
		return nil
	}
	return b[loc[0]:loc[1]]
}

func (re *Regexp) FindString(s string) string {
	loc := re.FindStringIndex(s)
	if loc == nil {
		return ""
	}
	return s[loc[0]:loc[1]]
}

func (re *Regexp) FindStringIndex(s string) []int {
	b := unsafe.Slice(unsafe.StringData(s), len(s))
	return re.FindIndex(b)
}

func (re *Regexp) FindStringSubmatch(s string) []string {
	indices := re.FindStringSubmatchIndex(s)
	if indices == nil {
		return nil
	}
	result := make([]string, len(indices)/2)
	for i := range result {
		if start, end := indices[2*i], indices[2*i+1]; start >= 0 && end >= 0 {
			result[i] = s[start:end]
		}
	}
	return result
}

func (re *Regexp) FindStringSubmatchIndex(s string) []int {
	b := unsafe.Slice(unsafe.StringData(s), len(s))
	return re.findSubmatchIndexAt(b, 0, len(b), b)
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
	b := unsafe.Slice(unsafe.StringData(s), len(s))
	var result []string
	re.all(b, n, func(loc []int) {
		result = append(result, string(b[loc[0]:loc[1]]))
	})
	return result
}

func (re *Regexp) FindAllStringIndex(s string, n int) [][]int {
	b := unsafe.Slice(unsafe.StringData(s), len(s))
	return re.FindAllIndex(b, n)
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
	b := unsafe.Slice(unsafe.StringData(s), len(s))
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
	b := unsafe.Slice(unsafe.StringData(s), len(s))
	return re.FindAllSubmatchIndex(b, n)
}

func (re *Regexp) all(b []byte, n int, deliver func([]int)) {
	if n < 0 {
		n = len(b) + 1
	}
	pos := 0
	totalBytes := len(b)
	for i := 0; i < n; i++ {
		start, end, _ := re.findIndexAt(b[pos:], pos, totalBytes, b)
		if start < 0 {
			break
		}
		absStart, absEnd := start+pos, end+pos
		deliver([]int{absStart, absEnd})
		if pos >= totalBytes {
			break
		}
		// Correct advancement: 'end' is already the byte index to advance by
		advance := end
		if advance <= 0 {
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
	pos := 0
	totalBytes := len(b)
	for i := 0; i < n; i++ {
		loc := re.findSubmatchIndexAt(b[pos:], pos, totalBytes, b)
		if loc == nil {
			break
		}
		// findSubmatchIndexAt returns relative coordinates for the PROVIDED slice,
		// but since we provided b[pos:], we must add pos to get absolute coordinates.
		absStart, absEnd := loc[0]+pos, loc[1]+pos
		loc[0], loc[1] = absStart, absEnd
		for j := 1; j < len(loc)/2; j++ {
			if loc[2*j] >= 0 {
				loc[2*j] += pos
				loc[2*j+1] += pos
			}
		}
		deliver(loc)
		if pos >= totalBytes {
			break
		}
		advance := loc[1] - pos
		if advance <= 0 {
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
	for _, m := range re.FindAllStringIndex(s, n) {
		if len(result) == n-1 {
			break
		}
		result = append(result, s[start:m[0]])
		start = m[1]
	}
	result = append(result, s[start:])
	return result
}
