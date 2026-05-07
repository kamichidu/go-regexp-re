package ir

import (
	"bytes"
	"encoding/binary"
	"github.com/kamichidu/go-regexp-re/syntax"
	"math/bits"
	"unicode"
	"unicode/utf8"
)

// AnchorType defines the type of anchor.
type AnchorType uint8

const (
	AnchorPrefix AnchorType = iota
	AnchorPivot
	AnchorSuffix
)

// Constraint defines a requirement on characters surrounding the anchor.
type Constraint struct {
	Offset   int  // Relative to anchor start
	Length   int  // Fixed length if > 0
	IsRepeat bool // If true, this is a variable length skip (Warp)
	Info     CCWarpInfo
}

// AugmentedPattern defines a search pattern that includes surrounding context.
type AugmentedPattern struct {
	Pattern []byte
	Offset  int  // Actual anchor starts at this offset in Pattern
	IsStart bool // Only search at the very beginning of the input
	IsEnd   bool // Only search at the very end of the input
}

// AnchorInfo holds information about a potential anchor in the pattern.
type AnchorInfo struct {
	Anchor           []byte
	Class            CCWarpInfo // If Anchor is empty, use this SWAR class anchor
	HasClass         bool
	Type             AnchorType
	Distance         int  // Minimum distance from the start of the match
	IsFixed          bool // True if Distance is the EXACT distance
	Mandatory        bool // True if this anchor must be present in every match
	Forward          []Constraint
	Backward         []Constraint
	HasConstraints   bool // True if Forward or Backward is not empty
	HasBeginText     bool // This anchor path is strictly anchored to ^
	HasBeginLine     bool // This anchor path is strictly anchored to line start (^ or \n)
	HasEndText       bool // This anchor path is strictly anchored to $
	MinDistToEnd     int  // Minimum distance from anchor start to $
	MaxDistToEnd     int  // Maximum distance from anchor start to $
	HasEndLine       bool // This anchor path is strictly anchored to line end ($ or \n)
	MinDistToLineEnd int  // Minimum distance from anchor start to line end
	MaxDistToLineEnd int  // Maximum distance from anchor start to line end
	Augmented        []AugmentedPattern
	SimpleBackward   []Constraint // Only fixed length, single byte constraints
	SkipGaze         bool         // If true, Phase 2 (Gaze) can be skipped
}

// ExtractAnchors traverses the AST and identifies all potential anchors.
func ExtractAnchors(re *syntax.Regexp) []AnchorInfo {
	if minLength(re) == 0 {
		return nil
	}

	flatRE := stripCaptures(re)
	if flatRE == nil {
		return nil
	}
	anchors := extractAnchors(flatRE, 0, true, true, true, false, false)

	// Suffix identification
	totalMin := minLength(re)
	if totalMin >= 0 {
		for i := range anchors {
			anchorLen := len(anchors[i].Anchor)
			if anchors[i].HasClass {
				anchorLen = 1
			}
			if anchors[i].Distance+anchorLen == totalMin {
				if anchors[i].Type != AnchorPrefix {
					anchors[i].Type = AnchorSuffix
				}
			}
		}
	}

	return anchors
}

func stripCaptures(re *syntax.Regexp) *syntax.Regexp {
	if re == nil {
		return nil
	}
	res := *re
	switch re.Op {
	case syntax.OpCapture:
		return stripCaptures(re.Sub[0])
	case syntax.OpConcat:
		var subs []*syntax.Regexp
		for _, sub := range re.Sub {
			s := stripCaptures(sub)
			if s == nil {
				continue
			}
			if s.Op == syntax.OpConcat {
				subs = append(subs, s.Sub...)
			} else {
				subs = append(subs, s)
			}
		}
		if len(subs) == 0 {
			return nil
		}
		if len(subs) > 1 {
			merged := []*syntax.Regexp{subs[0]}
			for i := 1; i < len(subs); i++ {
				last := merged[len(merged)-1]
				curr := subs[i]
				if last.Op == syntax.OpLiteral && curr.Op == syntax.OpLiteral && last.Flags == curr.Flags {
					newLit := *last
					newLit.Rune = append(append([]rune(nil), last.Rune...), curr.Rune...)
					merged[len(merged)-1] = &newLit
				} else {
					merged = append(merged, curr)
				}
			}
			res.Sub = merged
		} else {
			res.Sub = subs
		}
	case syntax.OpAlternate:
		var subs []*syntax.Regexp
		for _, sub := range re.Sub {
			s := stripCaptures(sub)
			if s != nil {
				subs = append(subs, s)
			}
		}
		if len(subs) == 0 {
			return nil
		}
		res.Sub = subs
	case syntax.OpRepeat, syntax.OpQuest, syntax.OpPlus, syntax.OpStar:
		s := stripCaptures(re.Sub[0])
		if s == nil {
			return nil
		}
		res.Sub = []*syntax.Regexp{s}
	}
	return &res
}

// extractAnchors identifies mandatory anchors.
func extractAnchors(re *syntax.Regexp, offset int, mandatory bool, atStart bool, atEnd bool, hasBeginText bool, hasBeginLine bool) []AnchorInfo {
	if re == nil {
		return nil
	}

	var anchors []AnchorInfo

	switch re.Op {
	case syntax.OpBeginText, syntax.OpBeginLine, syntax.OpEndText, syntax.OpEndLine:
		// Do nothing
	case syntax.OpLiteral:
		if re.Flags&syntax.FoldCase == 0 {
			var buf []byte
			for _, r := range re.Rune {
				var b [utf8.UTFMax]byte
				n := utf8.EncodeRune(b[:], r)
				buf = append(buf, b[:n]...)
			}
			if len(buf) > 0 {
				anchors = append(anchors, AnchorInfo{
					Anchor:       buf,
					Type:         AnchorPivot,
					Distance:     offset,
					IsFixed:      true,
					Mandatory:    mandatory,
					HasBeginText: hasBeginText,
					HasBeginLine: hasBeginLine,
				})
			}
		}
	case syntax.OpCharClass:
		if re.Flags&syntax.FoldCase == 0 {
			if len(re.Rune) == 2 && re.Rune[0] == re.Rune[1] {
				var b [utf8.UTFMax]byte
				n := utf8.EncodeRune(b[:], re.Rune[0])
				anchors = append(anchors, AnchorInfo{
					Anchor:       b[:n],
					Type:         AnchorPivot,
					Distance:     offset,
					IsFixed:      true,
					Mandatory:    mandatory,
					HasBeginText: hasBeginText,
					HasBeginLine: hasBeginLine,
				})
			} else if info, ok := toCCWarp(re); ok {
				anchors = append(anchors, AnchorInfo{
					Class:        info,
					HasClass:     true,
					Type:         AnchorPivot,
					Distance:     offset,
					IsFixed:      true,
					Mandatory:    mandatory,
					HasBeginText: hasBeginText,
					HasBeginLine: hasBeginLine,
				})
			}
		}
	case syntax.OpRepeat, syntax.OpQuest, syntax.OpStar, syntax.OpPlus:
		m := false
		if re.Op == syntax.OpPlus || (re.Op == syntax.OpRepeat && re.Min > 0) {
			m = mandatory
		}
		anchors = append(anchors, extractAnchors(re.Sub[0], offset, m, atStart, atEnd, hasBeginText, hasBeginLine)...)
	case syntax.OpCapture:
		anchors = append(anchors, extractAnchors(re.Sub[0], offset, mandatory, atStart, atEnd, hasBeginText, hasBeginLine)...)
	case syntax.OpConcat:
		currentOffset := offset
		currentAtStart := atStart
		currentAtEnd := atEnd
		currentHasBeginText := hasBeginText
		currentHasBeginLine := hasBeginLine
		currentIsFixed := true
		for i, sub := range re.Sub {
			subAnchors := extractAnchors(sub, currentOffset, mandatory, currentAtStart, currentAtEnd, currentHasBeginText, currentHasBeginLine)
			if i == 0 && offset == 0 {
				for j := range subAnchors {
					if subAnchors[j].Distance == 0 {
						subAnchors[j].Type = AnchorPrefix
					}
				}
			}
			for j := range subAnchors {
				subAnchors[j].IsFixed = subAnchors[j].IsFixed && currentIsFixed
			}
			anchors = append(anchors, subAnchors...)

			if sub.Op == syntax.OpBeginText {
				currentHasBeginText = true
			}
			if sub.Op == syntax.OpBeginLine {
				currentHasBeginLine = true
			}

			d := minLength(sub)
			if d > 0 {
				currentHasBeginText = false
				currentHasBeginLine = false
			}

			maxD := maxLength(sub)
			if d != maxD {
				currentIsFixed = false
			}

			if d > 0 {
				currentAtStart = false
			}
			if d >= 0 {
				currentOffset += d
			} else {
				currentOffset = 1000000
			}
		}
	case syntax.OpAlternate:
		for _, sub := range re.Sub {
			anchors = append(anchors, extractAnchors(sub, offset, false, atStart, atEnd, hasBeginText, hasBeginLine)...)
		}
	}

	return anchors
}

func maxLength(re *syntax.Regexp) int {
	if re == nil {
		return 0
	}
	switch re.Op {
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return 0
	case syntax.OpLiteral:
		n := 0
		for _, r := range re.Rune {
			n += utf8.RuneLen(r)
		}
		return n
	case syntax.OpCharClass:
		return 1
	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		return 1
	case syntax.OpCapture:
		return maxLength(re.Sub[0])
	case syntax.OpConcat:
		total := 0
		for _, sub := range re.Sub {
			d := maxLength(sub)
			if d < 0 {
				return -1
			}
			total += d
		}
		return total
	case syntax.OpQuest:
		return maxLength(re.Sub[0])
	case syntax.OpStar, syntax.OpPlus:
		return -1 // Infinite
	case syntax.OpRepeat:
		if re.Max == -1 {
			return -1
		}
		d := maxLength(re.Sub[0])
		if d < 0 {
			return -1
		}
		return d * re.Max
	case syntax.OpAlternate:
		max := 0
		for _, sub := range re.Sub {
			d := maxLength(sub)
			if d < 0 {
				return -1
			}
			if d > max {
				max = d
			}
		}
		return max
	}
	return -1
}

func minLength(re *syntax.Regexp) int {
	if re == nil {
		return 0
	}
	switch re.Op {
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return 0
	case syntax.OpLiteral:
		n := 0
		for _, r := range re.Rune {
			n += utf8.RuneLen(r)
		}
		return n
	case syntax.OpCharClass:
		if len(re.Rune) > 0 {
			return 1
		}
		return 0
	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		return 1
	case syntax.OpCapture:
		return minLength(re.Sub[0])
	case syntax.OpConcat:
		total := 0
		for _, sub := range re.Sub {
			d := minLength(sub)
			if d < 0 {
				return -1
			}
			total += d
		}
		return total
	case syntax.OpQuest, syntax.OpStar:
		return 0
	case syntax.OpPlus:
		return minLength(re.Sub[0])
	case syntax.OpRepeat:
		d := minLength(re.Sub[0])
		if d < 0 {
			return -1
		}
		return d * re.Min
	case syntax.OpAlternate:
		min := -1
		for _, sub := range re.Sub {
			d := minLength(sub)
			if d < 0 {
				return -1
			}
			if min < 0 || d < min {
				min = d
			}
		}
		return min
	}
	return -1
}

func ExtractConstraints(re *syntax.Regexp, anchor *AnchorInfo) {
	flatRE := stripCaptures(re)
	extractConstraints(flatRE, anchor)
	if len(anchor.Backward) > 0 || len(anchor.Forward) > 0 {
		anchor.HasConstraints = true
	}

	minD, maxD, hasEnd := extractDistToEnd(flatRE, anchor)
	if hasEnd {
		anchor.HasEndText = true
		anchor.MinDistToEnd = minD
		anchor.MaxDistToEnd = maxD
	}

	minL, maxL, hasLineEnd := extractDistToLineEnd(flatRE, anchor)
	if hasLineEnd {
		anchor.HasEndLine = true
		anchor.MinDistToLineEnd = minL
		anchor.MaxDistToLineEnd = maxL
	}

	if !anchor.HasClass && len(anchor.Anchor) > 0 {
		if anchor.HasBeginText && anchor.Distance == 0 {
			anchor.Augmented = append(anchor.Augmented, AugmentedPattern{Pattern: anchor.Anchor, Offset: 0, IsStart: true})
		}
		if anchor.HasEndText && anchor.MaxDistToEnd == 0 {
			anchor.Augmented = append(anchor.Augmented, AugmentedPattern{Pattern: anchor.Anchor, Offset: 0, IsEnd: true})
		}
		if anchor.HasBeginLine && anchor.Distance == 0 && (re.Flags&syntax.OneLine == 0) {
			p := append([]byte{'\n'}, anchor.Anchor...)
			anchor.Augmented = append(anchor.Augmented, AugmentedPattern{Pattern: p, Offset: 1})
			anchor.Augmented = append(anchor.Augmented, AugmentedPattern{Pattern: anchor.Anchor, Offset: 0, IsStart: true})
		}
		if anchor.HasEndLine && anchor.MaxDistToLineEnd == 0 && (re.Flags&syntax.OneLine == 0) {
			p := append(append([]byte(nil), anchor.Anchor...), '\n')
			anchor.Augmented = append(anchor.Augmented, AugmentedPattern{Pattern: p, Offset: 0})
			anchor.Augmented = append(anchor.Augmented, AugmentedPattern{Pattern: anchor.Anchor, Offset: 0, IsEnd: true})
		}
	}

	for _, c := range anchor.Backward {
		if !c.IsRepeat && c.Length == 1 && c.Info.Kernel == CCWarpEqual {
			anchor.SimpleBackward = append(anchor.SimpleBackward, c)
		}
	}
}

func extractDistToLineEnd(re *syntax.Regexp, anchor *AnchorInfo) (int, int, bool) {
	if re == nil {
		return 0, 0, false
	}
	if re.Op == syntax.OpAlternate {
		min, max := 1<<30, -1
		anyEnd := false
		for _, sub := range re.Sub {
			d0, d1, ok := extractDistToLineEnd(sub, anchor)
			if ok {
				anyEnd = true
				if d0 < min {
					min = d0
				}
				if d1 > max {
					max = d1
				}
			}
		}
		return min, max, anyEnd
	}
	if re.Op != syntax.OpConcat {
		return 0, 0, false
	}

	currentOffset := 0
	anchorIdx := -1
	for i, sub := range re.Sub {
		if currentOffset == anchor.Distance {
			if anchor.HasClass {
				if info, ok := toCCWarp(sub); ok && info.Kernel == anchor.Class.Kernel {
					match := true
					if info.Kernel == CCWarpEqualSet || info.Kernel == CCWarpNotEqualSet {
						if len(info.Extra) != len(anchor.Class.Extra) {
							match = false
						} else {
							for k := range info.Extra {
								if info.Extra[k] != anchor.Class.Extra[k] {
									match = false
									break
								}
							}
						}
					} else {
						match = info.V0 == anchor.Class.V0 && info.V1 == anchor.Class.V1
					}
					if match {
						anchorIdx = i
						break
					}
				}
			} else {
				if lit, ok := isLiteral(sub); ok && string(lit) == string(anchor.Anchor) {
					anchorIdx = i
					break
				}
			}
		}
		d := minLength(sub)
		if d < 0 {
			break
		}
		currentOffset += d
	}
	if anchorIdx < 0 {
		return 0, 0, false
	}

	minDist, maxDist := 0, 0
	foundEnd := false
	for i := anchorIdx + 1; i < len(re.Sub); i++ {
		sub := re.Sub[i]
		if sub.Op == syntax.OpEndLine {
			foundEnd = true
			break
		}
		d0 := minLength(sub)
		d1 := maxLength(sub)
		if d0 < 0 || d1 < 0 {
			return 0, 0, false
		}
		minDist += d0
		maxDist += d1
	}
	if foundEnd {
		return minDist, maxDist, true
	}
	return 0, 0, false
}

func extractDistToEnd(re *syntax.Regexp, anchor *AnchorInfo) (int, int, bool) {
	if re == nil {
		return 0, 0, false
	}
	if re.Op == syntax.OpAlternate {
		min, max := 1<<30, -1
		anyEnd := false
		for _, sub := range re.Sub {
			d0, d1, ok := extractDistToEnd(sub, anchor)
			if ok {
				anyEnd = true
				if d0 < min {
					min = d0
				}
				if d1 > max {
					max = d1
				}
			}
		}
		return min, max, anyEnd
	}
	if re.Op != syntax.OpConcat {
		return 0, 0, false
	}

	currentOffset := 0
	anchorIdx := -1
	for i, sub := range re.Sub {
		if currentOffset == anchor.Distance {
			if anchor.HasClass {
				if info, ok := toCCWarp(sub); ok && info.Kernel == anchor.Class.Kernel {
					match := true
					if info.Kernel == CCWarpEqualSet || info.Kernel == CCWarpNotEqualSet {
						if len(info.Extra) != len(anchor.Class.Extra) {
							match = false
						} else {
							for k := range info.Extra {
								if info.Extra[k] != anchor.Class.Extra[k] {
									match = false
									break
								}
							}
						}
					} else {
						match = info.V0 == anchor.Class.V0 && info.V1 == anchor.Class.V1
					}
					if match {
						anchorIdx = i
						break
					}
				}
			} else {
				if lit, ok := isLiteral(sub); ok && string(lit) == string(anchor.Anchor) {
					anchorIdx = i
					break
				}
			}
		}
		d := minLength(sub)
		if d < 0 {
			break
		}
		currentOffset += d
	}
	if anchorIdx < 0 {
		return 0, 0, false
	}

	minDist, maxDist := 0, 0
	foundEnd := false
	for i := anchorIdx + 1; i < len(re.Sub); i++ {
		sub := re.Sub[i]
		if sub.Op == syntax.OpEndText {
			foundEnd = true
			break
		}
		d0 := minLength(sub)
		d1 := maxLength(sub)
		if d0 < 0 || d1 < 0 {
			return 0, 0, false
		}
		minDist += d0
		maxDist += d1
	}
	if foundEnd {
		return minDist, maxDist, true
	}
	return 0, 0, false
}

func extractConstraints(re *syntax.Regexp, anchor *AnchorInfo) {
	if re == nil {
		return
	}
	if re.Op == syntax.OpAlternate {
		for _, sub := range re.Sub {
			extractConstraints(sub, anchor)
		}
		return
	}
	if re.Op != syntax.OpConcat {
		return
	}

	var anchorIdx int = -1
	currentOffset := 0
	for i, sub := range re.Sub {
		if currentOffset == anchor.Distance {
			if anchor.HasClass {
				if info, ok := toCCWarp(sub); ok && info.Kernel == anchor.Class.Kernel {
					match := true
					if info.Kernel == CCWarpEqualSet || info.Kernel == CCWarpNotEqualSet {
						if len(info.Extra) != len(anchor.Class.Extra) {
							match = false
						} else {
							for k := range info.Extra {
								if info.Extra[k] != anchor.Class.Extra[k] {
									match = false
									break
								}
							}
						}
					} else {
						match = info.V0 == anchor.Class.V0 && info.V1 == anchor.Class.V1
					}
					if match {
						anchorIdx = i
						break
					}
				}
			} else {
				if lit, ok := isLiteral(sub); ok && string(lit) == string(anchor.Anchor) {
					anchorIdx = i
					break
				}
			}
		}
		d := minLength(sub)
		if d < 0 {
			break
		}
		currentOffset += d
	}
	if anchorIdx < 0 {
		return
	}

	backOffset := 0
	for i := anchorIdx - 1; i >= 0; i-- {
		sub := re.Sub[i]
		if sub.Op == syntax.OpBeginText || sub.Op == syntax.OpBeginLine {
			continue
		}

		d := minLength(sub)
		maxD := maxLength(sub)
		isRepeat := d != maxD || sub.Op == syntax.OpStar || sub.Op == syntax.OpPlus || (sub.Op == syntax.OpRepeat && sub.Max == -1)

		if sub.Op == syntax.OpLiteral && sub.Flags&syntax.FoldCase == 0 {
			for j := len(sub.Rune) - 1; j >= 0; j-- {
				r := sub.Rune[j]
				rd := utf8.RuneLen(r)
				anchor.Backward = append(anchor.Backward, Constraint{
					Offset: backOffset - rd, Length: rd,
					Info: CCWarpInfo{Kernel: CCWarpEqual, V0: uint64(r)},
				})
				backOffset -= rd
			}
			continue
		}

		if info, ok := toCCWarp(sub); ok {
			anchor.Backward = append(anchor.Backward, Constraint{
				Offset: backOffset - d, Length: d, IsRepeat: isRepeat, Info: info,
			})
			if isRepeat {
				break
			}
		} else {
			if d == maxD && d > 0 {
				anchor.Backward = append(anchor.Backward, Constraint{
					Offset: backOffset - d, Length: d, IsRepeat: false,
				})
			} else if maxLength(sub) > 0 {
				anchor.Backward = append(anchor.Backward, Constraint{
					Offset: backOffset, Length: 0, IsRepeat: true,
					Info: CCWarpInfo{Kernel: CCWarpAnyChar},
				})
				break
			}
		}
		if d >= 0 {
			backOffset -= d
		} else {
			break
		}
	}

	forwardOffset := 1
	if !anchor.HasClass {
		forwardOffset = len(anchor.Anchor)
	}

	for i := anchorIdx + 1; i < len(re.Sub); i++ {
		sub := re.Sub[i]
		if sub.Op == syntax.OpEndText || sub.Op == syntax.OpEndLine {
			continue
		}

		d := minLength(sub)
		maxD := maxLength(sub)
		isRepeat := d != maxD || sub.Op == syntax.OpStar || sub.Op == syntax.OpPlus || (sub.Op == syntax.OpRepeat && sub.Max == -1)

		if sub.Op == syntax.OpLiteral && sub.Flags&syntax.FoldCase == 0 {
			for _, r := range sub.Rune {
				rd := utf8.RuneLen(r)
				anchor.Forward = append(anchor.Forward, Constraint{
					Offset: forwardOffset, Length: rd,
					Info: CCWarpInfo{Kernel: CCWarpEqual, V0: uint64(r)},
				})
				forwardOffset += rd
			}
			continue
		}

		if info, ok := toCCWarp(sub); ok {
			anchor.Forward = append(anchor.Forward, Constraint{
				Offset: forwardOffset, Length: d, IsRepeat: isRepeat, Info: info,
			})
			if isRepeat {
				break
			}
		} else {
			if d == maxD && d > 0 {
				anchor.Forward = append(anchor.Forward, Constraint{
					Offset: forwardOffset, Length: d, IsRepeat: false,
				})
			} else if maxLength(sub) > 0 {
				anchor.Forward = append(anchor.Forward, Constraint{
					Offset: forwardOffset, Length: 0, IsRepeat: true,
					Info: CCWarpInfo{Kernel: CCWarpAnyChar},
				})
				break
			}
		}
		if d >= 0 {
			forwardOffset += d
		} else {
			break
		}
	}
}

func isLiteral(re *syntax.Regexp) ([]byte, bool) {
	if re == nil {
		return nil, false
	}
	if re.Op == syntax.OpLiteral && re.Flags&syntax.FoldCase == 0 {
		var buf []byte
		for _, r := range re.Rune {
			var b [utf8.UTFMax]byte
			n := utf8.EncodeRune(b[:], r)
			buf = append(buf, b[:n]...)
		}
		return buf, true
	}
	if re.Op == syntax.OpCharClass && re.Flags&syntax.FoldCase == 0 && len(re.Rune) == 2 && re.Rune[0] == re.Rune[1] {
		var b [utf8.UTFMax]byte
		n := utf8.EncodeRune(b[:], re.Rune[0])
		return b[:n], true
	}
	if re.Op == syntax.OpCapture {
		return isLiteral(re.Sub[0])
	}
	return nil, false
}

func toCCWarp(re *syntax.Regexp) (CCWarpInfo, bool) {
	if re == nil {
		return CCWarpInfo{}, false
	}
	switch re.Op {
	case syntax.OpLiteral:
		if len(re.Rune) == 1 {
			r := re.Rune[0]
			if re.Flags&syntax.FoldCase != 0 {
				var extra []uint64
				seen := make(map[rune]bool)
				f := r
				for {
					if !seen[f] {
						if f < 0x80 {
							extra = append(extra, uint64(f))
						} else {
							return CCWarpInfo{}, false // Cannot use CCWarp for multi-byte runes
						}
						seen[f] = true
					}
					f = unicode.SimpleFold(f)
					if f == r {
						break
					}
				}
				if len(extra) > 0 {
					return CCWarpInfo{Kernel: CCWarpEqualSet, Extra: extra}, true
				}
				return CCWarpInfo{}, false
			}
			return CCWarpInfo{Kernel: CCWarpEqual, V0: uint64(r)}, true
		}
	case syntax.OpCharClass:
		if re.Flags&syntax.FoldCase != 0 {
			var extra []uint64
			seen := make(map[rune]bool)
			for i := 0; i+1 < len(re.Rune); i += 2 {
				for r := re.Rune[i]; r <= re.Rune[i+1]; r++ {
					f := r
					for {
						if !seen[f] {
							if f < 0x80 {
								extra = append(extra, uint64(f))
							} else {
								return CCWarpInfo{}, false // Cannot use CCWarp for multi-byte runes
							}
							seen[f] = true
						}
						f = unicode.SimpleFold(f)
						if f == r {
							break
						}
					}
					if len(extra) > 1000 {
						return CCWarpInfo{}, false
					}
				}
			}
			if len(extra) > 0 {
				return CCWarpInfo{Kernel: CCWarpEqualSet, Extra: extra}, true
			}
			return CCWarpInfo{}, false
		}
		if len(re.Rune) == 2 {
			return CCWarpInfo{Kernel: CCWarpSingleRange, V0: uint64(re.Rune[0]), V1: uint64(re.Rune[1])}, true
		}
		if len(re.Rune) == 4 && re.Rune[0] == 0 && re.Rune[3] == 0x10FFFF {
			// Negated single range or char
			low, high := re.Rune[1]+1, re.Rune[2]-1
			if low <= high {
				return CCWarpInfo{Kernel: CCWarpNotSingleRange, V0: uint64(low), V1: uint64(high)}, true
			}
		}
		var extra []uint64
		for i := 0; i+1 < len(re.Rune); i += 2 {
			for r := re.Rune[i]; r <= re.Rune[i+1]; r++ {
				if r < 0x80 {
					extra = append(extra, uint64(r))
				} else {
					return CCWarpInfo{}, false // Cannot use CCWarp for multi-byte runes
				}
				if len(extra) > 1000 {
					return CCWarpInfo{}, false // Too large
				}
			}
		}
		if len(extra) > 0 {
			return CCWarpInfo{Kernel: CCWarpEqualSet, Extra: extra}, true
		}
	case syntax.OpAnyCharNotNL:
		return CCWarpInfo{Kernel: CCWarpAnyExceptNL}, true
	case syntax.OpAnyChar:
		return CCWarpInfo{Kernel: CCWarpAnyChar}, true
	case syntax.OpRepeat, syntax.OpPlus, syntax.OpStar:
		if info, ok := toCCWarp(re.Sub[0]); ok {
			return info, true
		}
	case syntax.OpCapture:
		return toCCWarp(re.Sub[0])
	case syntax.OpAlternate:
		var combined []uint64
		seen := make(map[uint64]bool)
		allSimple := true
		for _, sub := range re.Sub {
			if sub.Op == syntax.OpEmptyMatch || sub.Op == syntax.OpBeginText || sub.Op == syntax.OpBeginLine || sub.Op == syntax.OpEndText || sub.Op == syntax.OpEndLine {
				continue
			}
			info, ok := toCCWarp(sub)
			if !ok {
				allSimple = false
				break
			}
			switch info.Kernel {
			case CCWarpEqual:
				if !seen[info.V0] {
					combined = append(combined, info.V0)
					seen[info.V0] = true
				}
			case CCWarpEqualSet:
				for _, v := range info.Extra {
					if !seen[v] {
						combined = append(combined, v)
						seen[v] = true
					}
				}
			case CCWarpSingleRange:
				for v := info.V0; v <= info.V1 && v < 0x80; v++ {
					if !seen[v] {
						combined = append(combined, v)
						seen[v] = true
					}
				}
			default:
				allSimple = false
				break
			}
			if !allSimple {
				break
			}
		}
		if allSimple && len(combined) > 0 {
			return CCWarpInfo{Kernel: CCWarpEqualSet, Extra: combined}, true
		}
	}
	return CCWarpInfo{}, false
}

func SelectBestAnchors(re *syntax.Regexp) []AnchorInfo {
	if minLength(re) == 0 {
		return nil
	}
	flatRE := stripCaptures(re)
	if flatRE == nil {
		return nil
	}
	anchors := findCoveringAnchors(flatRE, 0, true, false, false)
	suffixAnchors := findCoveringSuffixAnchors(flatRE, minLength(flatRE), true, false, false)
	anchors = append(anchors, suffixAnchors...)
	if len(anchors) > 16 {
		anchors = anchors[:16]
	}
	for i := range anchors {
		ExtractConstraints(re, &anchors[i])
	}
	return anchors
}

func findCoveringSuffixAnchors(re *syntax.Regexp, distFromEnd int, atEnd bool, hasEndText bool, hasEndLine bool) []AnchorInfo {
	if re == nil {
		return nil
	}
	switch re.Op {
	case syntax.OpLiteral:
		if re.Flags&syntax.FoldCase == 0 {
			var buf []byte
			for _, r := range re.Rune {
				var b [utf8.UTFMax]byte
				n := utf8.EncodeRune(b[:], r)
				buf = append(buf, b[:n]...)
			}
			if len(buf) > 0 {
				return []AnchorInfo{{
					Anchor: buf, Type: AnchorSuffix, Distance: distFromEnd - len(buf),
					IsFixed: true, Mandatory: true, HasEndText: hasEndText, HasEndLine: hasEndLine,
				}}
			}
		}
	case syntax.OpRepeat, syntax.OpQuest, syntax.OpStar, syntax.OpPlus:
		return findCoveringSuffixAnchors(re.Sub[0], distFromEnd, atEnd, hasEndText, hasEndLine)
	case syntax.OpCapture:
		return findCoveringSuffixAnchors(re.Sub[0], distFromEnd, atEnd, hasEndText, hasEndLine)
	case syntax.OpConcat:
		currentDist := distFromEnd
		currentAtEnd := atEnd
		currentHasEndText := hasEndText
		currentHasEndLine := hasEndLine

		var all []AnchorInfo
		for i := len(re.Sub) - 1; i >= 0; i-- {
			sub := re.Sub[i]
			subAnchors := findCoveringSuffixAnchors(sub, currentDist, currentAtEnd, currentHasEndText, currentHasEndLine)
			if len(subAnchors) > 0 {
				isNullable := matchesEmpty(sub)
				// To be IsFixed, EVERYTHING before this sub must be fixed distance
				prefixIsFixed := true
				for k := 0; k < i; k++ {
					if minLength(re.Sub[k]) != maxLength(re.Sub[k]) {
						prefixIsFixed = false
						break
					}
				}
				for j := range subAnchors {
					subAnchors[j].IsFixed = subAnchors[j].IsFixed && prefixIsFixed
					if isNullable {
						subAnchors[j].Mandatory = false
					}
				}
				all = append(all, subAnchors...)
			}
			if !matchesEmpty(sub) {
				return all
			}

			if sub.Op == syntax.OpEndText && currentAtEnd {
				currentHasEndText = true
			}
			if sub.Op == syntax.OpEndLine && currentAtEnd {
				currentHasEndLine = true
			}
			d := minLength(sub)
			if d > 0 {
				currentAtEnd = false
			}
			if d >= 0 {
				currentDist -= d
			} else {
				currentDist = -1000000
			}
		}
		return all
	case syntax.OpAlternate:
		var all []AnchorInfo
		commonIsFixed := true
		commonDist := -1
		for _, sub := range re.Sub {
			subAnchors := findCoveringSuffixAnchors(sub, distFromEnd, atEnd, hasEndText, hasEndLine)
			if len(subAnchors) == 0 {
				return nil
			}
			for j := range subAnchors {
				if commonDist < 0 {
					commonDist = subAnchors[j].Distance
				} else if commonDist != subAnchors[j].Distance {
					commonIsFixed = false
				}
				if !subAnchors[j].IsFixed {
					commonIsFixed = false
				}
				subAnchors[j].Mandatory = false
			}
			all = append(all, subAnchors...)
		}
		if !commonIsFixed {
			for i := range all {
				all[i].IsFixed = false
			}
		}
		return all
	}
	return nil
}

func findCoveringAnchors(re *syntax.Regexp, offset int, atStart bool, hasBeginText bool, hasBeginLine bool) []AnchorInfo {
	if re == nil {
		return nil
	}
	switch re.Op {
	case syntax.OpLiteral:
		if re.Flags&syntax.FoldCase == 0 {
			var buf []byte
			for _, r := range re.Rune {
				var b [utf8.UTFMax]byte
				n := utf8.EncodeRune(b[:], r)
				buf = append(buf, b[:n]...)
			}
			if len(buf) > 0 {
				return []AnchorInfo{{
					Anchor: buf, Type: AnchorPivot, Distance: offset,
					IsFixed: true, Mandatory: true, HasBeginText: hasBeginText, HasBeginLine: hasBeginLine,
				}}
			}
		} else {
			if info, ok := toCCWarp(re); ok {
				return []AnchorInfo{{
					Class: info, HasClass: true, Type: AnchorPivot, Distance: offset,
					IsFixed: true, Mandatory: true, HasBeginText: hasBeginText, HasBeginLine: hasBeginLine,
				}}
			}
		}
	case syntax.OpCharClass:
		if info, ok := toCCWarp(re); ok {
			return []AnchorInfo{{
				Class: info, HasClass: true, Type: AnchorPivot, Distance: offset,
				IsFixed: true, Mandatory: true, HasBeginText: hasBeginText, HasBeginLine: hasBeginLine,
			}}
		}
	case syntax.OpBeginText:
		return []AnchorInfo{{
			Type: AnchorPivot, Distance: offset,
			IsFixed: true, Mandatory: true, HasBeginText: true, HasBeginLine: hasBeginLine,
		}}
	case syntax.OpBeginLine:
		return []AnchorInfo{{
			Type: AnchorPivot, Distance: offset,
			IsFixed: true, Mandatory: true, HasBeginText: hasBeginText, HasBeginLine: true,
		}}
	case syntax.OpRepeat, syntax.OpQuest, syntax.OpStar, syntax.OpPlus:
		return findCoveringAnchors(re.Sub[0], offset, atStart, hasBeginText, hasBeginLine)
	case syntax.OpCapture:
		return findCoveringAnchors(re.Sub[0], offset, atStart, hasBeginText, hasBeginLine)
	case syntax.OpConcat:
		currentOffset := offset
		currentAtStart := atStart
		currentHasBeginText := hasBeginText
		currentHasBeginLine := hasBeginLine
		currentIsFixed := true
		var all []AnchorInfo
		for _, sub := range re.Sub {
			subAnchors := findCoveringAnchors(sub, currentOffset, currentAtStart, currentHasBeginText, currentHasBeginLine)
			if len(subAnchors) > 0 {
				isNullable := matchesEmpty(sub)
				for j := range subAnchors {
					subAnchors[j].IsFixed = subAnchors[j].IsFixed && currentIsFixed
					if isNullable {
						subAnchors[j].Mandatory = false
					}
				}
				all = append(all, subAnchors...)
			}

			if !matchesEmpty(sub) {
				return all
			}

			if sub.Op == syntax.OpBeginText {
				currentHasBeginText = true
			}
			if sub.Op == syntax.OpBeginLine {
				currentHasBeginLine = true
			}

			d := minLength(sub)
			if d > 0 {
				currentHasBeginText = false
				currentHasBeginLine = false
			}

			maxD := maxLength(sub)
			if d != maxD {
				currentIsFixed = false
			}
			if d > 0 {
				currentAtStart = false
			}
			if d >= 0 {
				currentOffset += d
			} else {
				currentOffset = 1000000
			}
		}
		return all
	case syntax.OpAlternate:
		var all []AnchorInfo
		commonIsFixed := true
		commonDist := -1
		for _, sub := range re.Sub {
			subAnchors := findCoveringAnchors(sub, offset, atStart, hasBeginText, hasBeginLine)
			if len(subAnchors) == 0 {
				return nil
			}
			for j := range subAnchors {
				if commonDist < 0 {
					commonDist = subAnchors[j].Distance
				} else if commonDist != subAnchors[j].Distance {
					commonIsFixed = false
				}
				if !subAnchors[j].IsFixed {
					commonIsFixed = false
				}
				subAnchors[j].Mandatory = false
			}
			all = append(all, subAnchors...)
		}
		if !commonIsFixed {
			for i := range all {
				all[i].IsFixed = false
			}
		}
		return all
	}
	return nil
}

func (a *AnchorInfo) Score() int {
	if !a.Mandatory || !a.IsFixed {
		return 0
	}
	score := len(a.Anchor) * 10
	if a.Distance == 0 {
		score += 5
	}
	if a.IsFixed {
		score += 50
	}
	if a.HasBeginText || a.HasBeginLine || a.HasEndText || a.HasEndLine {
		score += 20
	}
	if a.Type == AnchorSuffix && (a.HasEndText || a.HasEndLine) {
		score += 100
	}
	for _, aug := range a.Augmented {
		if !aug.IsStart && !aug.IsEnd {
			s := len(aug.Pattern) * 12
			if s > score {
				score = s
			}
		}
	}
	return score
}

func (a *AnchorInfo) Validate(b []byte, p int, matchStart int) (int, int, bool) {
	newMatchStart := matchStart
	for _, c := range a.Backward {
		if c.IsRepeat {
			end := p + c.Offset
			if end < matchStart {
				continue
			}
			switch c.Info.Kernel {
			case CCWarpAnyExceptNL:
				if idx := bytes.IndexByte(b[matchStart:end], '\n'); idx >= 0 {
					return p, matchStart + idx + 1, false
				}
			case CCWarpEqual:
				target := byte(c.Info.V0)
				for i := matchStart; i < end; i++ {
					if b[i] != target {
						return p, i, false
					}
				}
			case CCWarpSingleRange:
				low, high := byte(c.Info.V0), byte(c.Info.V1)
				for i := matchStart; i < end; i++ {
					if v := b[i]; v < low || v > high {
						return p, i, false
					}
				}
			case CCWarpNotSingleRange:
				low, high := byte(c.Info.V0), byte(c.Info.V1)
				for i := matchStart; i < end; i++ {
					if v := b[i]; v >= low && v <= high {
						return p, i, false
					}
				}
			}
		} else {
			start := p + c.Offset
			if start < matchStart {
				return p, matchStart, false
			}
			if !ValidateFixed(c.Info, b[start:start+c.Length]) {
				return p, start, false
			}
		}
	}
	endPos := p + len(a.Anchor)
	if a.HasClass {
		endPos = p + 1
	}
	for _, c := range a.Forward {
		start := p + c.Offset
		if start > len(b) {
			return p, newMatchStart, false
		}
		if c.IsRepeat {
			skipped := Warp(c.Info, b[start:])
			endPos = start + skipped
		} else {
			if start+c.Length > len(b) {
				return p, newMatchStart, false
			}
			if !ValidateFixed(c.Info, b[start:start+c.Length]) {
				return p, newMatchStart, false
			}
			endPos = start + c.Length
		}
	}
	return endPos, newMatchStart, true
}

func ValidateFixed(info CCWarpInfo, b []byte) bool {
	if len(b) == 0 {
		return true
	}
	switch info.Kernel {
	case CCWarpEqual:
		target := byte(info.V0)
		for _, v := range b {
			if v != target {
				return false
			}
		}
		return true
	case CCWarpSingleRange:
		low, high := byte(info.V0), byte(info.V1)
		for _, v := range b {
			if v < low || v > high {
				return false
			}
		}
		return true
	case CCWarpNotSingleRange:
		low, high := byte(info.V0), byte(info.V1)
		for _, v := range b {
			if v >= low && v <= high {
				return false
			}
		}
		return true
	case CCWarpAnyChar:
		return true
	case CCWarpAnyExceptNL:
		for _, v := range b {
			if v == '\n' {
				return false
			}
		}
		return true
	case CCWarpNotEqual:
		target := byte(info.V0)
		for _, v := range b {
			if v == target {
				return false
			}
		}
		return true
	case CCWarpEqualSet:
		for _, target := range b {
			found := false
			for _, v := range info.Extra {
				if byte(v) == target {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	case CCWarpNotEqualSet:
		for _, target := range b {
			for _, v := range info.Extra {
				if byte(v) == target {
					return false
				}
			}
		}
		return true
	}
	return true
}

func Warp(info CCWarpInfo, b []byte) int {
	i := 0
	switch info.Kernel {
	case CCWarpAnyChar:
		n := len(b)
		for i+8 <= n {
			v := binary.LittleEndian.Uint64(b[i:])
			if v&0x8080808080808080 != 0 {
				break
			}
			i += 8
		}
		for i < n && b[i] < 0x80 {
			i++
		}
		return i
	case CCWarpAnyExceptNL:
		pos := bytes.IndexByte(b, '\n')
		limit := len(b)
		if pos >= 0 {
			limit = pos
		}
		for i+8 <= limit {
			v := binary.LittleEndian.Uint64(b[i:])
			if v&0x8080808080808080 != 0 {
				break
			}
			i += 8
		}
		for i < limit && b[i] < 0x80 {
			i++
		}
		return i
	case CCWarpEqual:
		target := byte(info.V0)
		target64 := splat(uint64(target))
		for i+8 <= len(b) {
			v := binary.LittleEndian.Uint64(b[i:])
			if v != target64 {
				diff := v ^ target64
				return i + bits.TrailingZeros64(diff)/8
			}
			i += 8
		}
		for i < len(b) && b[i] == target {
			i++
		}
	case CCWarpNotEqual:
		target := byte(info.V0)
		for i < len(b) && b[i] != target {
			i++
		}
	case CCWarpSingleRange:
		low, high := byte(info.V0), byte(info.V1)
		low64, high64 := splat(uint64(low)), splat(uint64(high))
		for i+8 <= len(b) {
			v := binary.LittleEndian.Uint64(b[i:])
			outside := ((v - low64) & ^v) | ((high64 - v) & ^high64)
			if (outside & 0x8080808080808080) != 0 {
				break
			}
			i += 8
		}
		for i < len(b) {
			if b[i] < low || b[i] > high {
				break
			}
			i++
		}
	case CCWarpNotSingleRange:
		low, high := byte(info.V0), byte(info.V1)
		for i < len(b) && (b[i] < low || b[i] > high) {
			i++
		}
	case CCWarpEqualSet:
		for i < len(b) {
			target := b[i]
			found := false
			for _, v := range info.Extra {
				if byte(v) == target {
					found = true
					break
				}
			}
			if !found {
				break
			}
			i++
		}
	case CCWarpNotEqualSet:
		for i < len(b) {
			target := b[i]
			found := false
			for _, v := range info.Extra {
				if byte(v) == target {
					found = true
					break
				}
			}
			if found {
				break
			}
			i++
		}
	}
	return i
}

func WarpBack(info CCWarpInfo, b []byte) int {
	i := 0
	n := len(b)
	switch info.Kernel {
	case CCWarpAnyChar:
		return n
	case CCWarpAnyExceptNL:
		pos := bytes.LastIndexByte(b, '\n')
		if pos < 0 {
			return n
		}
		return n - (pos + 1)
	case CCWarpEqual:
		target := byte(info.V0)
		for i < n && b[n-1-i] == target {
			i++
		}
	case CCWarpNotEqual:
		target := byte(info.V0)
		for i < n && b[n-1-i] != target {
			i++
		}
	case CCWarpSingleRange:
		low, high := byte(info.V0), byte(info.V1)
		for i < n {
			v := b[n-1-i]
			if v < low || v > high {
				break
			}
			i++
		}
	case CCWarpNotSingleRange:
		low, high := byte(info.V0), byte(info.V1)
		for i < n {
			v := b[n-1-i]
			if v >= low && v <= high {
				break
			}
			i++
		}
	case CCWarpEqualSet:
		for i < n {
			target := b[n-1-i]
			found := false
			for _, v := range info.Extra {
				if byte(v) == target {
					found = true
					break
				}
			}
			if !found {
				break
			}
			i++
		}
	case CCWarpNotEqualSet:
		for i < n {
			target := b[n-1-i]
			found := false
			for _, v := range info.Extra {
				if byte(v) == target {
					found = true
					break
				}
			}
			if found {
				break
			}
			i++
		}
	}
	return i
}

func IndexClass(info CCWarpInfo, b []byte) int {
	i := 0
	switch info.Kernel {
	case CCWarpAnyChar:
		if len(b) > 0 {
			return 0
		}
		return -1
	case CCWarpAnyExceptNL:
		for i < len(b) {
			if b[i] != '\n' {
				return i
			}
			i++
		}
		return -1
	case CCWarpEqual:
		if info.IncludeNL {
			target := byte(info.V0)
			for i < len(b) {
				if b[i] == target || b[i] == '\n' {
					return i
				}
				i++
			}
			return -1
		}
		return bytes.IndexByte(b, byte(info.V0))
	case CCWarpNotEqual:
		target := byte(info.V0)
		for i < len(b) {
			if b[i] != target || (info.IncludeNL && b[i] == '\n') {
				return i
			}
			i++
		}
		return -1
	case CCWarpSingleRange:
		low, high := byte(info.V0), byte(info.V1)
		low64, high64 := splat(uint64(low)), splat(uint64(high))
		var nl64 uint64
		if info.IncludeNL {
			nl64 = splat(uint64('\n'))
		}
		for i+8 <= len(b) {
			v := binary.LittleEndian.Uint64(b[i:])
			outside := ((v - low64) & ^v) | ((high64 - v) & ^high64)
			inside := ^outside & 0x8080808080808080
			if info.IncludeNL {
				matchNL := ^((v ^ nl64 + 0x7f7f7f7f7f7f7f7f) | v ^ nl64) & 0x8080808080808080
				inside |= matchNL
			}
			if inside != 0 {
				break
			}
			i += 8
		}
		for ; i < len(b); i++ {
			if (b[i] >= low && b[i] <= high) || (info.IncludeNL && b[i] == '\n') {
				return i
			}
		}
	case CCWarpNotSingleRange:
		low, high := byte(info.V0), byte(info.V1)
		for i < len(b) {
			if b[i] < low || b[i] > high || (info.IncludeNL && b[i] == '\n') {
				return i
			}
			i++
		}
		return -1
	case CCWarpEqualSet:
		if info.IndexAny != "" {
			return bytes.IndexAny(b, info.IndexAny)
		}
		for i < len(b) {
			target := b[i]
			for _, v := range info.Extra {
				if byte(v) == target {
					return i
				}
			}
			if info.IncludeNL && target == '\n' {
				return i
			}
			i++
		}
	case CCWarpNotEqualSet:
		for i < len(b) {
			target := b[i]
			found := false
			for _, v := range info.Extra {
				if byte(v) == target {
					found = true
					break
				}
			}
			if !found || (info.IncludeNL && target == '\n') {
				return i
			}
			i++
		}
	}
	return -1
}

func splat(v uint64) uint64 { return v * 0x0101010101010101 }

func IsLineBounded(re *syntax.Regexp) bool {
	if re == nil {
		return true
	}
	switch re.Op {
	case syntax.OpLiteral:
		for _, r := range re.Rune {
			if r == '\n' {
				return false
			}
		}
		return true
	case syntax.OpCharClass:
		for i := 0; i+1 < len(re.Rune); i += 2 {
			if re.Rune[i] <= '\n' && '\n' <= re.Rune[i+1] {
				return false
			}
		}
		return true
	case syntax.OpAnyCharNotNL:
		return true
	case syntax.OpAnyChar:
		return false
	case syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText:
		return true
	case syntax.OpCapture, syntax.OpRepeat, syntax.OpQuest, syntax.OpPlus, syntax.OpStar:
		return IsLineBounded(re.Sub[0])
	case syntax.OpConcat, syntax.OpAlternate:
		for _, sub := range re.Sub {
			if !IsLineBounded(sub) {
				return false
			}
		}
		return true
	}
	return false
}

func HasComplexAnchors(re *syntax.Regexp) bool {
	if re == nil {
		return false
	}
	switch re.Op {
	case syntax.OpCapture, syntax.OpRepeat, syntax.OpQuest, syntax.OpPlus, syntax.OpStar:
		if len(re.Sub) > 0 {
			return HasComplexAnchors(re.Sub[0])
		}
		return false
	case syntax.OpConcat, syntax.OpAlternate:
		for _, sub := range re.Sub {
			if HasComplexAnchors(sub) {
				return true
			}
		}
		return false
	}
	return false
}
