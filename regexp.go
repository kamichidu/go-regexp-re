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
	} else if dfa.HasAnchors() {
		res.match = res.doMatchExtended
	} else {
		res.match = res.doMatchFast
	}
	return res, nil
}

func (re *Regexp) doMatchFast(b []byte) bool {
	dfa := re.dfa
	trans := dfa.Transitions()
	stride := dfa.Stride()
	accepting := dfa.Accepting()
	startState := dfa.StartState()

	for i := 0; i <= len(b); i++ {
		state := startState
		if accepting[state] {
			return true
		}
		for j := i; j < len(b); j++ {
			c := b[j]
			state = trans[int(state)*stride+int(c)]
			if state == ir.InvalidState {
				break
			}
			if accepting[state] {
				return true
			}
		}
	}
	return false
}

func (re *Regexp) doMatchExtended(b []byte) bool {
	dfa := re.dfa
	trans := dfa.Transitions()
	stride := dfa.Stride()
	accepting := dfa.Accepting()
	startState := dfa.StartState()

	for i := 0; i <= len(b); i++ {
		state := startState

		// Context-based virtual bytes at the start
		ctx := re.calculateContext(b, i)
		state = re.applyContextToState(state, ctx)

		if accepting[state] {
			return true
		}

		searchBuf := b[i:]
		for j := 0; j < len(searchBuf); j++ {
			c := searchBuf[j]
			state = trans[int(state)*stride+int(c)]
			if state == ir.InvalidState {
				break
			}

			// Context-based virtual bytes at boundaries
			ctx := re.calculateContext(b, i+j+1)
			state = re.applyContextToState(state, ctx)

			if accepting[state] {
				return true
			}
		}
	}
	return false
}

func (re *Regexp) applyContextToState(state ir.StateID, op syntax.EmptyOp) ir.StateID {
	dfa := re.dfa
	trans := dfa.Transitions()
	stride := dfa.Stride()

	for {
		changed := false
		if op&syntax.EmptyBeginLine != 0 {
			if next := trans[int(state)*stride+ir.VirtualBeginLine]; next != ir.InvalidState {
				state, changed = next, true
			}
		}
		if op&syntax.EmptyEndLine != 0 {
			if next := trans[int(state)*stride+ir.VirtualEndLine]; next != ir.InvalidState {
				state, changed = next, true
			}
		}
		if op&syntax.EmptyBeginText != 0 {
			if next := trans[int(state)*stride+ir.VirtualBeginText]; next != ir.InvalidState {
				state, changed = next, true
			}
		}
		if op&syntax.EmptyEndText != 0 {
			if next := trans[int(state)*stride+ir.VirtualEndText]; next != ir.InvalidState {
				state, changed = next, true
			}
		}
		if op&syntax.EmptyWordBoundary != 0 {
			if next := trans[int(state)*stride+ir.VirtualWordBoundary]; next != ir.InvalidState {
				state, changed = next, true
			}
		}
		if op&syntax.EmptyNoWordBoundary != 0 {
			if next := trans[int(state)*stride+ir.VirtualNoWordBoundary]; next != ir.InvalidState {
				state, changed = next, true
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
	return re.doExecuteFastSubmatch(b)
}

func (re *Regexp) doExecuteFastSubmatch(b []byte) []int {
	dfa := re.dfa
	numRegs := (re.numSubexp + 1) * 2
	hasAnchors := dfa.HasAnchors()
	trans := dfa.Transitions()
	stride := dfa.Stride()
	accepting := dfa.Accepting()
	startState := dfa.StartState()

	// Pre-allocate register pool. Two sets (current and next) to avoid copies.
	const maxPaths = 512
	poolA := make([]int, maxPaths*numRegs)
	poolB := make([]int, maxPaths*numRegs)
	for i := range poolA {
		poolA[i] = -1
		poolB[i] = -1
	}

	for i := 0; i <= len(b); i++ {
		state := startState
		currentPaths := dfa.NfaPaths(state)

		currPool := poolA
		nextPool := poolB

		// Reset initial registers.
		for k := 0; k < len(currentPaths); k++ {
			base := k * numRegs
			for r := 0; r < numRegs; r++ {
				currPool[base+r] = -1
			}
			re.applyTagsToRegs(currPool[base:base+numRegs], dfa.EntryTagsForPath(k), i)
		}

		// Initial Context
		numCurrent := len(currentPaths)
		if hasAnchors {
			ctx := re.calculateContext(b, i)
			var nextState ir.StateID
			var n int
			var finalPool []int
			nextState, n, finalPool = re.applyContextWithPool(state, ctx, currPool, numCurrent, numRegs, i, nextPool)
			if nextState != state || len(finalPool) > 0 { // Just check if any transitions happened
				if n > 0 {
					state = nextState
					numCurrent = n
					// If the pool was swapped, currPool and nextPool need to be updated.
					// We can check if finalPool is actually currPool or nextPool.
					if &finalPool[0] == &nextPool[0] {
						currPool, nextPool = nextPool, currPool
					}
				}
			}
		}

		var bestMatch []int
		bestPriority := 1<<30 - 1

		updateBestMatch := func(endOffset int, s ir.StateID, regs []int) {
			p := dfa.AcceptingPriority(s)
			if bestMatch == nil || p < bestPriority || (p == bestPriority && endOffset > bestMatch[1]) {
				// Find which path matched.
				paths := dfa.NfaPaths(s)
				for idx, path := range paths {
					if re.prog.Inst[path.ID].Op == syntax.InstMatch && path.Priority == p {
						bestPriority = p
						bestMatch = make([]int, numRegs)
						copy(bestMatch, regs[idx*numRegs:(idx+1)*numRegs])
						bestMatch[0] = i
						bestMatch[1] = endOffset
						break
					}
				}
			}
		}

		if accepting[state] {
			updateBestMatch(i, state, currPool)
		}

		for j := i; j < len(b); j++ {
			c := b[j]
			idx := int(state)*stride + int(c)
			nextState := trans[idx]
			if nextState == ir.InvalidState {
				break
			}

			// Transition info.
			pathOffsets := dfa.TransPathOffsets()
			sources := dfa.PathSources()
			tagOffsets := dfa.PathTagOffsets()
			tagPool := dfa.TagPool()

			start := pathOffsets[idx]
			end := pathOffsets[idx+1]
			numNext := int(end - start)

			for k := 0; k < numNext; k++ {
				srcIdx := int(sources[start+uint32(k)])
				base := k * numRegs
				if srcIdx >= 0 {
					copy(nextPool[base:base+numRegs], currPool[srcIdx*numRegs:(srcIdx+1)*numRegs])
				} else {
					for r := 0; r < numRegs; r++ {
						nextPool[base+r] = -1
					}
				}
				tags := tagPool[tagOffsets[start+uint32(k)]:tagOffsets[start+uint32(k)+1]]
				re.applyTagsToRegs(nextPool[base:base+numRegs], tags, j)
			}

			state = nextState
			numCurrent = numNext
			currPool, nextPool = nextPool, currPool // Swap pools.

			if hasAnchors {
				ctx := re.calculateContext(b, j+1)
				var s2 ir.StateID
				var n int
				var finalPool []int
				s2, n, finalPool = re.applyContextWithPool(state, ctx, currPool, numCurrent, numRegs, j+1, nextPool)
				if s2 != state || len(finalPool) > 0 {
					if n > 0 {
						state = s2
						numCurrent = n
						if &finalPool[0] == &nextPool[0] {
							currPool, nextPool = nextPool, currPool
						}
					}
				}
			}

			if accepting[state] {
				updateBestMatch(j+1, state, currPool)
			}
		}
		if bestMatch != nil {
			return bestMatch
		}
	}
	return nil
}

func (re *Regexp) applyContextWithPool(state ir.StateID, op syntax.EmptyOp, currPool []int, numCurrent int, numRegs int, offset int, nextPool []int) (ir.StateID, int, []int) {
	dfa := re.dfa
	trans := dfa.Transitions()
	stride := dfa.Stride()

	pathOffsets := dfa.TransPathOffsets()
	sources := dfa.PathSources()
	tagOffsets := dfa.PathTagOffsets()
	tagPool := dfa.TagPool()

	vbytes := [...]int{
		ir.VirtualBeginLine,
		ir.VirtualEndLine,
		ir.VirtualBeginText,
		ir.VirtualEndText,
		ir.VirtualWordBoundary,
		ir.VirtualNoWordBoundary,
	}

	anyChanged := false
	for {
		changed := false
		for k, vb := range vbytes {
			if (op & syntax.EmptyOp(1<<k)) != 0 {
				idx := int(state)*stride + vb
				next := trans[idx]
				if next != ir.InvalidState {
					start, end := pathOffsets[idx], pathOffsets[idx+1]
					numNext := int(end - start)
					for nextK := 0; nextK < numNext; nextK++ {
						srcIdx := int(sources[start+uint32(nextK)])
						base := nextK * numRegs
						copy(nextPool[base:base+numRegs], currPool[srcIdx*numRegs:(srcIdx+1)*numRegs])
						tags := tagPool[tagOffsets[start+uint32(nextK)]:tagOffsets[start+uint32(nextK)+1]]
						re.applyTagsToRegs(nextPool[base:base+numRegs], tags, offset)
					}
					state = next
					numCurrent = numNext
					currPool, nextPool = nextPool, currPool // Swap.
					changed = true
					anyChanged = true
				}
			}
		}
		if !changed {
			break
		}
	}
	if !anyChanged {
		return state, numCurrent, nil
	}
	return state, numCurrent, currPool
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
	return re.match(b)
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
