package ir

import (
	"unicode"
)

// utf8Node represents a node in a Trie of byte ranges that match a set of runes.
type utf8Node struct {
	ranges []byteRange
	next   []*utf8Node // if nil, this is a leaf (match complete)
}

type byteRange struct {
	lo, hi byte
}

// runeRangesToUTF8Trie converts a set of rune ranges [lo1, hi1, lo2, hi2, ...]
// into a Trie of byte-range sequences.
func runeRangesToUTF8Trie(runes []rune, foldCase bool) []*utf8Node {
	var roots []*utf8Node
	if foldCase {
		// Expand each rune range to include its case-folded equivalents.
		// For simplicity and correctness with the existing DFA builder,
		// we "register both" (or all) folded variants into the trie.
		seen := make(map[rune]bool)
		var expanded []rune
		for i := 0; i+1 < len(runes); i += 2 {
			for r := runes[i]; r <= runes[i+1]; r++ {
				if !seen[r] {
					seen[r] = true
					expanded = append(expanded, r)
				}
				for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
					if !seen[f] {
						seen[f] = true
						expanded = append(expanded, f)
					}
				}
			}
		}
		if len(runes)%2 == 1 {
			r := runes[len(runes)-1]
			if !seen[r] {
				seen[r] = true
				expanded = append(expanded, r)
			}
			for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
				if !seen[f] {
					seen[f] = true
					expanded = append(expanded, f)
				}
			}
		}
		for _, r := range expanded {
			roots = append(roots, decomposeRuneRange(r, r)...)
		}
		return roots
	}

	for i := 0; i+1 < len(runes); i += 2 {
		lo, hi := runes[i], runes[i+1]
		roots = append(roots, decomposeRuneRange(lo, hi)...)
	}
	// If there's a trailing rune, treat it as a single-rune range.
	if len(runes)%2 == 1 {
		r := runes[len(runes)-1]
		roots = append(roots, decomposeRuneRange(r, r)...)
	}
	return roots
}

func decomposeRuneRange(lo, hi rune) []*utf8Node {
	var nodes []*utf8Node
	for _, seq := range encodeRange(lo, hi) {
		nodes = append(nodes, sequenceToTrie(seq))
	}
	return nodes
}

func sequenceToTrie(seq []byteRange) *utf8Node {
	if len(seq) == 0 {
		return nil
	}
	return &utf8Node{
		ranges: []byteRange{seq[0]},
		next:   sequenceToTrieChildren(seq[1:]),
	}
}

func sequenceToTrieChildren(seq []byteRange) []*utf8Node {
	if len(seq) == 0 {
		return nil
	}
	return []*utf8Node{sequenceToTrie(seq)}
}

// encodeRange converts a rune range [lo, hi] into a set of byte-range sequences.
// This implements the standard UTF-8 range decomposition.
func encodeRange(lo, hi rune) [][]byteRange {
	var sequences [][]byteRange
	for i := 1; i <= 4; i++ {
		mlo, mhi := rangeForLen(i)
		if lo <= mhi && hi >= mlo {
			sequences = append(sequences, split(max(lo, mlo), min(hi, mhi), i)...)
		}
	}
	return sequences
}

func rangeForLen(n int) (rune, rune) {
	switch n {
	case 1:
		return 0, 0x7F
	case 2:
		return 0x80, 0x7FF
	case 3:
		return 0x800, 0xFFFF
	case 4:
		return 0x10000, 0x10FFFF
	}
	return 0, 0
}

func split(lo, hi rune, n int) [][]byteRange {
	if n == 1 {
		return [][]byteRange{{{byte(lo), byte(hi)}}}
	}

	var res [][]byteRange
	m := rune(1 << (6 * uint(n-1)))
	for lo <= hi {
		next := (lo + m) &^ (m - 1)
		if next <= hi+1 {
			res = append(res, combine(lo, next-1, n))
			lo = next
		} else {
			res = append(res, combine(lo, hi, n))
			break
		}
	}
	return res
}

func combine(lo, hi rune, n int) []byteRange {
	res := make([]byteRange, n)
	for i := n - 1; i >= 0; i-- {
		if i == 0 {
			res[i] = firstByteRange(lo, hi, n)
		} else {
			res[i] = byteRange{byte(0x80 | (lo & 0x3F)), byte(0x80 | (hi & 0x3F))}
			lo >>= 6
			hi >>= 6
		}
	}
	return res
}

func firstByteRange(lo, hi rune, n int) byteRange {
	var mask, prefix rune
	switch n {
	case 2:
		mask, prefix = 0x1F, 0xC0
	case 3:
		mask, prefix = 0x0F, 0xE0
	case 4:
		mask, prefix = 0x07, 0xF0
	}
	return byteRange{byte(prefix | (lo & mask)), byte(prefix | (hi & mask))}
}

func min(a, b rune) rune {
	if a < b {
		return a
	}
	return b
}

func max(a, b rune) rune {
	if a > b {
		return a
	}
	return b
}
