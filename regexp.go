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
	numSubexp   int
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
	numSubexp := re.MaxCap()
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
		expr:      expr,
		numSubexp: numSubexp,
		prefix:    []byte(prefixStr),
		prog:      prog,
		dfa:       dfa,
	}

	if complete {
		res.match = func(b []byte) bool {
			return bytes.Contains(b, []byte(prefixStr))
		}
	} else {
		res.match = res.doMatch
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

// NumSubexp returns the number of parenthesized subexpressions in this Regexp.
func (re *Regexp) NumSubexp() int {
	return re.numSubexp
}

// FindSubmatchIndex returns a slice holding the index pairs identifying the leftmost match of
// the regular expression of b and the matches, if any, of its subexpressions.
func (re *Regexp) FindSubmatchIndex(b []byte) []int {
	return re.doExecute(b)
}

func (re *Regexp) doExecute(b []byte) []int {
	dfa := re.dfa
	numRegs := (re.numSubexp + 1) * 2
	hasAnchors := dfa.HasAnchors()

	for i := 0; i <= len(b); i++ {
		state := dfa.StartState()

		// regs stores registers for each NFA path in the current DFA state.
		nfaPaths := dfa.NfaPaths(state)
		regs := make([][]int, len(nfaPaths))
		for k := range regs {
			regs[k] = make([]int, numRegs)
			for r := range regs[k] {
				regs[k][r] = -1
			}
		}

		// Apply initial entry tags to all paths in the start state.
		for k := range regs {
			re.applyTagsToRegs(regs[k], dfa.EntryTagsForPath(k), i)
		}

		// Initial Context
		if hasAnchors {
			ctx := re.calculateContext(b, i)
			state, regs = re.applyContext(state, ctx, regs, i)
		}

		var bestMatch []int
		bestPriority := 1<<30 - 1

		updateBestMatch := func(endOffset int, currentState ir.StateID, currentRegs [][]int) {
			priority := dfa.AcceptingPriority(currentState)
			if priority > bestPriority {
				return
			}
			// If priority is same, we prefer LONGER match (higher endOffset).
			if bestMatch != nil && priority == bestPriority && endOffset <= bestMatch[1] {
				return
			}

			// Find which NFA path matched with this priority.
			paths := dfa.NfaPaths(currentState)
			for idx, p := range paths {
				// check if this path is a match
				if re.prog.Inst[p.ID].Op == syntax.InstMatch {
					if p.Priority == priority {
						bestPriority = priority
						bestMatch = make([]int, numRegs)
						copy(bestMatch, currentRegs[idx])
						bestMatch[0] = i
						bestMatch[1] = endOffset
						break
					}
				}
			}
		}

		if dfa.IsAccepting(state) {
			updateBestMatch(i, state, regs)
		}

		searchBuf := b[i:]
		for j := 0; j < len(searchBuf); j++ {
			c := searchBuf[j]
			nextState := dfa.Next(state, int(c))
			if nextState == ir.InvalidState {
				break
			}

			sources, tags := dfa.TransitionInfo(state, int(c))
			nextRegs := make([][]int, len(sources))
			for k, srcIdx := range sources {
				nextRegs[k] = make([]int, numRegs)
				if srcIdx >= 0 && int(srcIdx) < len(regs) {
					copy(nextRegs[k], regs[srcIdx])
				} else {
					for r := range nextRegs[k] {
						nextRegs[k][r] = -1
					}
				}
				re.applyTagsToRegs(nextRegs[k], tags[k], i+j)
			}

			state = nextState
			regs = nextRegs

			if hasAnchors {
				ctx := re.calculateContext(b, i+j+1)
				state, regs = re.applyContext(state, ctx, regs, i+j+1)
			}

			if dfa.IsAccepting(state) {
				updateBestMatch(i+j+1, state, regs)
			}
		}
		if bestMatch != nil {
			return bestMatch
		}
	}
	return nil
}

func (re *Regexp) applyTagsToRegs(regs []int, tags []ir.TagOp, offset int) {
	for _, tag := range tags {
		idx := tag.Index()
		if idx < len(regs) {
			off := offset
			if tag.After() {
				off++
			}
			regs[idx] = off
		}
	}
}

func (re *Regexp) applyContext(state ir.StateID, op syntax.EmptyOp, regs [][]int, offset int) (ir.StateID, [][]int) {
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
				sources, tags := re.dfa.TransitionInfo(state, vb)
				nextRegs := make([][]int, len(sources))
				for k, srcIdx := range sources {
					nextRegs[k] = make([]int, len(regs[0]))
					copy(nextRegs[k], regs[srcIdx])
					re.applyTagsToRegs(nextRegs[k], tags[k], offset)
				}
				state = next
				regs = nextRegs
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return state, regs
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

func (re *Regexp) doMatch(b []byte) bool {
	return re.doExecute(b) != nil
}

func (re *Regexp) String() string {
	return re.expr
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
