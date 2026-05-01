package regexp

import (
	"github.com/kamichidu/go-regexp-re/internal/ir"
)

func (re *Regexp) ReplaceAll(src, repl []byte) []byte {
	return re.replaceAll(src, func(dst []byte, match []int) []byte {
		return re.Expand(dst, repl, src, match)
	})
}

func (re *Regexp) ReplaceAllString(src, repl string) string {
	return string(re.ReplaceAll([]byte(src), []byte(repl)))
}

func (re *Regexp) ReplaceAllLiteral(src, repl []byte) []byte {
	return re.replaceAll(src, func(dst []byte, match []int) []byte {
		return append(dst, repl...)
	})
}

func (re *Regexp) ReplaceAllLiteralString(src, repl string) string {
	return string(re.ReplaceAllLiteral([]byte(src), []byte(repl)))
}

func (re *Regexp) ReplaceAllFunc(src []byte, repl func([]byte) []byte) []byte {
	return re.replaceAll(src, func(dst []byte, match []int) []byte {
		return append(dst, repl(src[match[0]:match[1]])...)
	})
}

func (re *Regexp) ReplaceAllStringFunc(src string, repl func(string) string) string {
	return string(re.replaceAll([]byte(src), func(dst []byte, match []int) []byte {
		return append(dst, repl(string(src[match[0]:match[1]]))...)
	}))
}

func (re *Regexp) replaceAll(src []byte, repl func(dst []byte, match []int) []byte) []byte {
	var dst []byte
	pos := 0
	totalBytes := len(src)
	for pos <= totalBytes {
		match := re.findSubmatchIndexAt(src[pos:], pos, totalBytes, src)
		if match == nil {
			break
		}
		// match[0] and match[1] are now absolute
		dst = append(dst, src[pos:match[0]]...)
		dst = repl(dst, match)
		advance := match[1] - pos
		if advance <= 0 {
			if pos < totalBytes {
				dst = append(dst, src[pos])
				advance = 1 + ir.GetTrailingByteCount(src[pos])
			} else {
				pos = totalBytes + 1
				break
			}
		}
		pos += advance
	}
	if pos <= totalBytes {
		dst = append(dst, src[pos:]...)
	}
	return dst
}
