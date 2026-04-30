package regexp

import (
	"io"
	"strings"
)

func (re *Regexp) NumSubexp() int {
	return re.numSubexp
}

func (re *Regexp) SubexpNames() []string {
	return re.subexpNames
}

func (re *Regexp) SubexpIndex(name string) int {
	if name == "" {
		return -1
	}
	for i, n := range re.subexpNames {
		if n == name {
			return i
		}
	}
	return -1
}

func (re *Regexp) MarshalText() ([]byte, error) {
	return []byte(re.expr), nil
}

func (re *Regexp) UnmarshalText(text []byte) error {
	r, err := Compile(string(text))
	if err != nil {
		return err
	}
	*re = *r
	return nil
}

func (re *Regexp) MatchReader(r io.RuneReader) bool {
	// Simple implementation for compatibility
	var b []byte
	for {
		rn, _, err := r.ReadRune()
		if err != nil {
			break
		}
		var buf [8]byte
		n := copy(buf[:], string(rn))
		b = append(b, buf[:n]...)
	}
	return re.Match(b)
}

func (re *Regexp) Expand(dst []byte, template []byte, src []byte, match []int) []byte {
	// Standard Expand implementation
	for i := 0; i < len(template); i++ {
		b := template[i]
		if b == '$' && i+1 < len(template) {
			i++
			b = template[i]
			if b == '$' {
				dst = append(dst, '$')
				continue
			}
			var num int
			if b == '{' {
				start := i + 1
				for i+1 < len(template) && template[i+1] != '}' {
					i++
				}
				if i+1 < len(template) && template[i+1] == '}' {
					name := string(template[start : i+1])
					i++
					// If name is numeric, use group number
					isNum := len(name) > 0
					num = 0
					for j := 0; j < len(name); j++ {
						if name[j] >= '0' && name[j] <= '9' {
							num = num*10 + int(name[j]-'0')
						} else {
							isNum = false
							break
						}
					}
					if !isNum {
						num = re.SubexpIndex(name)
					}
				}
			} else if b >= '0' && b <= '9' {
				num = int(b - '0')
				for i+1 < len(template) && template[i+1] >= '0' && template[i+1] <= '9' {
					i++
					num = num*10 + int(template[i]-'0')
				}
			} else {
				// Invalid sequence, skip
				dst = append(dst, '$', b)
				continue
			}

			if num >= 0 && num*2+1 < len(match) && match[num*2] >= 0 {
				dst = append(dst, src[match[num*2]:match[num*2+1]]...)
			}
			continue
		}
		dst = append(dst, b)
	}
	return dst
}

func (re *Regexp) ExpandString(dst []byte, template string, src string, match []int) []byte {
	return re.Expand(dst, []byte(template), []byte(src), match)
}

func QuoteMeta(s string) string {
	// Standard QuoteMeta implementation
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
