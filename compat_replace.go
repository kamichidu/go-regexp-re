package regexp

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
