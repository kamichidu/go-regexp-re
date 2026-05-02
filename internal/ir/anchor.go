package ir

import (
	"bytes"
	"encoding/binary"
	"github.com/kamichidu/go-regexp-re/syntax"
	"math/bits"
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
	Distance     int  // Minimum distance from the start of the match
	IsFixed      bool // True if Distance is the EXACT distance
	Mandatory    bool // True if this anchor must be present in every match
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
	anchors := extractAnchors(flatRE, 0, true, true, false)
	// ...

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
func extractAnchors(re *syntax.Regexp, offset int, mandatory bool, atStart bool, hasBegin bool) []AnchorInfo {
	if re == nil {
		return nil
	}

	var anchors []AnchorInfo

	switch re.Op {
	case syntax.OpBeginText:
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
					IsFixed:      true,
					Mandatory:    mandatory,
					HasBeginText: hasBegin,
				})
			} else if info, ok := toCCWarp(re); ok {
				anchors = append(anchors, AnchorInfo{
					Class:        info,
					HasClass:     true,
					Type:         AnchorPivot,
					Distance:     offset,
					IsFixed:      true,
					Mandatory:    mandatory,
					HasBeginText: hasBegin,
				})
			}
		}
	case syntax.OpRepeat:
		if re.Min > 0 {
			anchors = append(anchors, extractAnchors(re.Sub[0], offset, mandatory, atStart, hasBegin)...)
		}
	case syntax.OpQuest, syntax.OpStar:
		anchors = append(anchors, extractAnchors(re.Sub[0], offset, false, atStart, hasBegin)...)
	case syntax.OpPlus:
		anchors = append(anchors, extractAnchors(re.Sub[0], offset, mandatory, atStart, hasBegin)...)
	case syntax.OpCapture:
		anchors = append(anchors, extractAnchors(re.Sub[0], offset, mandatory, atStart, hasBegin)...)
	case syntax.OpConcat:
		currentOffset := offset
		currentAtStart := atStart
		currentHasBegin := hasBegin
		currentIsFixed := true
		for i, sub := range re.Sub {
			subAnchors := extractAnchors(sub, currentOffset, mandatory, currentAtStart, currentHasBegin)
			if i == 0 && offset == 0 {
				for j := range subAnchors {
					if subAnchors[j].Distance == 0 {
						subAnchors[j].Type = AnchorPrefix
					}
				}
			}
			// Propagate currentIsFixed to subAnchors
			for j := range subAnchors {
				subAnchors[j].IsFixed = subAnchors[j].IsFixed && currentIsFixed
			}
			anchors = append(anchors, subAnchors...)

			if sub.Op == syntax.OpBeginText && currentAtStart {
				currentHasBegin = true
			}

			d := minLength(sub)
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
			anchors = append(anchors, extractAnchors(sub, offset, false, atStart, hasBegin)...)
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

	// Score each anchor and pick the best one.
	// We MUST only pick Mandatory AND IsFixed anchors for exclusive search.
	// Non-fixed anchors can't safely jump restartBase because we don't know the exact start.
	var best AnchorInfo
	maxScore := -1

	for _, a := range anchors {
		if !a.Mandatory || !a.IsFixed {
			continue
		}
		s := a.Score()
		if s > maxScore {
			maxScore = s
			best = a
		}
	}

	if maxScore <= 0 {
		return nil
	}
	return []AnchorInfo{best}
}

func (a *AnchorInfo) Score() int {
	// Mandatory and Fixed anchors are the only ones we can safely use for exclusive skip.
	if !a.Mandatory || !a.IsFixed {
		return 0
	}

	score := 0
	if !a.HasClass {
		// Literal anchor: length is key
		score = len(a.Anchor) * 10
	} else {
		// Class anchor: specificity is key
		switch a.Class.Kernel {
		case CCWarpEqual:
			score = 8
		case CCWarpSingleRange:
			score = 5
		case CCWarpAnyChar, CCWarpAnyExceptNL:
			score = 1
		}
	}

	// Prefer anchors closer to the start of the match to reduce false starts
	if a.Distance == 0 {
		score += 5
	}

	// FIXED distance anchors are very strong
	if a.IsFixed {
		score += 50
	}

	// Anchors strictly tied to text boundaries are very strong
	if a.HasBeginText || a.HasEndText {
		score += 20
	}

	return score
}

func (a *AnchorInfo) Validate(b []byte, p int) (int, bool) {
	// ... (no changes here as Input is not used in AnchorInfo.Validate? Wait.)
	for _, c := range a.Backward {
		start := p + c.Offset
		if start < 0 {
			return p, false
		}
		if !ValidateFixed(c.Info, b[start:start+c.Length]) {
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
			skipped := Warp(c.Info, b[start:])
			endPos = start + skipped
		} else {
			if start+c.Length > len(b) {
				return p, false
			}
			if !ValidateFixed(c.Info, b[start:start+c.Length]) {
				return p, false
			}
			endPos = start + c.Length
		}
	}

	return endPos, true
}

func ValidateFixed(info CCWarpInfo, b []byte) bool {
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

func Warp(info CCWarpInfo, b []byte) int {
	i := 0
	switch info.Kernel {
	case CCWarpAnyChar:
		return len(b)
	case CCWarpAnyExceptNL:
		pos := bytes.IndexByte(b, '\n')
		if pos < 0 {
			return len(b)
		}
		return pos
	case CCWarpEqual:
		target := byte(info.V0)
		target64 := splat(uint64(target))
		for i+8 <= len(b) {
			v := binary.LittleEndian.Uint64(b[i:])
			if v != target64 {
				// Find first different byte
				diff := v ^ target64
				pos := bits.TrailingZeros64(diff) / 8
				return i + pos
			}
			i += 8
		}
		for i < len(b) && b[i] == target {
			i++
		}
	case CCWarpSingleRange:
		low, high := byte(info.V0), byte(info.V1)
		low64, high64 := splat(uint64(low)), splat(uint64(high))
		for i+8 <= len(b) {
			v := binary.LittleEndian.Uint64(b[i:])
			// Byte j is outside if (v_j < low) OR (v_j > high)
			// has_less(v, low) = (v - low64) & ~v & 0x80...
			// has_greater(v, high) = (high64 - v) & ~high64 & 0x80...
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
		return bytes.IndexByte(b, byte(info.V0))
	case CCWarpSingleRange:
		low, high := byte(info.V0), byte(info.V1)
		low64, high64 := splat(uint64(low)), splat(uint64(high))
		for i+8 <= len(b) {
			v := binary.LittleEndian.Uint64(b[i:])
			// Byte j is inside if !((v_j < low) OR (v_j > high))
			// is_outside has bit 7 set if byte is outside.
			outside := ((v - low64) & ^v) | ((high64 - v) & ^high64)
			inside := ^outside & 0x8080808080808080
			if inside != 0 {
				break
			}
			i += 8
		}
		for ; i < len(b); i++ {
			if b[i] >= low && b[i] <= high {
				return i
			}
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
