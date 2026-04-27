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
	Type         AnchorType
	Distance     int // Estimated distance from the start of the match
	Forward      []Constraint
	Backward     []Constraint
	HasBeginText bool // Starts with ^
	HasEndText   bool // Ends with $
}

// ExtractAnchors traverses the AST and identifies all potential literal anchors.
func ExtractAnchors(re *syntax.Regexp) []AnchorInfo {
	anchors := extractAnchors(re, 0, false)

	// Suffix identification
	totalMin := minLength(re)
	if totalMin >= 0 {
		for i := range anchors {
			if anchors[i].Distance+len(anchors[i].Anchor) == totalMin {
				if anchors[i].Type != AnchorPrefix {
					anchors[i].Type = AnchorSuffix
				}
			}
		}
	}

	// Anchor flags
	hasBegin := false
	hasEnd := false
	if re.Op == syntax.OpConcat {
		if len(re.Sub) > 0 && re.Sub[0].Op == syntax.OpBeginText {
			hasBegin = true
		}
		if len(re.Sub) > 0 && re.Sub[len(re.Sub)-1].Op == syntax.OpEndText {
			hasEnd = true
		}
	} else if re.Op == syntax.OpBeginText {
		hasBegin = true
	} else if re.Op == syntax.OpEndText {
		hasEnd = true
	}

	for i := range anchors {
		anchors[i].HasBeginText = hasBegin
		anchors[i].HasEndText = hasEnd
	}

	return anchors
}

func extractAnchors(re *syntax.Regexp, offset int, inStar bool) []AnchorInfo {
	var anchors []AnchorInfo

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
				anchors = append(anchors, AnchorInfo{
					Anchor:   buf,
					Type:     AnchorPivot,
					Distance: offset,
				})
			}
		}
	case syntax.OpCharClass:
		if re.Flags&syntax.FoldCase == 0 && len(re.Rune) == 2 && re.Rune[0] == re.Rune[1] {
			var b [utf8.UTFMax]byte
			n := utf8.EncodeRune(b[:], re.Rune[0])
			anchors = append(anchors, AnchorInfo{
				Anchor:   b[:n],
				Type:     AnchorPivot,
				Distance: offset,
			})
		}
	case syntax.OpCapture, syntax.OpRepeat, syntax.OpQuest, syntax.OpPlus:
		anchors = append(anchors, extractAnchors(re.Sub[0], offset, inStar)...)
	case syntax.OpConcat:
		currentOffset := offset
		for i, sub := range re.Sub {
			subAnchors := extractAnchors(sub, currentOffset, inStar)
			if i == 0 && offset == 0 && !inStar {
				for j := range subAnchors {
					if subAnchors[j].Distance == 0 {
						subAnchors[j].Type = AnchorPrefix
					}
				}
			}
			anchors = append(anchors, subAnchors...)

			if d := minLength(sub); d >= 0 {
				currentOffset += d
			} else {
				currentOffset = 1000000
			}
		}
	case syntax.OpStar:
		anchors = append(anchors, extractAnchors(re.Sub[0], offset, true)...)
	}

	return anchors
}

func minLength(re *syntax.Regexp) int {
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
	if re.Op != syntax.OpConcat {
		return
	}

	var anchorIdx int = -1
	currentOffset := 0
	for i, sub := range re.Sub {
		if currentOffset == anchor.Distance {
			if lit, ok := isLiteral(sub); ok && string(lit) == string(anchor.Anchor) {
				anchorIdx = i
				break
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

	// Backward constraints (Only support fixed length for now)
	backOffset := 0
	for i := anchorIdx - 1; i >= 0; i-- {
		sub := re.Sub[i]
		if sub.Op == syntax.OpBeginText {
			continue // Handled by HasBeginText
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

	// Forward constraints (Support variable length skip/warp)
	forwardOffset := len(anchor.Anchor)
	for i := anchorIdx + 1; i < len(re.Sub); i++ {
		sub := re.Sub[i]
		if sub.Op == syntax.OpEndText {
			continue // Handled by HasEndText
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
				// After a repeat, we can't easily track fixed offsets anymore for Pass 0
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

func SelectBestAnchor(anchors []AnchorInfo) *AnchorInfo {
	if len(anchors) == 0 {
		return nil
	}
	var best *AnchorInfo
	maxScore := -1
	for i := range anchors {
		score := len(anchors[i].Anchor) * 10
		if anchors[i].Type == AnchorPrefix {
			score += 100
		}
		if anchors[i].Type == AnchorSuffix {
			score += 50
		}
		if anchors[i].Distance >= 1000000 {
			score -= 50
		}
		if score > maxScore {
			maxScore = score
			best = &anchors[i]
		}
	}
	return best
}

// Validate checks if the constraints at the given anchor position P are satisfied.
// It returns the estimated end of the validated region (for skipping forward).
func (a *AnchorInfo) Validate(b []byte, p int) (int, bool) {
	// Backward constraints
	for _, c := range a.Backward {
		start := p + c.Offset
		if start < 0 {
			return p, false
		}
		if !validateFixed(c.Info, b[start:start+c.Length]) {
			return p, false
		}
	}

	// Forward constraints
	endPos := p + len(a.Anchor)
	for _, c := range a.Forward {
		start := p + c.Offset
		if start > len(b) {
			return p, false
		}
		if c.IsRepeat {
			// Dynamic Skip (Warp)
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

func splat(v uint64) uint64 {
	return v * 0x0101010101010101
}

// HasComplexAnchors reports if the pattern contains anchors other than ^ or $.
func HasComplexAnchors(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpBeginLine, syntax.OpEndLine, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return true
	case syntax.OpCapture, syntax.OpRepeat, syntax.OpQuest, syntax.OpPlus, syntax.OpStar:
		return HasComplexAnchors(re.Sub[0])
	case syntax.OpConcat, syntax.OpAlternate:
		for _, sub := range re.Sub {
			if HasComplexAnchors(sub) {
				return true
			}
		}
	}
	return false
}
