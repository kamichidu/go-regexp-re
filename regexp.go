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
	primaryAnchor  *ir.AnchorInfo
	searchAny      string
	lineBounded    bool
}

type CompileOptions struct {
	MaxMemory     int
	forceStrategy matchStrategy // Internal use for testing
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
	if err := ir.CheckCompatibility(s); err != nil {
		return nil, err
	}
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
		lineBounded:    ir.IsLineBounded(s),
	}

	if res.literalMatcher == nil && !ir.HasComplexAnchors(s) {
		res.mapAnchors = ir.SelectBestAnchors(s)
		if len(res.mapAnchors) == 1 {
			res.primaryAnchor = &res.mapAnchors[0]
		} else if len(res.mapAnchors) > 1 {
			var buf []byte
			seen := make(map[byte]bool)
			for _, a := range res.mapAnchors {
				var b byte
				if !a.HasClass {
					if len(a.Anchor) > 0 {
						b = a.Anchor[0]
					}
				} else {
					if a.Class.Kernel == ir.CCWarpEqual {
						b = byte(a.Class.V0)
					}
				}
				if b != 0 && !seen[b] {
					buf = append(buf, b)
					seen[b] = true
				}
			}
			res.searchAny = string(buf)
		}
	}

	if opts.forceStrategy != strategyNone {
		res.strategy = opts.forceStrategy
	} else {
		res.bindMatchStrategy()
	}
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
	start, _, _ := re.findIndexAt(b, 0, len(b), b)
	return start >= 0
}

func (re *Regexp) MatchString(s string) bool {
	b := unsafe.Slice(unsafe.StringData(s), len(s))
	start, _, _ := re.findIndexAt(b, 0, len(b), b)
	return start >= 0
}

func (re *Regexp) FindSubmatchIndex(b []byte) []int {
	return re.findSubmatchIndexAt(b, 0, len(b), b)
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
