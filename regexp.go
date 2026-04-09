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
	matchFn     func([]byte) bool
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
	var prefixState ir.StateID = dfa.StartState()
	if prefixStr != "" {
		trans := dfa.Transitions()
		stride := dfa.Stride()
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

	if complete {
		res.matchFn = func(b []byte) bool {
			if res.anchorStart && res.anchorEnd {
				return bytes.Equal(b, res.prefix)
			}
			if res.anchorStart {
				return bytes.HasPrefix(b, res.prefix)
			}
			if res.anchorEnd {
				return bytes.HasSuffix(b, res.prefix)
			}
			return bytes.Contains(b, res.prefix)
		}
	} else if expr == "^" || expr == "$" || expr == "" || expr == "(?m)^" || expr == "(?m)$" {
		// Optimization: these patterns always match any string at least once.
		res.matchFn = func(b []byte) bool {
			return true
		}
	} else {
		res.matchFn = res.match
	}
	return res, nil
}

// Match reports whether the byte slice b contains any match of the regular expression re.
func (re *Regexp) Match(b []byte) bool {
	return re.matchFn(b)
}

// MatchString reports whether the string s contains any match of the regular expression re.
func (re *Regexp) MatchString(s string) bool {
	b := unsafe.Slice(unsafe.StringData(s), len(s))
	return re.matchFn(b)
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
	if re.complete && re.numSubexp == 0 {
		idx := -1
		if re.anchorStart {
			if bytes.HasPrefix(b, re.prefix) {
				idx = 0
			}
		} else {
			idx = bytes.Index(b, re.prefix)
		}
		if idx < 0 {
			return nil
		}
		if re.anchorEnd && idx+len(re.prefix) != len(b) {
			return nil
		}
		return []int{idx, idx + len(re.prefix)}
	}

	// 2-Pass Strategy
	// Pass 1: Find match boundary using leftmost-longest DFA scan
	start, end := re.findBoundary(b)
	if start < 0 {
		return nil
	}

	// Pass 2: Targeted rescan for submatches
	return ir.NFAMatch(re.prog, re.dfa.TrieRoots(), b, start, end, re.numSubexp)
}

func (re *Regexp) applyContextToState(dfa *ir.DFA, state ir.StateID, op syntax.EmptyOp) ir.StateID {
	if state == ir.InvalidState || op == 0 || !dfa.HasAnchors() {
		return state
	}
	trans := dfa.Transitions()
	stride := dfa.Stride()

	if op&syntax.EmptyBeginLine != 0 {
		if next := trans[int(state)*stride+ir.VirtualBeginLine]; next != ir.InvalidState {
			state = next
		}
	}
	if op&syntax.EmptyEndLine != 0 {
		if next := trans[int(state)*stride+ir.VirtualEndLine]; next != ir.InvalidState {
			state = next
		}
	}
	if op&syntax.EmptyBeginText != 0 {
		if next := trans[int(state)*stride+ir.VirtualBeginText]; next != ir.InvalidState {
			state = next
		}
	}
	if op&syntax.EmptyEndText != 0 {
		if next := trans[int(state)*stride+ir.VirtualEndText]; next != ir.InvalidState {
			state = next
		}
	}
	if op&syntax.EmptyWordBoundary != 0 {
		if next := trans[int(state)*stride+ir.VirtualWordBoundary]; next != ir.InvalidState {
			state = next
		}
	}
	if op&syntax.EmptyNoWordBoundary != 0 {
		if next := trans[int(state)*stride+ir.VirtualNoWordBoundary]; next != ir.InvalidState {
			state = next
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

func (re *Regexp) match(b []byte) bool {
	if re.dfa.HasAnchors() {
		return re.doMatchExtended(b)
	}
	return re.doMatchFast(b)
}

func (re *Regexp) doMatchFast(b []byte) bool {
	dfa := re.dfa
	trans := dfa.Transitions()
	stride := dfa.Stride()
	accepting := dfa.Accepting()
	startState := dfa.StartState()

	if accepting[startState] {
		return true
	}

	if len(re.prefix) == 0 {
		state := startState
		for _, c := range b {
			state = trans[int(state)*stride+int(c)]
			if state == ir.InvalidState {
				if re.anchorStart {
					return false
				}
				state = startState
				// Re-transition on the same byte from startState
				state = trans[int(state)*stride+int(c)]
				if state == ir.InvalidState {
					state = startState
				}
			}
			if accepting[state] {
				return true
			}
		}
		return false
	}

	for i := 0; i < len(b); {
		if re.anchorStart {
			if i > 0 || !bytes.HasPrefix(b, re.prefix) {
				return false
			}
			i = 0
		} else {
			idx := bytes.Index(b[i:], re.prefix)
			if idx < 0 {
				return false
			}
			i += idx
		}
		state := re.prefixState
		if accepting[state] {
			return true
		}
		curr := i + len(re.prefix)
		for curr < len(b) {
			state = trans[int(state)*stride+int(b[curr])]
			if state == ir.InvalidState {
				break
			}
			if accepting[state] {
				return true
			}
			curr++
		}
		if re.anchorStart {
			return false
		}
		i++
	}
	return false
}

func (re *Regexp) doMatchExtended(b []byte) bool {
	// For extended matching, we can use the search DFA if it has search closure.
	// But our extended matching logic also needs to handle anchors.
	// Since re.dfa has search closure, we can just run it once.
	dfa := re.dfa
	trans := dfa.Transitions()
	stride := dfa.Stride()
	accepting := dfa.Accepting()
	state := dfa.StartState()

	for i := 0; i <= len(b); i++ {
		state = re.applyContextToState(dfa, state, ir.CalculateContext(b, i))
		if accepting[state] {
			return true
		}
		if i < len(b) {
			state = trans[int(state)*stride+int(b[i])]
			if state == ir.InvalidState {
				// Search DFA should ideally not reach InvalidState unless anchored.
				if re.anchorStart {
					return false
				}
				// If it happens, we can't easily restart because of search closure.
				// But re.dfa is built with search closure, so state 0 already handles .*?
				state = dfa.StartState()
			}
		}
	}
	return false
}

func (re *Regexp) findBoundary(b []byte) (int, int) {
	dfa := re.dfaMatch
	trans := dfa.Transitions()
	stride := dfa.Stride()
	accepting := dfa.Accepting()
	startState := dfa.StartState()

	bestStart, bestEnd := -1, -1

	for i := 0; i <= len(b); i++ {
		state := re.applyContextToState(dfa, startState, ir.CalculateContext(b, i))
		lastAcceptingEnd := -1
		if state != ir.InvalidState && accepting[state] {
			lastAcceptingEnd = i
		}

		for curr := i; curr < len(b); curr++ {
			if state == ir.InvalidState {
				break
			}
			state = trans[int(state)*stride+int(b[curr])]
			if state == ir.InvalidState {
				break
			}
			state = re.applyContextToState(dfa, state, ir.CalculateContext(b, curr+1))
			if state == ir.InvalidState {
				break
			}
			if accepting[state] {
				lastAcceptingEnd = curr + 1
			}
		}

		if lastAcceptingEnd != -1 {
			if bestStart == -1 || i < bestStart || (i == bestStart && lastAcceptingEnd > bestEnd) {
				bestStart, bestEnd = i, lastAcceptingEnd
			}
			return bestStart, bestEnd
		}

		if re.anchorStart {
			break
		}
	}
	return bestStart, bestEnd
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
