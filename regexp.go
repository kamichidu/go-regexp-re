package regexp

import (
	"context"
	"unsafe"

	"github.com/kamichidu/go-regexp-re/internal/ir"
	"github.com/kamichidu/go-regexp-re/syntax"
)

// UnsupportedError represents a valid regular expression pattern that is not
// currently supported by the DFA engine due to structural limitations.
type UnsupportedError = syntax.UnsupportedError

type Regexp struct {
	expr           string
	numSubexp      int
	prefix         []byte
	complete       bool
	anchorStart    bool
	hasAnchors     bool
	prog           *syntax.Prog
	dfa            *ir.DFA
	literalMatcher *ir.LiteralMatcher
	subexpNames    []string
	strategy       matchStrategy
	searchState    uint32
	matchState     uint32
	uIndices       []uint32
	uPrioDeltas    []int32
	searchWarp     ir.CCWarpInfo
	mapAnchors     []ir.AnchorInfo
}

type CompileOptions struct {
	MaxMemory     int
	forceStrategy matchStrategy // Internal use for testing (strategyFast or strategyExtended)
}

func Compile(expr string) (*Regexp, error) { return CompileContext(context.Background(), expr) }
func CompileWithOptions(expr string, opt CompileOptions) (*Regexp, error) {
	return CompileContextWithOptions(context.Background(), expr, opt)
}
func CompileContext(ctx context.Context, expr string) (*Regexp, error) {
	return CompileContextWithOptions(ctx, expr, CompileOptions{MaxMemory: ir.MaxDFAMemory})
}

func CompileContextWithOptions(ctx context.Context, expr string, opts CompileOptions) (*Regexp, error) {
	s, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return nil, err
	}
	numSubexp := s.MaxCap()
	subexpNames := s.CapNames()

	s = syntax.Simplify(s)
	s = syntax.Optimize(s)
	prog, err := syntax.Compile(s)
	if err != nil {
		return nil, err
	}

	var literalMatcher *ir.LiteralMatcher
	if opts.forceStrategy == strategyNone {
		literalMatcher = ir.AnalyzeLiteralPattern(s, numSubexp+1)
	}
	prefix, complete := calculateLiteralPrefix(s)

	anchorStart := false
	if s.Op == syntax.OpConcat && len(s.Sub) > 0 && s.Sub[0].Op == syntax.OpBeginText {
		anchorStart = true
	} else if s.Op == syntax.OpBeginText {
		anchorStart = true
	}

	var dfa *ir.DFA
	var searchState, matchState uint32
	var uIndices []uint32
	var uPrioDeltas []int32
	var searchWarp ir.CCWarpInfo

	if literalMatcher == nil {
		dfa, err = ir.NewDFAWithMemoryLimit(ctx, s, prog, opts.MaxMemory, true)
		if err != nil {
			return nil, err
		}
		acc := dfa.Accepting()
		searchState = uint32(dfa.SearchState())
		if acc[searchState&ir.StateIDMask] {
			searchState |= ir.AcceptingStateFlag
		}
		matchState = uint32(dfa.MatchState())
		if acc[matchState&ir.StateIDMask] {
			matchState |= ir.AcceptingStateFlag
		}

		uIndices = dfa.TagUpdateIndices()
		tagUpdates := dfa.TagUpdates()
		uPrioDeltas = make([]int32, len(tagUpdates))
		for i, update := range tagUpdates {
			uPrioDeltas[i] = update.BasePriority
		}
		searchWarp = dfa.SearchWarp()
	}

	res := &Regexp{
		expr:           expr,
		numSubexp:      numSubexp,
		prefix:         []byte(prefix),
		complete:       complete,
		anchorStart:    anchorStart,
		hasAnchors:     hasAnchors(prog),
		prog:           prog,
		dfa:            dfa,
		literalMatcher: literalMatcher,
		subexpNames:    subexpNames,
		searchState:    searchState,
		matchState:     matchState,
		uIndices:       uIndices,
		uPrioDeltas:    uPrioDeltas,
		searchWarp:     searchWarp,
	}

	if res.literalMatcher == nil && !ir.HasComplexAnchors(s) {
		anchors := ir.ExtractAnchors(s)
		for i := range anchors {
			if (anchors[i].HasBeginText || anchors[i].HasEndText) && (s.Flags&syntax.OneLine == 0) {
				continue
			}
			ir.ExtractConstraints(s, &anchors[i])
			res.mapAnchors = append(res.mapAnchors, anchors[i])
		}
		res.mapAnchors = ir.SelectBestAnchors(res.mapAnchors)
	}

	res.bindMatchStrategy()
	return res, nil
}

func calculateLiteralPrefix(re *syntax.Regexp) (string, bool) {
	switch re.Op {
	default:
		return "", false
	case syntax.OpLiteral:
		if re.Flags&syntax.FoldCase != 0 {
			return "", false
		}
		return string(re.Rune), true
	case syntax.OpCharClass:
		if (re.Flags&syntax.FoldCase == 0) && len(re.Rune) == 2 && re.Rune[0] == re.Rune[1] {
			return string(re.Rune[0]), true
		}
		return "", false
	case syntax.OpCapture:
		return calculateLiteralPrefix(re.Sub[0])
	case syntax.OpConcat:
		var prefix string
		for i, sub := range re.Sub {
			p, c := calculateLiteralPrefix(sub)
			prefix += p
			if !c {
				return prefix, false
			}
			if i == len(re.Sub)-1 {
				return prefix, true
			}
		}
		return prefix, true
	}
}

func hasAnchors(prog *syntax.Prog) bool {
	for _, inst := range prog.Inst {
		if inst.Op == syntax.InstEmptyWidth {
			return true
		}
	}
	return false
}

func (re *Regexp) Match(b []byte) bool {
	start, _, _ := re.findIndexAt(b, 0, len(b))
	return start >= 0
}

func (re *Regexp) MatchString(s string) bool {
	b := unsafe.Slice(unsafe.StringData(s), len(s))
	start, _, _ := re.findIndexAt(b, 0, len(b))
	return start >= 0
}

func (re *Regexp) FindSubmatchIndex(b []byte) []int {
	return re.findSubmatchIndexAt(b, 0, len(b))
}

func (re *Regexp) findSubmatchIndexAt(b []byte, pos int, totalBytes int) []int {
	in := ir.Input{
		B:          b,
		AbsPos:     pos,
		TotalBytes: totalBytes,
		SearchEnd:  len(b),
	}

	if re.strategy == strategyLiteral {
		regs := make([]int, (re.numSubexp+1)*2)
		for i := range regs {
			regs[i] = -1
		}
		if !re.literalMatcher.FindSubmatchIndexInto(in, regs) {
			return nil
		}
		for i := range regs {
			if regs[i] >= 0 {
				regs[i] += pos
			}
		}
		return regs
	}

	mc := matchContextPool.Get().(*matchContext)
	defer matchContextPool.Put(mc)
	mc.prepare(len(b), re.numSubexp, pos)

	start, end, prio := re.findSubmatchIndexInternal(b, mc, mc.regs)
	if start < 0 {
		return nil
	}

	regs := mc.regs
	// submatch results are relative to in.B, so add pos for absolute coordinates
	regs[0], regs[1] = start+pos, end+pos
	if re.numSubexp > 0 {
		re.sparseTDFA_PathSelection(mc, b, start, end, prio)
		re.sparseTDFA_Recap(mc, b, start, end, prio, regs)
	}

	res := make([]int, len(mc.regs))
	copy(res, mc.regs)
	return res
}

func (re *Regexp) FindStringSubmatchIndex(s string) []int {
	b := unsafe.Slice(unsafe.StringData(s), len(s))
	return re.findSubmatchIndexAt(b, 0, len(b))
}

func MustCompile(expr string) *Regexp {
	re, err := Compile(expr)
	if err != nil {
		panic(err)
	}
	return re
}

func (re *Regexp) String() string { return re.expr }

func (re *Regexp) LiteralPrefix() (prefix string, complete bool) {
	return string(re.prefix), re.complete
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
