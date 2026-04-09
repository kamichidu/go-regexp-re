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
	dfa         *ir.DFA
	match       func([]byte) bool
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
	dfa, err := ir.NewDFA(prog)
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
		subexpNames: subexpNames,
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

	if len(re.prefix) == 0 {
		state := startState
		if accepting[state] {
			return true
		}
		for i, c := range b {
			state = trans[int(state)*stride+int(c)]
			if state == ir.InvalidState {
				if re.anchorStart {
					return false
				}
				state = startState
			}
			if accepting[state] {
				if !re.anchorEnd || i == len(b)-1 {
					return true
				}
			}
		}
		return false
	}

	// Prefix acceleration
	for i := 0; i < len(b); {
		var idx int
		if re.anchorStart {
			if i > 0 {
				return false
			}
			if !bytes.HasPrefix(b, re.prefix) {
				return false
			}
			idx = 0
		} else {
			idx = bytes.Index(b[i:], re.prefix)
			if idx < 0 {
				return false
			}
		}
		i += idx

		state := re.prefixState
		if accepting[state] {
			if !re.anchorEnd || i+len(re.prefix) == len(b) {
				return true
			}
		}

		// Continue matching after the prefix
		curr := i + len(re.prefix)
		matched := false
		for curr < len(b) {
			c := b[curr]
			state = trans[int(state)*stride+int(c)]
			if state == ir.InvalidState {
				break
			}
			if accepting[state] {
				if !re.anchorEnd || curr == len(b)-1 {
					matched = true
					break
				}
			}
			curr++
		}
		if matched {
			return true
		}
		if re.anchorStart {
			return false
		}
		// If failed, start searching for the next prefix from i+1
		i++
	}
	return false
}

func (re *Regexp) doMatchExtended(b []byte) bool {
	dfa := re.dfa
	trans := dfa.Transitions()
	stride := dfa.Stride()
	accepting := dfa.Accepting()
	startState := dfa.StartState()

	if len(re.prefix) == 0 {
		state := startState
		// Initial context
		ctx := re.calculateContext(b, 0)
		state = re.applyContextToState(state, ctx)
		if accepting[state] {
			if !re.anchorEnd || len(b) == 0 {
				return true
			}
		}

		for i, c := range b {
			state = trans[int(state)*stride+int(c)]
			if state == ir.InvalidState {
				if re.anchorStart {
					return false
				}
				state = startState
			}

			ctx := re.calculateContext(b, i+1)
			state = re.applyContextToState(state, ctx)

			if accepting[state] {
				if !re.anchorEnd || i == len(b)-1 {
					return true
				}
			}
		}
		return false
	}

	for i := 0; i < len(b); {
		var idx int
		if re.anchorStart {
			if i > 0 {
				return false
			}
			if !bytes.HasPrefix(b, re.prefix) {
				return false
			}
			idx = 0
		} else {
			idx = bytes.Index(b[i:], re.prefix)
			if idx < 0 {
				return false
			}
		}
		i += idx

		state := startState
		ctx := re.calculateContext(b, i)
		state = re.applyContextToState(state, ctx)
		if accepting[state] {
			if !re.anchorEnd || i == len(b) {
				return true
			}
		}

		// Continue matching after the prefix start
		curr := i
		matched := false
		for curr < len(b) {
			c := b[curr]
			state = trans[int(state)*stride+int(c)]
			if state == ir.InvalidState {
				break
			}

			ctx = re.calculateContext(b, curr+1)
			state = re.applyContextToState(state, ctx)

			if accepting[state] {
				if !re.anchorEnd || curr == len(b)-1 {
					matched = true
					break
				}
			}
			curr++
		}
		if matched {
			return true
		}
		if re.anchorStart {
			return false
		}
		i++
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
	b := unsafe.Slice(unsafe.StringData(s), len(s))
	return re.match(b)
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
		idx := bytes.Index(b, re.prefix)
		if idx < 0 {
			return nil
		}
		return []int{idx, idx + len(re.prefix)}
	}

	match := re.doExecuteDFAIndex(b)
	if match == nil {
		return nil
	}
	// match[0] is start, match[1] is end.
	// 2nd pass: NFA rescan to extract submatches.
	return ir.NFAMatch(re.prog, re.dfa.TrieRoots(), b, match[0], match[1], re.numSubexp)
}

func (re *Regexp) doExecuteDFAIndex(b []byte) []int {
	dfa := re.dfa
	numRegs := 2 // Only track overall match (start, end)
	hasAnchors := dfa.HasAnchors()
	trans := dfa.Transitions()
	stride := dfa.Stride()
	accepting := dfa.Accepting()
	state := dfa.StartState()

	// Pre-allocate register pool.
	const maxPaths = 512
	poolA := make([]int, maxPaths*numRegs)
	poolB := make([]int, maxPaths*numRegs)
	for i := range poolA {
		poolA[i] = -1
		poolB[i] = -1
	}

	currPool := poolA
	nextPool := poolB

	// Initial paths in start state.
	initialPaths := dfa.NfaPaths(state)
	numCurrent := len(initialPaths)
	for k := 0; k < numCurrent; k++ {
		base := k * numRegs
		for r := 0; r < numRegs; r++ {
			currPool[base+r] = -1
		}
		// Match starting at index 0.
		currPool[base] = 0
		re.applyTagsToRegs(currPool[base:base+numRegs], dfa.EntryTagsForPath(k), 0)
	}

	// Initial Context
	if hasAnchors {
		ctx := re.calculateContext(b, 0)
		var nextState ir.StateID
		var n int
		var finalPool []int
		nextState, n, finalPool = re.applyContextWithPool(state, ctx, currPool, numCurrent, numRegs, 0, nextPool)
		if nextState != state || finalPool != nil {
			state = nextState
			numCurrent = n
			if finalPool != nil && &finalPool[0] == &nextPool[0] {
				currPool, nextPool = nextPool, currPool
			}
		}
	}

	var bestMatch []int
	bestPriority := 1<<30 - 1

	updateBestMatch := func(endOffset int, s ir.StateID, regs []int, numPaths int) {
		if re.anchorEnd && endOffset != len(b) {
			return
		}
		paths := dfa.NfaPaths(s)
		for k, path := range paths {
			if k >= numPaths {
				break
			}
			if re.prog.Inst[path.ID].Op == syntax.InstMatch {
				currRegs := regs[k*numRegs : (k+1)*numRegs]
				startOffset := currRegs[0]
				if startOffset == -1 {
					continue
				}
				// Preference order:
				// 1. Smaller startOffset (leftmost match)
				// 2. Smaller path.Priority (first pattern match)
				// 3. Larger endOffset (greedy match)
				if bestMatch == nil || startOffset < bestMatch[0] || (startOffset == bestMatch[0] && path.Priority < bestPriority) || (startOffset == bestMatch[0] && path.Priority == bestPriority && endOffset > bestMatch[1]) {
					bestMatch = []int{startOffset, endOffset}
					bestPriority = path.Priority
				}
			}
		}
	}

	if accepting[state] {
		updateBestMatch(0, state, currPool, numCurrent)
	}

	for i := 0; i < len(b); {
		if len(re.prefix) > 0 && (state == dfa.StartState() || (re.anchorStart && i == 0)) {
			var idx int
			if re.anchorStart {
				if i > 0 {
					return bestMatch
				}
				if !bytes.HasPrefix(b[i:], re.prefix) {
					return bestMatch
				}
				idx = 0
			} else {
				idx = bytes.Index(b[i:], re.prefix)
				if idx < 0 {
					return bestMatch
				}
			}
			i += idx

			// Initialize paths for a new match starting at i
			initialPaths := dfa.NfaPaths(state)
			numCurrent = len(initialPaths)
			for k := 0; k < numCurrent; k++ {
				base := k * numRegs
				for r := 0; r < numRegs; r++ {
					currPool[base+r] = -1
				}
				currPool[base] = i
				re.applyTagsToRegs(currPool[base:base+numRegs], dfa.EntryTagsForPath(k), i)
			}

			if hasAnchors {
				ctx := re.calculateContext(b, i)
				var nextState ir.StateID
				var n int
				var finalPool []int
				nextState, n, finalPool = re.applyContextWithPool(state, ctx, currPool, numCurrent, numRegs, i, nextPool)
				if nextState != state || finalPool != nil {
					state = nextState
					numCurrent = n
					if finalPool != nil && &finalPool[0] == &nextPool[0] {
						currPool, nextPool = nextPool, currPool
					}
				}
			}
			if accepting[state] {
				updateBestMatch(i, state, currPool, numCurrent)
			}
		}

		c := b[i]
		idx := int(state)*stride + int(c)
		nextState := trans[idx]
		if nextState == ir.InvalidState {
			if re.anchorStart {
				return bestMatch
			}
			nextState = dfa.StartState()
		}

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
			if srcIdx >= 0 && srcIdx < numCurrent {
				copy(nextPool[base:base+numRegs], currPool[srcIdx*numRegs:(srcIdx+1)*numRegs])
			} else {
				// New match starting at current position i
				for r := 0; r < numRegs; r++ {
					nextPool[base+r] = -1
				}
				nextPool[base] = i
				// Apply entry tags for the new match.
				// Since we are starting a new match, we use the entry tags of the start state for this path.
				re.applyTagsToRegs(nextPool[base:base+numRegs], dfa.EntryTagsForPath(k), i)
			}
			tags := tagPool[tagOffsets[start+uint32(k)]:tagOffsets[start+uint32(k)+1]]
			re.applyTagsToRegs(nextPool[base:base+numRegs], tags, i)
		}

		state = nextState
		numCurrent = numNext
		currPool, nextPool = nextPool, currPool

		if hasAnchors {
			ctx := re.calculateContext(b, i+1)
			var s2 ir.StateID
			var n int
			var finalPool []int
			s2, n, finalPool = re.applyContextWithPool(state, ctx, currPool, numCurrent, numRegs, i+1, nextPool)
			if s2 != state || finalPool != nil {
				state = s2
				numCurrent = n
				if finalPool != nil && &finalPool[0] == &nextPool[0] {
					currPool, nextPool = nextPool, currPool
				}
			}
		}

		if accepting[state] {
			updateBestMatch(i+1, state, currPool, numCurrent)
		}
		i++
	}
	return bestMatch
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
						if srcIdx >= 0 && srcIdx < numCurrent {
							copy(nextPool[base:base+numRegs], currPool[srcIdx*numRegs:(srcIdx+1)*numRegs])
						} else {
							for r := 0; r < numRegs; r++ {
								nextPool[base+r] = -1
							}
							nextPool[base] = offset
						}
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
	b := unsafe.Slice(unsafe.StringData(s), len(s))
	return re.FindSubmatchIndex(b)
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
	return ir.CalculateContext(b, i)
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
