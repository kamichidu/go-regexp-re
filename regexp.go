package regexp

import (
	"bytes"
	"context"
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
	match       func([]byte) (int, int, int)
	subexpNames []string
}

// Compile parses a regular expression and returns, if successful,
// a Regexp object that can be used to match against text.
func Compile(expr string) (*Regexp, error) {
	return CompileContext(context.Background(), expr)
}

// CompileContext is like Compile but accepts a context to allow cancellation.
func CompileContext(ctx context.Context, expr string) (*Regexp, error) {
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
	re = syntax.Optimize(re)
	prefixStr, complete := syntax.Prefix(re)

	prog, err := syntax.Compile(re)
	if err != nil {
		return nil, err
	}
	dfa, err := ir.NewDFAForSearch(ctx, prog)
	if err != nil {
		return nil, err
	}
	dfaMatch, err := ir.NewDFAForMatch(ctx, prog)
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
		res.match = func(b []byte) (int, int, int) {
			if res.anchorStart && res.anchorEnd {
				if bytes.Equal(b, res.prefix) {
					return 0, len(b), 0
				}
				return -1, -1, -1
			}
			if res.anchorStart {
				if bytes.HasPrefix(b, res.prefix) {
					return 0, len(res.prefix), 0
				}
				return -1, -1, -1
			}
			if res.anchorEnd {
				if bytes.HasSuffix(b, res.prefix) {
					return len(b) - len(res.prefix), len(b), 0
				}
				return -1, -1, -1
			}
			i := bytes.Index(b, res.prefix)
			if i >= 0 {
				return i, i + len(res.prefix), 0
			}
			return -1, -1, -1
		}
	} else {
		res.bindMatchLoop()
	}
	return res, nil
}

// Match reports whether the byte slice b contains any match of the regular expression re.
func (re *Regexp) Match(b []byte) bool {
	i, _, _ := re.match(b)
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
	start, end, targetPriority := re.match(b)
	if start < 0 {
		return nil
	}

	// Special Case: Greedy loop resolution for simple DFA.
	// If targetPriority > 0, we use NFA rescan because a simple Tagged DFA
	// only records the tags for Priority 0 (the best path in each state).
	if targetPriority > 0 {
		regs := ir.NFAMatch(re.prog, re.dfa.TrieRoots(), b, start, end, re.numSubexp)
		if regs != nil {
			regs[0], regs[1] = start, end
			return regs
		}
	}

	// Phase 2: Simple Tagged DFA Rescan (O(n) for Priority 0 matches).
	regs := make([]int, (re.numSubexp+1)*2)
	for i := range regs {
		regs[i] = -1
	}
	regs[0] = start
	regs[1] = end

	dfa := re.dfaMatch
	trans := dfa.Transitions()
	stride := dfa.Stride()
	preTags := dfa.PreTags()
	postTags := dfa.PostTags()
	startState := dfa.StartState()

	recordTags := func(t uint64, pos int) {
		if t == 0 {
			return
		}
		for i := 0; i < 64; i++ {
			if (t & (1 << i)) != 0 {
				if i < len(regs) {
					regs[i] = pos
				}
			}
		}
	}

	recordTags(dfa.StartTags(), start)

	ctx := ir.CalculateContext(b, start)
	state := re.applyContextToState(dfa, startState, ctx, func(t uint64) {
		recordTags(t, start)
	})

	currState := state
	for i := start; i < end; i++ {
		idx := int(currState)*stride + int(b[i])
		recordTags(preTags[idx], i)
		recordTags(postTags[idx], i+1)

		next := trans[idx]
		if next == ir.InvalidState {
			break
		}
		currState = re.applyContextToState(dfa, next, ir.CalculateContext(b, i+1), func(t uint64) {
			recordTags(t, i+1)
		})
	}

	if currState != ir.InvalidState && dfa.IsAccepting(currState) {
		recordTags(dfa.MatchTags(currState), end)
	}

	return regs
}

func (re *Regexp) applyContextToState(d *ir.DFA, state ir.StateID, context syntax.EmptyOp, record func(uint64)) ir.StateID {
	if state == ir.InvalidState || context == 0 || d.Stride() <= 256 {
		return state
	}
	trans := d.Transitions()
	stride := d.Stride()
	preTags := d.PreTags()
	postTags := d.PostTags()

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
						if record != nil {
							record(preTags[idx])
							record(postTags[idx])
						}
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

func (re *Regexp) bindMatchLoop() {
	if re.dfa.HasAnchors() {
		re.match = func(b []byte) (int, int, int) {
			return execLoop[extendedLoopTrait](re, b)
		}
	} else {
		re.match = func(b []byte) (int, int, int) {
			return execLoop[fastLoopTrait](re, b)
		}
	}
}

type loopTrait interface {
	HasAnchors() bool
}

type fastLoopTrait struct{}

func (fastLoopTrait) HasAnchors() bool { return false }

type extendedLoopTrait struct{}

func (extendedLoopTrait) HasAnchors() bool { return true }

func execLoop[T loopTrait](re *Regexp, b []byte) (int, int, int) {
	var trait T
	dfa := re.dfa
	trans := dfa.Transitions()
	stride := dfa.Stride()
	accepting := dfa.Accepting()
	isAlwaysTrue := dfa.IsAlwaysTrueFunc()
	warpPoints := dfa.WarpPoints()
	warpPointState := dfa.WarpPointStates()

	if trait.HasAnchors() {
		state := dfa.StartState()
		for i := 0; i <= len(b); i++ {
			ctx := ir.CalculateContext(b, i)
			s := re.applyContextToState(dfa, state, ctx, nil)
			if s != ir.InvalidState && (accepting[s] || isAlwaysTrue(s)) {
				return re.findBoundary(b)
			}

			if i < len(b) {
				// SIMD Warp for extended loop (only if current state is not anchor-sensitive)
				// For now, only warp if the state is NOT affected by virtual bytes.
				// However, determining this statically is complex, so we skip warp in extended loop for safety.
				// In a future optimization, we can check if ALL transitions from this state lead to the same target for ALL virtual bytes.

				state = re.applyContextToState(dfa, state, ctx, nil)
				state = trans[int(state)*stride+int(b[i])]
				if state == ir.InvalidState {
					state = dfa.StartState()
					state = re.applyContextToState(dfa, state, ctx, nil)
					state = trans[int(state)*stride+int(b[i])]
					if state == ir.InvalidState {
						state = dfa.StartState()
					}
				}
			}
		}
	} else {
		i := 0
		if len(re.prefix) > 0 {
			idx := bytes.Index(b, re.prefix)
			if idx < 0 {
				return -1, -1, -1
			}
			i = idx
		}

		state := dfa.StartState()
		if accepting[state] || isAlwaysTrue(state) {
			return re.findBoundary(b)
		}

		for ; i < len(b); i++ {
			// SIMD Warp
			if wp := warpPoints[state]; wp != -1 {
				idx := bytes.IndexByte(b[i:], byte(wp))
				if idx < 0 {
					return -1, -1, -1
				}
				i += idx
				state = warpPointState[state]
			} else {
				state = trans[int(state)*stride+int(b[i])]
				if state == ir.InvalidState {
					state = dfa.StartState()
				}
			}

			if accepting[state] || isAlwaysTrue(state) {
				return re.findBoundary(b)
			}
		}
	}
	return -1, -1, -1
}

func (re *Regexp) findBoundary(b []byte) (int, int, int) {
	dfa := re.dfaMatch
	trans := dfa.Transitions()
	increments := dfa.PriorityIncrements()
	stride := dfa.Stride()
	accepting := dfa.Accepting()
	startState := dfa.StartState()

	for i := 0; i <= len(b); i++ {
		if len(re.prefix) > 0 {
			if re.anchorStart {
				if i > 0 {
					break
				}
				if !bytes.HasPrefix(b[i:], re.prefix) {
					return -1, -1, -1
				}
			} else {
				if i < len(b) {
					idx := bytes.Index(b[i:], re.prefix)
					if idx < 0 {
						break
					}
					i += idx
				} else if len(b) == 0 && len(re.prefix) == 0 {
					// Empty prefix, empty string - matches at 0.
				} else {
					break
				}
			}
		}

		ctx := ir.CalculateContext(b, i)
		state := re.applyContextToState(dfa, startState, ctx, nil)
		lastAcceptingEnd := -1
		bestAbsolutePriority := 1<<30 - 1

		if state != ir.InvalidState && accepting[state] {
			lastAcceptingEnd = i
			bestAbsolutePriority = dfa.AcceptingPriority(state)
			if dfa.IsBestMatch(state) {
				return i, lastAcceptingEnd, bestAbsolutePriority
			}
		}

		if state != ir.InvalidState {
			currState := state
			cumulativeIncrement := 0
			for j := i; j < len(b); j++ {
				idx := int(currState)*stride + int(b[j])
				next := trans[idx]
				if next == ir.InvalidState {
					break
				}
				cumulativeIncrement += int(increments[idx])

				currState = re.applyContextToState(dfa, next, ir.CalculateContext(b, j+1), nil)
				if currState == ir.InvalidState {
					break
				}
				if accepting[currState] {
					priority := cumulativeIncrement + dfa.AcceptingPriority(currState)
					if priority < bestAbsolutePriority {
						bestAbsolutePriority = priority
						lastAcceptingEnd = j + 1
					} else if priority == bestAbsolutePriority {
						lastAcceptingEnd = j + 1
					}

					if dfa.IsBestMatch(currState) {
						break
					}
				}
			}
		}

		if lastAcceptingEnd != -1 {
			return i, lastAcceptingEnd, bestAbsolutePriority
		}

		if re.anchorStart {
			break
		}
	}
	return -1, -1, -1
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
