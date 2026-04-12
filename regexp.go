package regexp

import (
	"bytes"
	"context"
	"fmt"
	"math/bits"
	"unicode"
	"unsafe"

	"github.com/kamichidu/go-regexp-re/internal/ir"
	"github.com/kamichidu/go-regexp-re/syntax"
)

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
	match       func([]byte) (int, int, int)
	subexpNames []string
}

type CompileOption struct{ MaxMemory int }

func Compile(expr string) (*Regexp, error) { return CompileContext(context.Background(), expr) }
func CompileWithOption(expr string, opt CompileOption) (*Regexp, error) {
	return CompileContextWithOption(context.Background(), expr, opt)
}
func CompileContext(ctx context.Context, expr string) (*Regexp, error) {
	return CompileContextWithOption(ctx, expr, CompileOption{})
}

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
	anchorStart, anchorEnd := false, false
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
	prefixState := dfa.MatchState()
	if prefixStr != "" {
		trans, stride := dfa.Transitions(), dfa.Stride()
		for _, c := range []byte(prefixStr) {
			rawNext := trans[int(prefixState)*stride+int(c)]
			if rawNext == ir.InvalidState {
				prefixState = ir.InvalidState
				break
			}
			prefixState = rawNext & 0x7FFFFFFF
		}
	}
	res := &Regexp{expr: expr, numSubexp: numSubexp, prefix: []byte(prefixStr), prefixState: prefixState, complete: complete, anchorStart: anchorStart, anchorEnd: anchorEnd, prog: prog, dfa: dfa, subexpNames: subexpNames}
	if complete && numSubexp == 0 {
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
			if i := bytes.Index(b, res.prefix); i >= 0 {
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
	if re.dfa.IsBitParallel() && !re.hasNonGreedy() {
		re.match = func(b []byte) (int, int, int) { i, j, k, _ := bitParallelExecLoop(re, b); return i, j, k }
	} else if re.dfa.HasAnchors() {
		re.match = func(b []byte) (int, int, int) { i, j, k, _ := execLoop[extendedMatchTrait](re, b); return i, j, k }
	} else {
		re.match = func(b []byte) (int, int, int) { i, j, k, _ := execLoop[fastMatchTrait](re, b); return i, j, k }
	}
}

func (re *Regexp) hasNonGreedy() bool {
	// A non-greedy Alt in syntax.Prog usually has Arg < Out.
	for _, inst := range re.prog.Inst {
		if (inst.Op == syntax.InstAlt || inst.Op == syntax.InstAltMatch) && inst.Arg < inst.Out {
			return true
		}
	}
	return false
}

func (re *Regexp) Match(b []byte) bool {
	i, _, _ := re.match(b)
	return i >= 0
}
func (re *Regexp) MatchString(s string) bool {
	b := unsafe.Slice(unsafe.StringData(s), len(s))
	return re.Match(b)
}
func (re *Regexp) NumSubexp() int                { return re.numSubexp }
func (re *Regexp) LiteralPrefix() (string, bool) { return string(re.prefix), re.complete }

func (re *Regexp) FindSubmatchIndex(b []byte) []int {
	var start, end, targetPriority int
	var matchTags uint64
	if re.dfa.IsBitParallel() && !re.hasNonGreedy() {
		start, end, targetPriority, matchTags = bitParallelExecLoop(re, b)
	} else if re.dfa.HasAnchors() {
		start, end, targetPriority, matchTags = execLoop[extendedCaptureTrait](re, b)
	} else {
		start, end, targetPriority, matchTags = execLoop[fastCaptureTrait](re, b)
	}
	if start < 0 {
		return nil
	}
	return re.extractSubmatches(b, start, end, targetPriority, matchTags)
}

func (re *Regexp) extractSubmatches(b []byte, start, end, targetPriority int, matchTags uint64) []int {
	regs := make([]int, (re.numSubexp+1)*2)
	for i := range regs {
		regs[i] = -1
	}
	regs[0], regs[1] = start, end
	if start < 0 || end < 0 {
		return nil
	}

	if re.numSubexp == 0 {
		return regs
	}

	type thread struct {
		id   uint32
		regs []int
	}
	curr := make([]thread, 0, 16)
	next := make([]thread, 0, 16)

	addThread := func(q *[]thread, instID uint32, r []int, pos int) {
		for _, th := range *q {
			if th.id == instID {
				return
			}
		}

		visited := make(map[uint32]bool)
		var dfs func(id uint32, currentRegs []int)
		dfs = func(id uint32, currentRegs []int) {
			if visited[id] {
				return
			}
			visited[id] = true
			for _, th := range *q {
				if th.id == id {
					return
				}
			}
			inst := re.prog.Inst[id]
			switch inst.Op {
			case syntax.InstCapture:
				newRegs := make([]int, len(currentRegs))
				copy(newRegs, currentRegs)
				if int(inst.Arg) < len(newRegs) {
					newRegs[inst.Arg] = pos
				}
				dfs(inst.Out, newRegs)
			case syntax.InstNop:
				dfs(inst.Out, currentRegs)
			case syntax.InstAlt, syntax.InstAltMatch:
				dfs(inst.Out, currentRegs)
				dfs(inst.Arg, currentRegs)
			case syntax.InstEmptyWidth:
				if syntax.EmptyOp(inst.Arg)&ir.CalculateContext(b, pos) == syntax.EmptyOp(inst.Arg) {
					dfs(inst.Out, currentRegs)
				}
			default:
				*q = append(*q, thread{id, currentRegs})
			}
		}
		dfs(instID, r)
	}

	initialRegs := make([]int, len(regs))
	for i := range initialRegs {
		initialRegs[i] = -1
	}
	initialRegs[0] = start
	addThread(&curr, uint32(re.prog.Start), initialRegs, start)

	for i := start; i < end; i++ {
		next = next[:0]
		for _, th := range curr {
			inst := re.prog.Inst[th.id]
			match := false
			switch inst.Op {
			case syntax.InstRune, syntax.InstRune1:
				fold := (inst.Arg&uint32(syntax.FoldCase) != 0)
				r := rune(b[i])
				if inst.Op == syntax.InstRune1 {
					r0 := inst.Rune[0]
					if fold {
						match = (r == r0 || r == unicode.SimpleFold(r0))
					} else {
						match = (r == r0)
					}
				} else {
					if fold {
						for _, rRange := range inst.Rune {
							if r == rRange || (r >= rRange && r <= rRange) {
								match = true
								break
							}
						}
						if !match {
							for j := 0; j < len(inst.Rune); j += 2 {
								if r >= inst.Rune[j] && r <= inst.Rune[j+1] {
									match = true
									break
								}
							}
						}
					} else {
						for j := 0; j < len(inst.Rune); j += 2 {
							if rune(b[i]) >= inst.Rune[j] && rune(b[i]) <= inst.Rune[j+1] {
								match = true
								break
							}
						}
					}
				}
			case syntax.InstRuneAny:
				match = true
			case syntax.InstRuneAnyNotNL:
				match = b[i] != '\n'
			}
			if match {
				addThread(&next, inst.Out, th.regs, i+1)
			}
		}
		curr, next = next, curr
	}

	next = next[:0]
	for _, th := range curr {
		addThread(&next, th.id, th.regs, end)
	}

	for _, th := range next {
		if re.prog.Inst[th.id].Op == syntax.InstMatch {
			copy(regs, th.regs)
			regs[1] = end
			return regs
		}
	}
	return regs
}

func bitParallelExecLoop(re *Regexp, b []byte) (int, int, int, uint64) {
	dfa := re.dfa
	masks := dfa.BPCharMasks()
	matchMask := dfa.BPMatchMask()
	epsilons := dfa.BPEpsilon()
	numBytes := len(b)

	bestStart, bestEnd, bestPriority := -1, -1, 1<<30-1

	var bpStarts [64]int
	for i := range bpStarts {
		bpStarts[i] = -1
	}

	state := uint64(0)
	for i := 0; i <= numBytes; i++ {
		// New thread starting at i
		if !re.anchorStart || i == 0 {
			startBits := epsilons[re.prog.Start]
			newBits := startBits & ^state
			state |= newBits
			for newBits != 0 {
				bit := bits.TrailingZeros64(newBits)
				bpStarts[bit] = i
				newBits &= ^(uint64(1) << bit)
			}
		}

		if (state & matchMask) != 0 {
			matchingBits := state & matchMask
			winningBit := bits.TrailingZeros64(matchingBits)
			currentStart := bpStarts[winningBit]

			// Leftmost-first + Greedy
			if bestStart == -1 || currentStart < bestStart || (currentStart == bestStart && winningBit < bestPriority) || (currentStart == bestStart && winningBit == bestPriority && i >= bestEnd) {
				bestPriority, bestEnd, bestStart = winningBit, i, currentStart
			}
		}
		if i == numBytes {
			break
		}

		var nextState uint64
		var nextStarts [64]int
		for j := range nextStarts {
			nextStarts[j] = -1
		}

		tempState := state
		for tempState != 0 {
			bit := bits.TrailingZeros64(tempState)
			if (masks[b[i]] & (1 << bit)) != 0 {
				inst := re.prog.Inst[bit]
				outBits := epsilons[inst.Out]
				nextState |= outBits

				for outBits != 0 {
					outBit := bits.TrailingZeros64(outBits)
					if nextStarts[outBit] == -1 || bpStarts[bit] < nextStarts[outBit] {
						nextStarts[outBit] = bpStarts[bit]
					}
					outBits &= ^(uint64(1) << outBit)
				}
			}
			tempState &= ^(uint64(1) << bit)
		}
		state = nextState
		bpStarts = nextStarts
		if state == 0 && re.anchorStart {
			break
		}
	}
	return bestStart, bestEnd, bestPriority, 0
}

func (re *Regexp) applyContextToState(d *ir.DFA, state ir.StateID, context syntax.EmptyOp, pos int, currentPrio int, targetPrio int, regs []int) ir.StateID {
	if state == ir.InvalidState || context == 0 || d.Stride() <= 256 {
		return state
	}
	trans, _, _, stride := d.Transitions(), d.TagUpdateIndices(), d.TagUpdates(), d.Stride()
	virtualBytes := [6]int{ir.VirtualBeginLine, ir.VirtualEndLine, ir.VirtualBeginText, ir.VirtualEndText, ir.VirtualWordBoundary, ir.VirtualNoWordBoundary}

	for iter := 0; iter < 6; iter++ {
		changed := false
		for bit := 0; bit < 6; bit++ {
			if (context & (1 << bit)) != 0 {
				idx := int(state)*stride + virtualBytes[bit]
				if idx < len(trans) {
					rawNext := trans[idx]
					nextID := rawNext & 0x7FFFFFFF
					if rawNext != ir.InvalidState && nextID != state {
						state = nextID
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

func MustCompile(expr string) *Regexp {
	re, err := Compile(expr)
	if err != nil {
		panic(`regexp: Compile(` + quote(expr) + `): ` + err.Error())
	}
	return re
}
func (re *Regexp) String() string { return re.expr }

type loopTrait interface {
	HasAnchors() bool
	IsCapture() bool
}
type fastMatchTrait struct{}

func (fastMatchTrait) HasAnchors() bool { return false }
func (fastMatchTrait) IsCapture() bool  { return false }

type fastCaptureTrait struct{}

func (fastCaptureTrait) HasAnchors() bool { return false }
func (fastCaptureTrait) IsCapture() bool  { return true }

type extendedMatchTrait struct{}

func (extendedMatchTrait) HasAnchors() bool { return true }
func (extendedMatchTrait) IsCapture() bool  { return false }

type extendedCaptureTrait struct{}

func (extendedCaptureTrait) HasAnchors() bool { return true }
func (extendedCaptureTrait) IsCapture() bool  { return true }

func execLoop[T loopTrait](re *Regexp, b []byte) (int, int, int, uint64) {
	var trait T
	dfa := re.dfa
	trans, tagUpdateIndices, tagUpdates, stride, accepting := dfa.Transitions(), dfa.TagUpdateIndices(), dfa.TagUpdates(), dfa.Stride(), dfa.Accepting()
	numStates, numBytes := dfa.NumStates(), len(b)
	lb := b[:numBytes]
	bestStart, bestEnd, bestPriority, bestMatchTags, currentPriority := -1, -1, 1<<30-1, uint64(0), 0
	state := dfa.SearchState()
	if re.anchorStart {
		state = dfa.MatchState()
	}
	hasAnchors := trait.HasAnchors()

	for i := 0; i <= numBytes; i++ {
		if hasAnchors {
			state = re.applyContextToState(dfa, state, ir.CalculateContext(lb, i), i, currentPriority, 1<<30-1, nil)
		}
		if state != ir.InvalidState {
			idx := int(state)
			if idx >= 0 && idx < len(accepting) && accepting[idx] {
				p := currentPriority + dfa.MatchPriority(state)
				if p <= bestPriority {
					bestPriority, bestEnd, bestMatchTags = p, i, dfa.MatchTags(state)
					bestStart = p / ir.SearchRestartPenalty
					if dfa.IsBestMatch(state) {
						return bestStart, bestEnd, bestPriority, bestMatchTags
					}
				}
			}
		} else if re.anchorStart {
			break
		}
		if i < numBytes {
			sidx := int(state)
			if sidx >= 0 && sidx < numStates && int(lb[i]) < stride {
				off := sidx*stride + int(lb[i])
				rawNext := trans[off]
				if rawNext != ir.InvalidState {
					state = rawNext & 0x7FFFFFFF
					if rawNext < 0 {
						update := tagUpdates[tagUpdateIndices[off]]
						currentPriority += int(update.BasePriority)
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
				state, currentPriority = dfa.SearchState(), currentPriority+ir.SearchRestartPenalty
			}
		}
	}
	return bestStart, bestEnd, bestPriority, bestMatchTags
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
		if start, end := indices[2*i], indices[2*i+1]; start >= 0 && end >= 0 {
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
		if start, end := indices[2*i], indices[2*i+1]; start >= 0 && end >= 0 {
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
