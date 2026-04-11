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
	dfa         *ir.DFA // Unified DFA for Search and Match
	match       func([]byte) (int, int, int)
	subexpNames []string
}

// CompileOption defines options for regex compilation.
type CompileOption struct {
	// MaxMemory is the maximum allowed memory for DFA construction.
	// Defaults to 64 MiB if zero.
	MaxMemory int
}

// Compile parses a regular expression and returns, if successful,
// a Regexp object that can be used to match against text.
func Compile(expr string) (*Regexp, error) {
	return CompileContext(context.Background(), expr)
}

// CompileWithOption is like Compile but allows specifying options.
func CompileWithOption(expr string, opt CompileOption) (*Regexp, error) {
	return CompileContextWithOption(context.Background(), expr, opt)
}

// CompileContext is like Compile but accepts a context to allow cancellation.
func CompileContext(ctx context.Context, expr string) (*Regexp, error) {
	return CompileContextWithOption(ctx, expr, CompileOption{})
}

// CompileContextWithOption is like CompileContext but allows specifying options.
func CompileContextWithOption(ctx context.Context, expr string, opt CompileOption) (*Regexp, error) {
	if opt.MaxMemory <= 0 {
		opt.MaxMemory = ir.MaxDFAMemory
	}
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
	dfa, err := ir.NewDFAWithMemoryLimit(ctx, prog, opt.MaxMemory)
	if err != nil {
		return nil, err
	}
	var prefixState ir.StateID = dfa.MatchState()
	if prefixStr != "" {
		trans := dfa.Transitions()
		stride := dfa.Stride()
		for _, c := range []byte(prefixStr) {
			rawNext := trans[int(prefixState)*stride+int(c)]
			if rawNext == ir.InvalidState {
				prefixState = ir.InvalidState
				break
			}
			if rawNext < 0 {
				prefixState = rawNext & 0x7FFFFFFF
			} else {
				prefixState = rawNext
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

	// Phase 2: Simple Tagged DFA Rescan (O(n) for Priority 0 matches).
	// Priority 0 means the match started at the beginning of the pattern
	// within the matched range [start, end].
	regs := make([]int, (re.numSubexp+1)*2)
	for i := range regs {
		regs[i] = -1
	}
	regs[0] = start
	regs[1] = end

	// If the inner priority was not 0, we use NFA fallback.
	// targetPriority % 1000000 gives the NFA-style priority within the pattern.
	if (targetPriority % 1000000) > 0 {
		nregs := ir.NFAMatch(re.prog, re.dfa.TrieRoots(), b, start, end, re.numSubexp)
		if nregs != nil {
			nregs[0], nregs[1] = start, end
			return nregs
		}
	}

	dfa := re.dfa
	trans := dfa.Transitions()
	tagUpdateIndices := dfa.TagUpdateIndices()
	tagUpdates := dfa.TagUpdates()
	stride := dfa.Stride()
	matchState := dfa.MatchState()

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
	state := re.applyContextToState(dfa, matchState, ctx, func(t uint64) {
		recordTags(t, start)
	})

	currState := state
	for i := start; i < end; i++ {
		idx := int(currState)*stride + int(b[i])
		rawNext := trans[idx]
		if rawNext < 0 && rawNext != ir.InvalidState {
			update := tagUpdates[tagUpdateIndices[idx]]
			recordTags(update.PreTags, i)
			recordTags(update.PostTags, i+1)
			next := rawNext & 0x7FFFFFFF
			currState = re.applyContextToState(dfa, next, ir.CalculateContext(b, i+1), func(t uint64) {
				recordTags(t, i+1)
			})
		} else {
			if rawNext == ir.InvalidState {
				currState = ir.InvalidState
				break
			}
			currState = re.applyContextToState(dfa, rawNext, ir.CalculateContext(b, i+1), func(t uint64) {
				recordTags(t, i+1)
			})
		}
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
	tagUpdateIndices := d.TagUpdateIndices()
	tagUpdates := d.TagUpdates()
	stride := d.Stride()

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
					rawNext := trans[idx]
					if rawNext != ir.InvalidState && rawNext != state {
						if rawNext < 0 {
							if record != nil {
								update := tagUpdates[tagUpdateIndices[idx]]
								record(update.PreTags)
								record(update.PostTags)
							}
							state = rawNext & 0x7FFFFFFF
						} else {
							state = rawNext
						}
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

type loopTrait interface {
	HasAnchors() bool
}

type fastLoopTrait struct{}

func (fastLoopTrait) HasAnchors() bool { return false }

type extendedLoopTrait struct{}

func (extendedLoopTrait) HasAnchors() bool { return true }

func execLoop[T loopTrait](re *Regexp, b []byte) (int, int, int) {
	var _ T
	dfa := re.dfa
	trans := dfa.Transitions()
	tagUpdateIndices := dfa.TagUpdateIndices()
	tagUpdates := dfa.TagUpdates()
	stride := dfa.Stride()
	accepting := dfa.Accepting()

	numStates := dfa.NumStates()
	numBytes := len(b)
	lb := b[:numBytes] // BCE hint

	bestStart, bestEnd, bestPriority := -1, -1, 1<<30-1
	currentPriority := 0

	state := dfa.SearchState()
	if re.anchorStart {
		state = dfa.MatchState()
	}

	for i := 0; i <= numBytes; i++ {
		// 1. Check for match at position i
		ctx := ir.CalculateContext(lb, i)
		s := re.applyContextToState(dfa, state, ctx, nil)
		if s != ir.InvalidState {
			idx := int(s)
			if idx >= 0 && idx < len(accepting) {
				if accepting[idx] {
					p := currentPriority + dfa.MatchPriority(s)
					if p <= bestPriority {
						bestPriority = p
						bestEnd = i
						bestStart = p / ir.SearchRestartPenalty
					}
				}
			}
		} else {
			if re.anchorStart {
				break
			}
		}

		// 2. Transition to next byte
		if i < numBytes {
			s = re.applyContextToState(dfa, state, ctx, nil)
			sidx := int(s)
			if i >= 0 && i < len(lb) {
				bval := int(lb[i])
				if sidx >= 0 && sidx < numStates && bval >= 0 && bval < stride {
					off := sidx*stride + bval
					if off >= 0 && off < len(trans) {
						rawNext := trans[off]
						if rawNext < 0 && rawNext != ir.InvalidState {
							state = rawNext & 0x7FFFFFFF
							currentPriority += int(tagUpdates[tagUpdateIndices[off]].Priority)
						} else {
							state = rawNext
						}
					} else {
						state = ir.InvalidState
					}
				} else {
					state = ir.InvalidState
				}
			} else {
				state = ir.InvalidState
			}

			if state == ir.InvalidState {
				if re.anchorStart {
					break
				}
				// Restart search from next byte
				state = dfa.SearchState()
				currentPriority += ir.SearchRestartPenalty
			}
		}
	}

	return bestStart, bestEnd, bestPriority
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
