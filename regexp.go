package regexp

import (
	"context"
	"github.com/kamichidu/go-regexp-re/internal/ir"
	"github.com/kamichidu/go-regexp-re/syntax"
	"unicode/utf8"
)

type Regexp struct {
	expr             string
	numSubexp        int
	prefix           []byte
	complete         bool
	anchorStart      bool
	hasAnchors       bool
	prog             *syntax.Prog
	dfa              *ir.DFA
	literalMatcher   *ir.LiteralMatcher
	subexpNames      []string
	strategy         matchStrategy
	searchState      uint32
	matchState       uint32
	uIndices         []uint32
	uPrioDeltas      []int32
	searchWarp       ir.CCWarpInfo
	mapAnchors       []ir.AnchorInfo
	primaryAnchor    *ir.AnchorInfo
	searchAny        []byte
	searchMask       [4]uint64
	lineBounded      bool
	primaryAugmented *ir.AugmentedPattern
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
		for i := range res.mapAnchors {
			if res.lineBounded && res.mapAnchors[i].Distance > 0 {
				res.mapAnchors[i].Class.IncludeNL = true
			}
		}
		if len(res.mapAnchors) > 0 {
			// 1. Calculate searchAny from ALL anchors in the covering set
			var buf []byte
			seen := make(map[byte]bool)
			allCovered := true
			for _, a := range res.mapAnchors {
				if !a.HasClass {
					if len(a.Anchor) > 0 {
						b := a.Anchor[0]
						if !seen[b] {
							buf = append(buf, b)
							seen[b] = true
						}
					} else {
						allCovered = false
						break
					}
				} else {
					switch a.Class.Kernel {
					case ir.CCWarpEqual:
						b := byte(a.Class.V0)
						if !seen[b] {
							buf = append(buf, b)
							seen[b] = true
						}
					case ir.CCWarpSingleRange:
						low, high := byte(a.Class.V0), byte(a.Class.V1)
						if high-low < 255 {
							for b := low; b <= high; b++ {
								if !seen[b] {
									buf = append(buf, b)
									seen[b] = true
								}
							}
						} else {
							allCovered = false
						}
					case ir.CCWarpEqualSet:
						if len(a.Class.Extra) < 256 {
							for _, v := range a.Class.Extra {
								b := byte(v)
								if !seen[b] {
									buf = append(buf, b)
									seen[b] = true
								}
							}
						} else {
							allCovered = false
						}
					default:
						allCovered = false
					}
					if !allCovered {
						break
					}
				}
			}
			if allCovered && len(buf) > 0 {
				if res.lineBounded {
					if !seen['\n'] {
						buf = append(buf, '\n')
						seen['\n'] = true
					}
				}
				res.searchAny = buf
				for _, b := range buf {
					res.searchMask[b/64] |= 1 << (b % 64)
				}
			}

			// 2. Select the best Mandatory anchor as primaryAnchor.
			// If no anchor is mandatory for all paths AND fixed-distance,
			// we MUST NOT use a single primaryAnchor for aggressive jumping.
			bestIdx := -1
			bestScore := -1
			for i := range res.mapAnchors {
				if res.mapAnchors[i].Mandatory && res.mapAnchors[i].IsFixed {
					score := res.mapAnchors[i].Score()
					if score > bestScore {
						bestScore = score
						bestIdx = i
					}
				}
			}
			if bestIdx >= 0 {
				res.primaryAnchor = &res.mapAnchors[bestIdx]
			}

			// 3. Select the best Augmented pattern as primaryAugmented.
		outer:
			for i := range res.mapAnchors {
				for j := range res.mapAnchors[i].Augmented {
					aug := &res.mapAnchors[i].Augmented[j]
					if !aug.IsStart && !aug.IsEnd {
						res.primaryAugmented = aug
						break outer
					}
				}
			}
		}
	}

	if opts.forceStrategy != strategyNone {
		res.strategy = opts.forceStrategy
	} else if res.literalMatcher != nil {
		res.strategy = strategyLiteral
	} else if res.hasAnchors || res.numSubexp > 0 {
		res.strategy = strategyExtended
	} else if res.dfa != nil {
		res.strategy = strategyFast
	} else {
		res.strategy = strategyNone
	}

	return res, nil
}

func calculateLiteralPrefix(re *syntax.Regexp) (string, bool) {
	if re == nil {
		return "", false
	}
	switch re.Op {
	case syntax.OpLiteral:
		var buf []byte
		for _, r := range re.Rune {
			var b [utf8.UTFMax]byte
			n := utf8.EncodeRune(b[:], r)
			buf = append(buf, b[:n]...)
		}
		return string(buf), true
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
			if i == 0 && (sub.Op == syntax.OpBeginText || sub.Op == syntax.OpBeginLine) {
				continue
			}
		}
		return prefix, true
	}
	return "", false
}

func hasAnchors(prog *syntax.Prog) bool {
	for _, inst := range prog.Inst {
		if inst.Op == syntax.InstEmptyWidth {
			return true
		}
	}
	return false
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

// UnsupportedError represents a regular expression pattern that is not
// supported by the current DFA-based engine.
type UnsupportedError = syntax.UnsupportedError

func (re *Regexp) LiteralPrefix() (prefix string, complete bool) {
	return string(re.prefix), re.complete
}
