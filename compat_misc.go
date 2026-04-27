package regexp

import (
	"io"
	"strings"
)

// NumSubexp returns the number of parenthesized subexpressions in this Regexp.
func (re *Regexp) NumSubexp() int {
	return re.numSubexp
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
					sIdx, eIdx := match[index*2], match[index*2+1]
					if sIdx >= 0 && eIdx >= sIdx && eIdx <= len(src) {
						dst = append(dst, src[sIdx:eIdx]...)
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
