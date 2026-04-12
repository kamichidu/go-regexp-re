package regexp

import (
	"bytes"
	"context"
	"fmt"
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

type CompileOption struct { MaxMemory int }

func Compile(expr string) (*Regexp, error) { return CompileContext(context.Background(), expr) }
func CompileWithOption(expr string, opt CompileOption) (*Regexp, error) { return CompileContextWithOption(context.Background(), expr, opt) }
func CompileContext(ctx context.Context, expr string) (*Regexp, error) { return CompileContextWithOption(ctx, expr, CompileOption{}) }

func CompileContextWithOption(ctx context.Context, expr string, opt CompileOption) (*Regexp, error) {
	if opt.MaxMemory <= 0 { opt.MaxMemory = ir.MaxDFAMemory }
	re, err := syntax.Parse(expr, syntax.Perl); if err != nil { return nil, err }
	numSubexp := re.MaxCap(); subexpNames := make([]string, numSubexp+1); extractCapNames(re, subexpNames)
	anchorStart, anchorEnd := false, false
	if re.Op == syntax.OpConcat && len(re.Sub) > 0 {
		if re.Sub[0].Op == syntax.OpBeginText { anchorStart = true }
		if re.Sub[len(re.Sub)-1].Op == syntax.OpEndText { anchorEnd = true }
	} else if re.Op == syntax.OpBeginText { anchorStart = true
	} else if re.Op == syntax.OpEndText { anchorEnd = true }
	re = syntax.Simplify(re); re = syntax.Optimize(re); prefixStr, complete := syntax.Prefix(re)
	prog, err := syntax.Compile(re); if err != nil { return nil, err }
	dfa, err := ir.NewDFAWithMemoryLimit(ctx, prog, opt.MaxMemory); if err != nil { return nil, err }
	prefixState := dfa.MatchState()
	if prefixStr != "" {
		trans, stride := dfa.Transitions(), dfa.Stride()
		for _, c := range []byte(prefixStr) {
			rawNext := trans[int(prefixState)*stride+int(c)]
			if rawNext == ir.InvalidState { prefixState = ir.InvalidState; break }
			prefixState = rawNext & 0x7FFFFFFF
		}
	}
	res := &Regexp{expr: expr, numSubexp: numSubexp, prefix: []byte(prefixStr), prefixState: prefixState, complete: complete, anchorStart: anchorStart, anchorEnd: anchorEnd, prog: prog, dfa: dfa, subexpNames: subexpNames}
	if complete && numSubexp == 0 {
		res.match = func(b []byte) (int, int, int) {
			if res.anchorStart && res.anchorEnd { if bytes.Equal(b, res.prefix) { return 0, len(b), 0 }; return -1, -1, -1 }
			if res.anchorStart { if bytes.HasPrefix(b, res.prefix) { return 0, len(res.prefix), 0 }; return -1, -1, -1 }
			if res.anchorEnd { if bytes.HasSuffix(b, res.prefix) { return len(b) - len(res.prefix), len(b), 0 }; return -1, -1, -1 }
			if i := bytes.Index(b, res.prefix); i >= 0 { return i, i + len(res.prefix), 0 }
			return -1, -1, -1
		}
	} else { res.bindMatchLoop() }
	return res, nil
}

func (re *Regexp) bindMatchLoop() {
	if re.dfa.HasAnchors() { re.match = func(b []byte) (int, int, int) { return execLoop[extendedMatchTrait](re, b, nil) } } else { re.match = func(b []byte) (int, int, int) { return execLoop[fastMatchTrait](re, b, nil) } }
}

func (re *Regexp) Match(b []byte) bool { i, _, _ := re.match(b); return i >= 0 }
func (re *Regexp) MatchString(s string) bool { b := unsafe.Slice(unsafe.StringData(s), len(s)); return re.Match(b) }
func (re *Regexp) NumSubexp() int { return re.numSubexp }
func (re *Regexp) LiteralPrefix() (string, bool) { return string(re.prefix), re.complete }

func (re *Regexp) FindSubmatchIndex(b []byte) []int {
	var stack ir.CaptureStack; var start, end, targetPriority int
	if re.dfa.HasAnchors() { start, end, targetPriority = execLoop[extendedCaptureTrait](re, b, &stack) } else { start, end, targetPriority = execLoop[fastCaptureTrait](re, b, &stack) }
	if start < 0 { return nil }
	return re.extractSubmatches(b, start, end, targetPriority, &stack)
}

func (re *Regexp) extractSubmatches(b []byte, start, end, targetPriority int, stack *ir.CaptureStack) []int {
	regs := make([]int, (re.numSubexp+1)*2); for i := range regs { regs[i] = -1 }; regs[0], regs[1] = start, end
	var currState ir.StateID
	if re.dfa.HasAnchors() { currState = rescanLoop[extendedMatchTrait](re, b, start, end, regs) } else { currState = rescanLoop[fastMatchTrait](re, b, start, end, regs) }
	dfa := re.dfa
	if currState != ir.InvalidState && dfa.IsAccepting(currState) {
		tags := dfa.MatchTags(currState)
		for i := 0; i < 64; i++ { if (tags & (1 << i)) != 0 && i < len(regs) { regs[i] = end } }
	}
	return regs
}

func rescanLoop[T loopTrait](re *Regexp, b []byte, start, end int, regs []int) ir.StateID {
	var trait T; dfa := re.dfa
	trans, tagUpdateIndices, tagUpdates, stride, matchState := dfa.Transitions(), dfa.TagUpdateIndices(), dfa.TagUpdates(), dfa.Stride(), dfa.MatchState()
	recordTags := func(t uint64, pos int) {
		if t == 0 { return }
		for i := 0; i < 64; i++ { if (t & (1 << i)) != 0 && i < len(regs) { regs[i] = pos } }
	}
	recordTags(dfa.StartTags(), start)
	hasAnchors, state := trait.HasAnchors(), matchState
	if hasAnchors {
		state = re.applyContextToState(dfa, state, ir.CalculateContext(b, start), func(t uint64) { recordTags(t, start) })
	}
	currState := state
	for i := start; i < end; i++ {
		idx := int(currState)*stride + int(b[i]); rawNext := trans[idx]
		if rawNext != ir.InvalidState {
			targetID := rawNext & 0x7FFFFFFF
			if rawNext < 0 {
				update := tagUpdates[tagUpdateIndices[idx]]
				recordTags(update.StartTags, i)
				recordTags(update.EndTags, i+1)
			}
			if hasAnchors {
				currState = re.applyContextToState(dfa, targetID, ir.CalculateContext(b, i+1), func(t uint64) { recordTags(t, i+1) })
			} else { currState = targetID }
		} else { currState = ir.InvalidState; break }
	}
	return currState
}

func (re *Regexp) applyContextToState(d *ir.DFA, state ir.StateID, context syntax.EmptyOp, record func(uint64)) ir.StateID {
	if state == ir.InvalidState || context == 0 || d.Stride() <= 256 { return state }
	trans, tagUpdateIndices, tagUpdates, stride := d.Transitions(), d.TagUpdateIndices(), d.TagUpdates(), d.Stride()
	virtualBytes := [6]int{ir.VirtualBeginLine, ir.VirtualEndLine, ir.VirtualBeginText, ir.VirtualEndText, ir.VirtualWordBoundary, ir.VirtualNoWordBoundary}
	for {
		changed := false
		for bit := 0; bit < 6; bit++ {
			if (context & (1 << bit)) != 0 {
				idx := int(state)*stride + virtualBytes[bit]
				if idx < len(trans) {
					rawNext := trans[idx]
					if rawNext != ir.InvalidState && rawNext != state {
						if rawNext < 0 && record != nil {
							update := tagUpdates[tagUpdateIndices[idx]]
							record(update.StartTags | update.EndTags)
						}
						state = rawNext & 0x7FFFFFFF; changed = true
					}
				}
			}
		}
		if !changed { break }
	}
	return state
}

func MustCompile(expr string) *Regexp {
	re, err := Compile(expr); if err != nil { panic(`regexp: Compile(` + quote(expr) + `): ` + err.Error()) }; return re
}
func (re *Regexp) String() string { return re.expr }

type loopTrait interface { HasAnchors() bool; IsCapture() bool }
type fastMatchTrait struct{}; func (fastMatchTrait) HasAnchors() bool { return false }; func (fastMatchTrait) IsCapture() bool { return false }
type fastCaptureTrait struct{}; func (fastCaptureTrait) HasAnchors() bool { return false }; func (fastCaptureTrait) IsCapture() bool { return true }
type extendedMatchTrait struct{}; func (extendedMatchTrait) HasAnchors() bool { return true }; func (extendedMatchTrait) IsCapture() bool { return false }
type extendedCaptureTrait struct{}; func (extendedCaptureTrait) HasAnchors() bool { return true }; func (extendedCaptureTrait) IsCapture() bool { return true }

func execLoop[T loopTrait](re *Regexp, b []byte, stack *ir.CaptureStack) (int, int, int) {
	var trait T; dfa := re.dfa
	trans, tagUpdateIndices, tagUpdates, stride, accepting := dfa.Transitions(), dfa.TagUpdateIndices(), dfa.TagUpdates(), dfa.Stride(), dfa.Accepting()
	numStates, numBytes := dfa.NumStates(), len(b); lb := b[:numBytes]
	bestStart, bestEnd, bestPriority, currentPriority := -1, -1, 1<<30-1, 0
	state := dfa.SearchState(); if re.anchorStart { state = dfa.MatchState() }
	hasAnchors, isCapture := trait.HasAnchors(), trait.IsCapture()
	for i := 0; i <= numBytes; i++ {
		s := state
		if hasAnchors { s = re.applyContextToState(dfa, state, ir.CalculateContext(lb, i), func(t uint64) { if isCapture && t != 0 { stack.Push(t, i) } }) }
		if s != ir.InvalidState {
			idx := int(s); if idx >= 0 && idx < len(accepting) && accepting[idx] {
				p := currentPriority + dfa.MatchPriority(s)
				if p <= bestPriority {
					bestPriority, bestEnd = p, i; bestStart = p / ir.SearchRestartPenalty
					if isCapture { if tags := dfa.MatchTags(s); tags != 0 { stack.Push(tags, i) } }
				}
			}
		} else if re.anchorStart { break }
		if i < numBytes {
			if hasAnchors { s = re.applyContextToState(dfa, state, ir.CalculateContext(lb, i), func(t uint64) { if isCapture && t != 0 { stack.Push(t, i) } }) } else { s = state }
			sidx := int(s)
			if sidx >= 0 && sidx < numStates && int(lb[i]) < stride {
				off := sidx*stride + int(lb[i]); rawNext := trans[off]
				if rawNext != ir.InvalidState {
					state = rawNext & 0x7FFFFFFF
					if rawNext < 0 {
						update := tagUpdates[tagUpdateIndices[off]]; currentPriority += int(update.Priority)
						if isCapture {
							if update.StartTags != 0 { stack.Push(update.StartTags, i) }
							if update.EndTags != 0 { stack.Push(update.EndTags, i+1) }
						}
					}
				} else { state = ir.InvalidState }
			} else { state = ir.InvalidState }
			if state == ir.InvalidState {
				if re.anchorStart { break }
				state, currentPriority = dfa.SearchState(), currentPriority + ir.SearchRestartPenalty
				if isCapture { stack.Reset() }
			}
		}
	}
	return bestStart, bestEnd, bestPriority
}

func (re *Regexp) FindStringSubmatchIndex(s string) []int { b := unsafe.Slice(unsafe.StringData(s), len(s)); return re.FindSubmatchIndex(b) }
func (re *Regexp) FindSubmatch(b []byte) [][]byte {
	indices := re.FindSubmatchIndex(b); if indices == nil { return nil }
	result := make([][]byte, len(indices)/2)
	for i := range result { if start, end := indices[2*i], indices[2*i+1]; start >= 0 && end >= 0 { result[i] = b[start:end] } }
	return result
}
func (re *Regexp) FindStringSubmatch(s string) []string {
	indices := re.FindStringSubmatchIndex(s); if indices == nil { return nil }
	result := make([]string, len(indices)/2)
	for i := range result { if start, end := indices[2*i], indices[2*i+1]; start >= 0 && end >= 0 { result[i] = s[start:end] } }
	return result
}
func extractCapNames(re *syntax.Regexp, names []string) { if re.Op == syntax.OpCapture { if re.Cap < len(names) { names[re.Cap] = re.Name } }; for _, sub := range re.Sub { extractCapNames(sub, names) } }
func quote(s string) string { if len(s) <= 16 { return fmt.Sprintf("%q", s) }; return fmt.Sprintf("%q...", s[:16]) }
