package regexp

import (
	"bytes"
	"fmt"

	"github.com/kamichidu/go-regexp-re/internal/ir"
	"github.com/kamichidu/go-regexp-re/syntax"
)

// Regexp is the representation of a compiled regular expression.
type Regexp struct {
	expr        string
	prefix      []byte
	prefixState ir.StateID
	prog        *syntax.Prog
	dfa         *ir.DFA
	match       func([]byte) bool
}

// Compile parses a regular expression and returns, if successful,
// a Regexp object that can be used to match against text.
func Compile(expr string) (*Regexp, error) {
	re, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return nil, err
	}
	re = syntax.Simplify(re)
	prefixStr, complete := syntax.Prefix(re)

	prog, err := syntax.Compile(re)
	if err != nil {
		return nil, err
	}
	dfa, err := ir.NewDFA(prog)
	if err != nil {
		return nil, err
	}
	res := &Regexp{
		expr:   expr,
		prefix: []byte(prefixStr),
		prog:   prog,
		dfa:    dfa,
	}

	if complete {
		res.match = func(b []byte) bool {
			return bytes.Contains(b, []byte(prefixStr))
		}
	} else if len(res.prefix) > 0 && !dfa.HasAnchors() {
		state := dfa.StartState()
		for _, b := range res.prefix {
			state = dfa.Next(state, int(b))
			if state == ir.InvalidState {
				break
			}
		}
		res.prefixState = state

		if dfa.HasAnchors() {
			res.match = res.doMatchExtendedPrefix
		} else {
			res.match = res.doMatchFastPrefix
		}
	} else {
		if dfa.HasAnchors() {
			res.match = res.doMatchExtended
		} else {
			res.match = res.doMatchFast
		}
	}
	return res, nil
}

// MustCompile is like Compile but panics if the expression cannot be parsed.
func MustCompile(expr string) *Regexp {
	re, err := Compile(expr)
	if err != nil {
		panic(`regexp: Compile(` + quote(expr) + `): ` + err.Error())
	}
	return re
}

// Match reports whether the byte slice b contains any match of the regular expression re.
func (re *Regexp) Match(b []byte) bool {
	return re.match(b)
}

// MatchString reports whether the string s contains any match of the regular expression re.
func (re *Regexp) MatchString(s string) bool {
	return re.match([]byte(s))
}

// FindSubmatchIndex returns a slice holding the index pairs identifying the leftmost match of
// the regular expression of b and the matches, if any, of its subexpressions.
func (re *Regexp) FindSubmatchIndex(b []byte) []int {
	if re.dfa.HasAnchors() {
		return re.doFindSubmatchExtended(b)
	}
	return re.doFindSubmatchFast(b)
}

// FindStringSubmatchIndex is the string version of FindSubmatchIndex.
func (re *Regexp) FindStringSubmatchIndex(s string) []int {
	return re.FindSubmatchIndex([]byte(s))
}

// FindSubmatch returns a slice of slices holding the text of the leftmost match of the
// regular expression in b and the matches, if any, of its subexpressions.
func (re *Regexp) FindSubmatch(b []byte) [][]byte {
	indices := re.FindSubmatchIndex(b)
	if indices == nil {
		return nil
	}
	result := make([][]byte, len(indices)/2)
	for i := range result {
		start, end := indices[2*i], indices[2*i+1]
		if start >= 0 && end >= 0 {
			result[i] = b[start:end]
		}
	}
	return result
}

// FindStringSubmatch is the string version of FindSubmatch.
func (re *Regexp) FindStringSubmatch(s string) []string {
	indices := re.FindStringSubmatchIndex(s)
	if indices == nil {
		return nil
	}
	result := make([]string, len(indices)/2)
	for i := range result {
		start, end := indices[2*i], indices[2*i+1]
		if start >= 0 && end >= 0 {
			result[i] = s[start:end]
		}
	}
	return result
}

func (re *Regexp) doFindSubmatchFast(b []byte) []int {
	dfa := re.dfa
	numRegs := re.prog.NumCap
	if numRegs < 2 {
		numRegs = 2
	}

	for i := 0; i <= len(b); i++ {
		state := dfa.StartState()
		registers := make([]int, numRegs)
		for k := range registers {
			registers[k] = -1
		}

		// Entry tags
		for _, tag := range dfa.EntryTags() {
			registers[tag.Index()] = i
		}

		var bestMatch []int
		if dfa.IsAccepting(state) {
			bestMatch = make([]int, numRegs)
			copy(bestMatch, registers)
			bestMatch[0] = i
			bestMatch[1] = i
		}

		searchBuf := b[i:]
		for j := 0; j < len(searchBuf); j++ {
			c := searchBuf[j]
			tags := dfa.Tags(state, int(c))
			state = dfa.Next(state, int(c))
			if state == ir.InvalidState {
				break
			}
			for _, tag := range tags {
				offset := i + j
				if tag.After() {
					offset++
				}
				registers[tag.Index()] = offset
			}

			if dfa.IsAccepting(state) {
				bestMatch = make([]int, numRegs)
				copy(bestMatch, registers)
				bestMatch[0] = i
				bestMatch[1] = i + j + 1
			}
		}

		if bestMatch != nil {
			return bestMatch
		}
	}
	return nil
}

func (re *Regexp) doFindSubmatchExtended(b []byte) []int {
	dfa := re.dfa
	numRegs := re.prog.NumCap
	if numRegs < 2 {
		numRegs = 2
	}

	for i := 0; i <= len(b); i++ {
		state := dfa.StartState()
		registers := make([]int, numRegs)
		for k := range registers {
			registers[k] = -1
		}

		// Initial entry tags
		for _, tag := range dfa.EntryTags() {
			registers[tag.Index()] = i
		}

		// Initial context
		state, tags := re.applyContextWithTags(state, re.calculateContext(b, i))
		for _, tag := range tags {
			// Tags on virtual transitions are always at the current position.
			registers[tag.Index()] = i
		}

		var bestMatch []int
		if dfa.IsAcceptingWithContext(state, re.prog, re.calculateContext(b, i)) {
			bestMatch = make([]int, numRegs)
			copy(bestMatch, registers)
			bestMatch[0] = i
			bestMatch[1] = i
		}

		searchBuf := b[i:]
		for j := 0; j < len(searchBuf); j++ {
			c := searchBuf[j]
			tags := dfa.Tags(state, int(c))
			state = dfa.Next(state, int(c))
			if state == ir.InvalidState {
				break
			}
			for _, tag := range tags {
				offset := i + j
				if tag.After() {
					offset++
				}
				registers[tag.Index()] = offset
			}

			// Context at i+j+1
			ctx := re.calculateContext(b, i+j+1)
			var ctags []ir.TagOp
			state, ctags = re.applyContextWithTags(state, ctx)
			for _, tag := range ctags {
				registers[tag.Index()] = i + j + 1
			}

			if dfa.IsAcceptingWithContext(state, re.prog, ctx) {
				bestMatch = make([]int, numRegs)
				copy(bestMatch, registers)
				bestMatch[0] = i
				bestMatch[1] = i + j + 1
			}
		}

		if bestMatch != nil {
			return bestMatch
		}
	}
	return nil
}

func (re *Regexp) applyContextWithTags(state ir.StateID, op syntax.EmptyOp) (ir.StateID, []ir.TagOp) {
	if state == ir.InvalidState {
		return ir.InvalidState, nil
	}
	var allTags []ir.TagOp
	for {
		changed := false
		var vbytes []int
		if op&syntax.EmptyBeginLine != 0 {
			vbytes = append(vbytes, ir.VirtualBeginLine)
		}
		if op&syntax.EmptyEndLine != 0 {
			vbytes = append(vbytes, ir.VirtualEndLine)
		}
		if op&syntax.EmptyBeginText != 0 {
			vbytes = append(vbytes, ir.VirtualBeginText)
		}
		if op&syntax.EmptyEndText != 0 {
			vbytes = append(vbytes, ir.VirtualEndText)
		}
		if op&syntax.EmptyWordBoundary != 0 {
			vbytes = append(vbytes, ir.VirtualWordBoundary)
		}
		if op&syntax.EmptyNoWordBoundary != 0 {
			vbytes = append(vbytes, ir.VirtualNoWordBoundary)
		}

		for _, vb := range vbytes {
			if next := re.dfa.Next(state, vb); next != ir.InvalidState {
				allTags = append(allTags, re.dfa.Tags(state, vb)...)
				state = next
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return state, allTags
}

// String returns the source text used to compile the regular expression.
func (re *Regexp) String() string {
	return re.expr
}

func (re *Regexp) doMatchFast(b []byte) bool {
	dfa := re.dfa
	for i := 0; i <= len(b); i++ {
		state := dfa.StartState()
		if dfa.IsAccepting(state) {
			return true
		}

		searchBuf := b[i:]
		for j := 0; j < len(searchBuf); j++ {
			state = dfa.Next(state, int(searchBuf[j]))
			if state == ir.InvalidState {
				break
			}
			if dfa.IsAccepting(state) {
				return true
			}
		}
	}
	return false
}

func (re *Regexp) doMatchFastPrefix(b []byte) bool {
	prefix := re.prefix
	plen := len(prefix)
	ps := re.prefixState
	dfa := re.dfa

	for i := 0; ; {
		idx := bytes.Index(b[i:], prefix)
		if idx < 0 {
			return false
		}
		i += idx

		state := ps
		if dfa.IsAccepting(state) {
			return true
		}

		for _, c := range b[i+plen:] {
			state = dfa.Next(state, int(c))
			if state == ir.InvalidState {
				break
			}
			if dfa.IsAccepting(state) {
				return true
			}
		}
		i++
	}
}

func (re *Regexp) doMatchExtended(b []byte) bool {
	for i := 0; i <= len(b); i++ {
		searchBuf := b[i:]

		state := re.dfa.StartState()

		// Initial context at position i
		state = re.applyContext(state, re.calculateContext(b, i))
		if re.dfa.IsAccepting(state) {
			return true
		}

		for j := 0; j < len(searchBuf); j++ {
			curr := searchBuf[j]

			state = re.dfa.Next(state, int(curr))
			if state == ir.InvalidState {
				break
			}

			// Calculate context at position i+j+1 more efficiently
			var op syntax.EmptyOp
			pos := i + j + 1
			if pos == len(b) {
				op |= syntax.EmptyEndText | syntax.EmptyEndLine
			} else if b[pos] == '\n' {
				op |= syntax.EmptyEndLine
			}
			if b[pos-1] == '\n' {
				op |= syntax.EmptyBeginLine
			}
			if isWordBoundary(b, pos) {
				op |= syntax.EmptyWordBoundary
			} else {
				op |= syntax.EmptyNoWordBoundary
			}

			state = re.applyContext(state, op)
			if re.dfa.IsAccepting(state) {
				return true
			}
		}
	}
	return false
}

func (re *Regexp) doMatchExtendedPrefix(b []byte) bool {
	prefix := re.prefix

	for i := 0; ; {
		idx := bytes.Index(b[i:], prefix)
		if idx < 0 {
			return false
		}
		i += idx

		state := re.dfa.StartState()

		// Initial context at position i
		state = re.applyContext(state, re.calculateContext(b, i))
		if re.dfa.IsAccepting(state) {
			return true
		}

		searchBuf := b[i:]
		for j := 0; j < len(searchBuf); j++ {
			curr := searchBuf[j]

			state = re.dfa.Next(state, int(curr))
			if state == ir.InvalidState {
				break
			}

			// Calculate context at position i+j+1 more efficiently
			var op syntax.EmptyOp
			pos := i + j + 1
			if pos == len(b) {
				op |= syntax.EmptyEndText | syntax.EmptyEndLine
			} else if b[pos] == '\n' {
				op |= syntax.EmptyEndLine
			}
			if b[pos-1] == '\n' {
				op |= syntax.EmptyBeginLine
			}
			if isWordBoundary(b, pos) {
				op |= syntax.EmptyWordBoundary
			} else {
				op |= syntax.EmptyNoWordBoundary
			}

			state = re.applyContext(state, op)
			if re.dfa.IsAccepting(state) {
				return true
			}
		}
		i++
	}
}

func (re *Regexp) calculateContext(b []byte, i int) syntax.EmptyOp {
	var op syntax.EmptyOp
	if i == 0 {
		op |= syntax.EmptyBeginText | syntax.EmptyBeginLine
	} else if b[i-1] == '\n' {
		op |= syntax.EmptyBeginLine
	}
	if i == len(b) {
		op |= syntax.EmptyEndText | syntax.EmptyEndLine
	} else if i < len(b) && b[i] == '\n' {
		op |= syntax.EmptyEndLine
	}
	if isWordBoundary(b, i) {
		op |= syntax.EmptyWordBoundary
	} else {
		op |= syntax.EmptyNoWordBoundary
	}
	return op
}

func (re *Regexp) applyContext(state ir.StateID, op syntax.EmptyOp) ir.StateID {
	if state == ir.InvalidState {
		return ir.InvalidState
	}
	for {
		changed := false
		if op&syntax.EmptyBeginLine != 0 {
			if next := re.dfa.Next(state, ir.VirtualBeginLine); next != ir.InvalidState {
				state = next
				changed = true
			}
		}
		if op&syntax.EmptyEndLine != 0 {
			if next := re.dfa.Next(state, ir.VirtualEndLine); next != ir.InvalidState {
				state = next
				changed = true
			}
		}
		if op&syntax.EmptyBeginText != 0 {
			if next := re.dfa.Next(state, ir.VirtualBeginText); next != ir.InvalidState {
				state = next
				changed = true
			}
		}
		if op&syntax.EmptyEndText != 0 {
			if next := re.dfa.Next(state, ir.VirtualEndText); next != ir.InvalidState {
				state = next
				changed = true
			}
		}
		if op&syntax.EmptyWordBoundary != 0 {
			if next := re.dfa.Next(state, ir.VirtualWordBoundary); next != ir.InvalidState {
				state = next
				changed = true
			}
		}
		if op&syntax.EmptyNoWordBoundary != 0 {
			if next := re.dfa.Next(state, ir.VirtualNoWordBoundary); next != ir.InvalidState {
				state = next
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return state
}

func isWordBoundary(b []byte, i int) bool {
	var r1, r2 rune = -1, -1
	if i > 0 {
		r1 = rune(b[i-1])
	}
	if i < len(b) {
		r2 = rune(b[i])
	}
	return isWord(r1) != isWord(r2)
}

func isWord(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
}

func quote(s string) string {
	if len(s) <= 16 {
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprintf("%q...", s[:16])
}
