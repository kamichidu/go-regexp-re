package ir

import (
	"bytes"
	"encoding/binary"
	"github.com/kamichidu/go-regexp-re/syntax"
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

// AnchorInfo holds information about a potential anchor in the pattern.
type AnchorInfo struct {
	Anchor       []byte
	Class        CCWarpInfo // If Anchor is empty, use this SWAR class anchor
	HasClass     bool
	Type         AnchorType
	Distance     int // Estimated distance from the start of the match
	Forward      []Constraint
	Backward     []Constraint
	HasBeginText bool // This anchor path is strictly anchored to ^
	HasEndText   bool // This anchor path is strictly anchored to $
}

// ExtractAnchors traverses the AST and identifies all potential anchors.
func ExtractAnchors(re *syntax.Regexp) []AnchorInfo {
	// MANDATORY: If the pattern can match empty string, MAP rejection is unsafe.
	if minLength(re) == 0 {
		return nil
	}

	flatRE := stripCaptures(re)
	if flatRE == nil {
		return nil
	}
	anchors := extractAnchors(flatRE, 0, false, true, false)

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
// atStart: the path is currently at the beginning of the regex.
// hasBegin: the path has already encountered OpBeginText.
func extractAnchors(re *syntax.Regexp, offset int, inOptional bool, atStart bool, hasBegin bool) []AnchorInfo {
	if inOptional || re == nil {
		return nil
	}

	var anchors []AnchorInfo

	switch re.Op {
	case syntax.OpBeginText:
		// Do nothing, but flags will be passed down if atStart is true.
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
					HasBeginText: hasBegin,
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
					HasBeginText: hasBegin,
				})
			} else if info, ok := toCCWarp(re); ok {
				anchors = append(anchors, AnchorInfo{
					Class:        info,
					HasClass:     true,
					Type:         AnchorPivot,
					Distance:     offset,
					HasBeginText: hasBegin,
				})
			}
		}
	case syntax.OpRepeat:
		if re.Min > 0 {
			anchors = append(anchors, extractAnchors(re.Sub[0], offset, false, atStart, hasBegin)...)
		}
	case syntax.OpQuest, syntax.OpStar:
		// Optional
	case syntax.OpPlus:
		anchors = append(anchors, extractAnchors(re.Sub[0], offset, false, atStart, hasBegin)...)
	case syntax.OpCapture:
		anchors = append(anchors, extractAnchors(re.Sub[0], offset, false, atStart, hasBegin)...)
	case syntax.OpConcat:
		currentOffset := offset
		currentAtStart := atStart
		currentHasBegin := hasBegin
		for i, sub := range re.Sub {
			subAnchors := extractAnchors(sub, currentOffset, false, currentAtStart, currentHasBegin)
			if i == 0 && offset == 0 {
				for j := range subAnchors {
					if subAnchors[j].Distance == 0 {
						subAnchors[j].Type = AnchorPrefix
					}
				}
			}
			anchors = append(anchors, subAnchors...)

			if sub.Op == syntax.OpBeginText && currentAtStart {
				currentHasBegin = true
			}

			d := minLength(sub)
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
			anchors = append(anchors, extractAnchors(sub, offset, false, atStart, hasBegin)...)
		}
	}

	return anchors
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
}

func extractConstraints(re *syntax.Regexp, anchor *AnchorInfo) {
	if re == nil {
		return
	}
	if re.Op == syntax.OpAlternate {
		// Find WHICH branch this anchor belongs to.
		// For now, simplify: if anchor is at distance X, find branch that has anchor at distance X.
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
				if info, ok := toCCWarp(sub); ok && info.Kernel == anchor.Class.Kernel && info.V0 == anchor.Class.V0 && info.V1 == anchor.Class.V1 {
					anchorIdx = i
					break
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
		if sub.Op == syntax.OpBeginText {
			continue
		}
		d := minLength(sub)
		if d < 0 {
			break
		}
		backOffset -= d
		if info, ok := toCCWarp(sub); ok {
			anchor.Backward = append(anchor.Backward, Constraint{
				Offset: backOffset,
				Length: d,
				Info:   info,
			})
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
		if sub.Op == syntax.OpEndText {
			continue
		}
		d := minLength(sub)
		isRepeat := false
		if sub.Op == syntax.OpStar || sub.Op == syntax.OpPlus || (sub.Op == syntax.OpRepeat && sub.Max == -1) {
			isRepeat = true
		}

		if info, ok := toCCWarp(sub); ok {
			anchor.Forward = append(anchor.Forward, Constraint{
				Offset:   forwardOffset,
				Length:   d,
				IsRepeat: isRepeat,
				Info:     info,
			})
			if isRepeat {
				break
			}
		} else {
			break
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
		if len(re.Rune) == 1 && re.Flags&syntax.FoldCase == 0 {
			return CCWarpInfo{Kernel: CCWarpEqual, V0: uint64(re.Rune[0])}, true
		}
	case syntax.OpCharClass:
		if re.Flags&syntax.FoldCase == 0 {
			if len(re.Rune) == 2 {
				return CCWarpInfo{Kernel: CCWarpSingleRange, V0: uint64(re.Rune[0]), V1: uint64(re.Rune[1])}, true
			}
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
	}
	return CCWarpInfo{}, false
}

func SelectBestAnchors(anchors []AnchorInfo) []AnchorInfo {
	if len(anchors) == 0 {
		return nil
	}
	// Sort by score or filter duplicates.
	// For now, return up to 4 good anchors.
	// Duplicate anchors with different distances are fine.
	return anchors
}

func (a *AnchorInfo) Validate(b []byte, p int) (int, bool) {
	for _, c := range a.Backward {
		start := p + c.Offset
		if start < 0 {
			return p, false
		}
		if !validateFixed(c.Info, b[start:start+c.Length]) {
			return p, false
		}
	}

	endPos := p + 1
	if !a.HasClass {
		endPos = p + len(a.Anchor)
	}

	for _, c := range a.Forward {
		start := p + c.Offset
		if start > len(b) {
			return p, false
		}
		if c.IsRepeat {
			skipped := warp(c.Info, b[start:])
			endPos = start + skipped
		} else {
			if start+c.Length > len(b) {
				return p, false
			}
			if !validateFixed(c.Info, b[start:start+c.Length]) {
				return p, false
			}
			endPos = start + c.Length
		}
	}

	return endPos, true
}

func validateFixed(info CCWarpInfo, b []byte) bool {
	if len(b) == 0 {
		return true
	}
	switch info.Kernel {
	case CCWarpEqual:
		target := byte(info.V0)
		return bytes.Count(b, []byte{target}) == len(b)
	case CCWarpSingleRange:
		low, high := byte(info.V0), byte(info.V1)
		low64, high64 := splat(uint64(low)), splat(uint64(high))
		i := 0
		for i+8 <= len(b) {
			v := binary.LittleEndian.Uint64(b[i:])
			if ((v+0x7f7f7f7f7f7f7f7f-high64)|(v-low64))&0x8080808080808080 != 0x8080808080808080 {
				return false
			}
			i += 8
		}
		for ; i < len(b); i++ {
			v := b[i]
			if v < low || v > high {
				return false
			}
		}
	case CCWarpNotSingleRange:
		low, high := byte(info.V0), byte(info.V1)
		low64, high64 := splat(uint64(low)), splat(uint64(high))
		i := 0
		for i+8 <= len(b) {
			v := binary.LittleEndian.Uint64(b[i:])
			if ((v+0x7f7f7f7f7f7f7f7f-high64)|(v-low64))&0x8080808080808080 == 0x8080808080808080 {
				return false
			}
			i += 8
		}
		for ; i < len(b); i++ {
			v := b[i]
			if v >= low && v <= high {
				return false
			}
		}
	case CCWarpAnyChar:
		i := 0
		for i+8 <= len(b) {
			v := binary.LittleEndian.Uint64(b[i:])
			if v&0x8080808080808080 != 0 {
				return false
			}
			i += 8
		}
		for ; i < len(b); i++ {
			if b[i] >= 0x80 {
				return false
			}
		}
	case CCWarpAnyExceptNL:
		if bytes.IndexByte(b, '\n') >= 0 {
			return false
		}
		i := 0
		for i+8 <= len(b) {
			v := binary.LittleEndian.Uint64(b[i:])
			if v&0x8080808080808080 != 0 {
				return false
			}
			i += 8
		}
		for ; i < len(b); i++ {
			if b[i] >= 0x80 {
				return false
			}
		}
	case CCWarpNotEqual:
		target := byte(info.V0)
		return bytes.IndexByte(b, target) < 0
	}
	return true
}

func warp(info CCWarpInfo, b []byte) int {
	i := 0
	switch info.Kernel {
	case CCWarpEqual:
		target := byte(info.V0)
		for i < len(b) && b[i] == target {
			i++
		}
	case CCWarpSingleRange:
		low, high := byte(info.V0), byte(info.V1)
		low64, high64 := splat(uint64(low)), splat(uint64(high))
		for i+8 <= len(b) {
			v := binary.LittleEndian.Uint64(b[i:])
			if ((v+0x7f7f7f7f7f7f7f7f-high64)|(v-low64))&0x8080808080808080 != 0x8080808080808080 {
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
	case CCWarpAnyChar:
		for i+8 <= len(b) {
			v := binary.LittleEndian.Uint64(b[i:])
			if v&0x8080808080808080 != 0 {
				break
			}
			i += 8
		}
		for i < len(b) && b[i] < 0x80 {
			i++
		}
	case CCWarpAnyExceptNL:
		for i < len(b) {
			if b[i] == '\n' || b[i] >= 0x80 {
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
	case CCWarpEqual:
		return bytes.IndexByte(b, byte(info.V0))
	case CCWarpSingleRange:
		low, high := byte(info.V0), byte(info.V1)
		low64, high64 := splat(uint64(low)), splat(uint64(high))
		for i+8 <= len(b) {
			v := binary.LittleEndian.Uint64(b[i:])
			if ((v+0x7f7f7f7f7f7f7f7f-high64)|(v-low64))&0x8080808080808080 != 0 {
				break
			}
			i += 8
		}
		for ; i < len(b); i++ {
			if b[i] >= low && b[i] <= high {
				return i
			}
		}
	case CCWarpAnyChar:
		for i < len(b) {
			if b[i] < 0x80 {
				return i
			}
			i++
		}
	case CCWarpAnyExceptNL:
		for i < len(b) {
			if b[i] < 0x80 && b[i] != '\n' {
				return i
			}
			i++
		}
	case CCWarpNotEqual:
		target := byte(info.V0)
		for i < len(b) {
			if b[i] != target {
				return i
			}
			i++
		}
	}
	return -1
}

func splat(v uint64) uint64 {
	return v * 0x0101010101010101
}

func HasComplexAnchors(re *syntax.Regexp) bool {
	if re == nil {
		return false
	}
	switch re.Op {
	case syntax.OpBeginLine, syntax.OpEndLine, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return true
	case syntax.OpCapture, syntax.OpRepeat, syntax.OpQuest, syntax.OpPlus, syntax.OpStar:
		if len(re.Sub) > 0 {
			return HasComplexAnchors(re.Sub[0])
		}
	case syntax.OpConcat, syntax.OpAlternate:
		for _, sub := range re.Sub {
			if HasComplexAnchors(sub) {
				return true
			}
		}
	}
	return false
}
