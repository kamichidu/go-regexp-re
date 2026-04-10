package regexp

import (
	"bytes"
	"fmt"
	"unsafe"

	"github.com/kamichidu/go-regexp-re/internal/ir"
	"github.com/kamichidu/go-regexp-re/syntax"
)

// Regexp is the representation of a compiled regular expression.
type Regexp struct {
	expr        string
	numSubexp   int
	prefix      []byte
	prefixState ir.StateID
	complete    bool
	anchorStart bool
	anchorEnd   bool
	prog        *syntax.Prog
	dfa         *ir.DFA // DFA with search closure (.*?) for Match
	dfaMatch    *ir.DFA // DFA without search closure for Find boundaries
	match       func([]byte) (int, int)
	subexpNames []string
}

// Compile parses a regular expression and returns, if successful,
// a Regexp object that can be used to match against text.
func Compile(expr string) (*Regexp, error) {
	re, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return nil, err
	}
	numSubexp := re.MaxCap()
	subexpNames := make([]string, numSubexp+1)
	extractCapNames(re, subexpNames)

	anchorStart := false
	anchorEnd := false
	if re.Op == syntax.OpConcat && len(re.Sub) > 0 {
		if re.Sub[0].Op == syntax.OpBeginText {
			anchorStart = true
		}
		if re.Sub[len(re.Sub)-1].Op == syntax.OpEndText {
			anchorEnd = true
		}
	} else if re.Op == syntax.OpBeginText {
		anchorStart = true
	} else if re.Op == syntax.OpEndText {
		anchorEnd = true
	}

	re = syntax.Simplify(re)
	prefixStr, complete := syntax.Prefix(re)

	prog, err := syntax.Compile(re)
	if err != nil {
		return nil, err
	}
	dfa, err := ir.NewDFAForSearch(prog)
	if err != nil {
		return nil, err
	}
	dfaMatch, err := ir.NewDFAForMatch(prog)
	if err != nil {
		return nil, err
	}
	var prefixState ir.StateID = dfaMatch.StartState()
	if prefixStr != "" {
		trans := dfaMatch.Transitions()
		stride := dfaMatch.Stride()
		for _, c := range []byte(prefixStr) {
			prefixState = trans[int(prefixState)*stride+int(c)]
			if prefixState == ir.InvalidState {
				break
			}
		}
	}

	res := &Regexp{
		expr:        expr,
		numSubexp:   numSubexp,
		prefix:      []byte(prefixStr),
		prefixState: prefixState,
		complete:    complete,
		anchorStart: anchorStart,
		anchorEnd:   anchorEnd,
		prog:        prog,
		dfa:         dfa,
		dfaMatch:    dfaMatch,
		subexpNames: subexpNames,
	}

	if complete && numSubexp == 0 {
		// literal match optimization
		res.match = func(b []byte) (int, int) {
			if res.anchorStart && res.anchorEnd {
				if bytes.Equal(b, res.prefix) {
					return 0, len(b)
				}
				return -1, -1
			}
			if res.anchorStart {
				if bytes.HasPrefix(b, res.prefix) {
					return 0, len(res.prefix)
				}
				return -1, -1
			}
			if res.anchorEnd {
				if bytes.HasSuffix(b, res.prefix) {
					return len(b) - len(res.prefix), len(b)
				}
				return -1, -1
			}
			i := bytes.Index(b, res.prefix)
			if i >= 0 {
				return i, i + len(res.prefix)
			}
			return -1, -1
		}
	} else {
		res.match = func(b []byte) (int, int) {
			if res.dfa.HasAnchors() {
				return res.doMatchExtended(b)
			}
			return res.doMatchFast(b)
		}
	}
	return res, nil
}

// Match reports whether the byte slice b contains any match of the regular expression re.
func (re *Regexp) Match(b []byte) bool {
	i, _ := re.match(b)
	return i >= 0
}

// MatchString reports whether the string s contains any match of the regular expression re.
func (re *Regexp) MatchString(s string) bool {
	b := unsafe.Slice(unsafe.StringData(s), len(s))
	return re.Match(b)
}

// NumSubexp returns the number of parenthesized subexpressions in this Regexp.
func (re *Regexp) NumSubexp() int {
	return re.numSubexp
}

// LiteralPrefix returns a literal string that must begin any match
// of the regular expression re. It returns the boolean true if the
// literal string comprises the entire regular expression.
func (re *Regexp) LiteralPrefix() (prefix string, complete bool) {
	return string(re.prefix), re.complete
}

// FindSubmatchIndex returns a slice holding the index pairs identifying the leftmost match of
// the regular expression of b and the matches, if any, of its subexpressions.
func (re *Regexp) FindSubmatchIndex(b []byte) []int {
	start, end := re.match(b)
	if start < 0 {
		return nil
	}

	regs := ir.NFAMatch(re.prog, re.dfa.TrieRoots(), b, start, end, re.numSubexp)
	if regs == nil {
		// This should not happen if re.match found a match, but as a fallback:
		return []int{start, end}
	}
	return regs
}

func (re *Regexp) applyContextToState(d *ir.DFA, state ir.StateID, context syntax.EmptyOp) ir.StateID {
	if state == ir.InvalidState || context == 0 || d.Stride() <= 256 {
		return state
	}
	trans := d.Transitions()
	stride := d.Stride()
	// gosyntax.EmptyOp bits:
	// 0: EmptyBeginLine (1)
	// 1: EmptyEndLine (2)
	// 2: EmptyBeginText (4)
	// 3: EmptyEndText (8)
	// 4: EmptyWordBoundary (16)
	// 5: EmptyNoWordBoundary (32)
	virtualBytes := [6]int{
		ir.VirtualBeginLine,
		ir.VirtualEndLine,
		ir.VirtualBeginText,
		ir.VirtualEndText,
		ir.VirtualWordBoundary,
		ir.VirtualNoWordBoundary,
	}
	for {
		changed := false
		for bit := 0; bit < 6; bit++ {
			if (context & (1 << bit)) != 0 {
				idx := int(state)*stride + virtualBytes[bit]
				if idx < len(trans) {
					next := trans[idx]
					if next != ir.InvalidState && next != state {
						state = next
						changed = true
					}
				}
			}
		}
		if !changed {
			break
		}
	}
	return state
}

// MustCompile is like Compile but panics if the expression cannot be parsed.
func MustCompile(expr string) *Regexp {
	re, err := Compile(expr)
	if err != nil {
		panic(`regexp: Compile(` + quote(expr) + `): ` + err.Error())
	}
	return re
}

func (re *Regexp) String() string {
	return re.expr
}

func (re *Regexp) doMatchFast(b []byte) (int, int) {
	dfa := re.dfa
	trans := dfa.Transitions()
	stride := dfa.Stride()
	accepting := dfa.Accepting()

	// Optimization: Prefix skip
	if len(re.prefix) > 0 {
		if bytes.Index(b, re.prefix) < 0 {
			return -1, -1
		}
	}

	state := dfa.StartState()
	// Check for empty match at the beginning
	if accepting[state] {
		return re.findBoundary(b)
	}

	for i := 0; i < len(b); i++ {
		// Transition on byte b[i].
		// re.dfa is built with withSearch=true, so it already handles restarting.
		state = trans[int(state)*stride+int(b[i])]
		if state == ir.InvalidState {
			// This should not happen with search DFA unless there's an error in construction,
			// but we fallback to start state just in case.
			state = dfa.StartState()
		}

		if accepting[state] {
			// Found SOME match ending at i+1.
			// findBoundary will find the leftmost-longest one.
			return re.findBoundary(b)
		}
	}
	return -1, -1
}

func (re *Regexp) doMatchExtended(b []byte) (int, int) {
	dfa := re.dfa
	trans := dfa.Transitions()
	stride := dfa.Stride()
	accepting := dfa.Accepting()

	// Patterns with anchors.
	state := dfa.StartState()
	for i := 0; i <= len(b); i++ {
		// 1. Check if the current state + context is accepting.
		ctx := ir.CalculateContext(b, i)
		s := re.applyContextToState(dfa, state, ctx)
		if s != ir.InvalidState && accepting[s] {
			return re.findBoundary(b)
		}

		if i < len(b) {
			// 2. Transition on byte b[i].
			// Apply context (for anchors like ^ that can precede a character)
			state = re.applyContextToState(dfa, state, ctx)
			state = trans[int(state)*stride+int(b[i])]
			if state == ir.InvalidState {
				// Search closure: restart if stuck.
				// dfa is built withSearch=true.
				state = dfa.StartState()
				// Try transitioning from start again for this byte.
				state = re.applyContextToState(dfa, state, ctx)
				state = trans[int(state)*stride+int(b[i])]
				if state == ir.InvalidState {
					state = dfa.StartState()
				}
			}
		}
	}
	return -1, -1
}

func (re *Regexp) findBoundary(b []byte) (int, int) {
	dfa := re.dfaMatch
	trans := dfa.Transitions()
	stride := dfa.Stride()
	accepting := dfa.Accepting()
	startState := dfa.StartState()

	// Find the leftmost-longest match.
	for i := 0; i <= len(b); i++ {
		// Optimization: Prefix skip
		if len(re.prefix) > 0 {
			if re.anchorStart {
				if i > 0 || !bytes.HasPrefix(b, re.prefix) {
					return -1, -1
				}
			} else {
				if i < len(b) {
					idx := bytes.Index(b[i:], re.prefix)
					if idx < 0 {
						break
					}
					i += idx
				} else if len(b) == 0 && len(re.prefix) == 0 {
					// allowed for empty string match
				} else {
					break
				}
			}
		}

		ctx := ir.CalculateContext(b, i)
		state := re.applyContextToState(dfa, startState, ctx)
		lastAcceptingEnd := -1
		if state != ir.InvalidState && accepting[state] {
			lastAcceptingEnd = i
		}

		if state != ir.InvalidState {
			currState := state
			for j := i; j < len(b); j++ {
				next := trans[int(currState)*stride+int(b[j])]
				if next == ir.InvalidState {
					break
				}
				currState = re.applyContextToState(dfa, next, ir.CalculateContext(b, j+1))
				if currState == ir.InvalidState {
					break
				}
				if accepting[currState] {
					lastAcceptingEnd = j + 1
				}
			}
		}

		if lastAcceptingEnd != -1 {
			// Found the leftmost match!
			// (Because i increases, the first i that gives a match is the leftmost start).
			// And for that i, lastAcceptingEnd is the longest.
			return i, lastAcceptingEnd
		}

		if re.anchorStart {
			break
		}
	}
	return -1, -1
}

func (re *Regexp) FindStringSubmatchIndex(s string) []int {
	b := unsafe.Slice(unsafe.StringData(s), len(s))
	return re.FindSubmatchIndex(b)
}

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

func extractCapNames(re *syntax.Regexp, names []string) {
	if re.Op == syntax.OpCapture {
		if re.Cap < len(names) {
			names[re.Cap] = re.Name
		}
	}
	for _, sub := range re.Sub {
		extractCapNames(sub, names)
	}
}

func quote(s string) string {
	if len(s) <= 16 {
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprintf("%q...", s[:16])
}
