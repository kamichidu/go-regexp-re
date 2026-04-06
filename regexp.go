package regexp

import (
	"bytes"
	"fmt"

	"github.com/kamichidu/go-regexp-re/internal/ir"
	"github.com/kamichidu/go-regexp-re/syntax"
)

// Regexp is the representation of a compiled regular expression.
type Regexp struct {
	expr   string
	prefix []byte
	prog   *syntax.Prog
	dfa    *ir.DFA
}

// Compile parses a regular expression and returns, if successful,
// a Regexp object that can be used to match against text.
func Compile(expr string) (*Regexp, error) {
	re, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return nil, err
	}
	re = syntax.Simplify(re)
	prefixStr, _ := syntax.Prefix(re)
	prog, err := syntax.Compile(re)
	if err != nil {
		return nil, err
	}
	dfa, err := ir.NewDFA(prog)
	if err != nil {
		return nil, err
	}
	return &Regexp{
		expr:   expr,
		prefix: []byte(prefixStr),
		prog:   prog,
		dfa:    dfa,
	}, nil
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
	return re.doMatch(b)
}

// MatchString reports whether the string s contains any match of the regular expression re.
func (re *Regexp) MatchString(s string) bool {
	return re.doMatch([]byte(s))
}

// String returns the source text used to compile the regular expression.
func (re *Regexp) String() string {
	return re.expr
}

func (re *Regexp) doMatch(b []byte) bool {
	if re.dfa.HasAnchors() {
		return re.doMatchExtended(b)
	}
	return re.doMatchFast(b)
}

func (re *Regexp) doMatchFast(b []byte) bool {
	for i := 0; i <= len(b); i++ {
		searchBuf := b[i:]

		// Prefix skip optimization
		if i < len(b) && len(re.prefix) > 0 {
			idx := bytes.Index(searchBuf, re.prefix)
			if idx < 0 {
				return false
			}
			i += idx
			searchBuf = b[i:]
		}

		state := re.dfa.StartState()
		if re.dfa.IsAccepting(state) {
			return true
		}

		for j := 0; j < len(searchBuf); j++ {
			state = re.dfa.Next(state, int(searchBuf[j]))
			if state == ir.InvalidState {
				break
			}
			if re.dfa.IsAccepting(state) {
				return true
			}
		}
	}
	return false
}

func (re *Regexp) doMatchExtended(b []byte) bool {
	// Optimization: If pattern must start at the beginning, only try i=0.
	// We can check if the program always starts with EmptyBeginText.
	// For now, let's just keep the loop but it's a potential further optimization.

	for i := 0; i <= len(b); i++ {
		searchBuf := b[i:]

		// Prefix skip optimization
		if i < len(b) && len(re.prefix) > 0 {
			idx := bytes.Index(searchBuf, re.prefix)
			if idx < 0 {
				return false
			}
			i += idx
			searchBuf = b[i:]
		}

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
