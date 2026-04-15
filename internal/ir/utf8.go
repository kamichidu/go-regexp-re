package ir

import (
	"unicode"
)

// UTF8Node represents a node in a Trie of byte ranges that match a set of runes.
type UTF8Node struct {
	ID     int
	Ranges []ByteRange
	Next   []*UTF8Node // if nil, this is a leaf (match complete)
}

func (n *UTF8Node) Match(b byte, foldCase bool) bool {
	// Note: foldCase is handled during trie construction, 
	// so we just need to check the byte ranges here.
	for _, r := range n.Ranges {
		if b >= r.Lo && b <= r.Hi {
			return true
		}
	}
	return false
}

type ByteRange struct {
	Lo, Hi byte
}

// utf8NodeCache handles canonicalization of UTF8Nodes during compilation.
type utf8NodeCache struct {
	nodes          map[string]*UTF8Node
	totalUTF8Nodes int
}

func newUTF8NodeCache() *utf8NodeCache {
	return &utf8NodeCache{
		nodes:          make(map[string]*UTF8Node),
		totalUTF8Nodes: 1,
	}
}

func (c *utf8NodeCache) newNode(ranges []ByteRange, next []*UTF8Node) *UTF8Node {
	// Simple serialization for cache key
	key := serializeNode(ranges, next)
	if n, ok := c.nodes[key]; ok {
		return n
	}

	id := c.totalUTF8Nodes
	c.totalUTF8Nodes++
	n := &UTF8Node{
		ID:     id,
		Ranges: ranges,
		Next:   next,
	}
	c.nodes[key] = n
	return n
}

func serializeNode(ranges []ByteRange, next []*UTF8Node) string {
	var b []byte
	for _, r := range ranges {
		b = append(b, r.Lo, r.Hi)
	}
	b = append(b, ':')
	for _, n := range next {
		if n == nil {
			b = append(b, '0')
		} else {
			// Use ID instead of pointer for stability
			id := n.ID
			b = append(b, byte(id), byte(id>>8), byte(id>>16), byte(id>>24))
		}
		b = append(b, ',')
	}
	return string(b)
}

// runeRangesToUTF8Trie converts a set of rune ranges [lo1, hi1, lo2, hi2, ...]
// into a Trie of byte-range sequences.
func (c *utf8NodeCache) runeRangesToUTF8Trie(runes []rune, foldCase bool) []*UTF8Node {
	var roots []*UTF8Node
	if foldCase {
		// Expand each rune range to include its case-folded equivalents.
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
			roots = append(roots, c.decomposeRuneRange(r, r)...)
		}
		return roots
	}

	for i := 0; i+1 < len(runes); i += 2 {
		lo, hi := runes[i], runes[i+1]
		roots = append(roots, c.decomposeRuneRange(lo, hi)...)
	}
	// If there's a trailing rune, treat it as a single-rune range.
	if len(runes)%2 == 1 {
		r := runes[len(runes)-1]
		roots = append(roots, c.decomposeRuneRange(r, r)...)
	}
	return roots
}

func (c *utf8NodeCache) decomposeRuneRange(lo, hi rune) []*UTF8Node {
	var nodes []*UTF8Node
	for _, seq := range encodeRange(lo, hi) {
		nodes = append(nodes, c.sequenceToTrie(seq))
	}
	return nodes
}

func (c *utf8NodeCache) sequenceToTrie(seq []ByteRange) *UTF8Node {
	if len(seq) == 0 {
		return nil
	}
	return c.newNode([]ByteRange{seq[0]}, c.sequenceToTrieChildren(seq[1:]))
}

func (c *utf8NodeCache) sequenceToTrieChildren(seq []ByteRange) []*UTF8Node {
	if len(seq) == 0 {
		return nil
	}
	return []*UTF8Node{c.sequenceToTrie(seq)}
}

// encodeRange converts a rune range [lo, hi] into a set of byte-range sequences.
func encodeRange(lo, hi rune) [][]ByteRange {
	var sequences [][]ByteRange
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

func split(lo, hi rune, n int) [][]ByteRange {
	if n == 1 {
		return [][]ByteRange{{{byte(lo), byte(hi)}}}
	}

	var res [][]ByteRange
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

func combine(lo, hi rune, n int) []ByteRange {
	res := make([]ByteRange, n)
	for i := n - 1; i >= 0; i-- {
		if i == 0 {
			res[i] = firstByteRange(lo, hi, n)
		} else {
			res[i] = ByteRange{byte(0x80 | (lo & 0x3F)), byte(0x80 | (hi & 0x3F))}
			lo >>= 6
			hi >>= 6
		}
	}
	return res
}

func firstByteRange(lo, hi rune, n int) ByteRange {
	var mask, prefix rune
	switch n {
	case 2:
		mask, prefix = 0x1F, 0xC0
	case 3:
		mask, prefix = 0x0F, 0xE0
	case 4:
		mask, prefix = 0x07, 0xF0
	}
	return ByteRange{byte(prefix | (lo & mask)), byte(prefix | (hi & mask))}
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

func (c *utf8NodeCache) byteRangesToTrie(ranges []ByteRange) []*UTF8Node {
	return []*UTF8Node{
		c.newNode(ranges, nil),
	}
}

// anyRuneTrie returns a Trie that matches any valid UTF-8 rune OR any invalid UTF-8 byte.
func (c *utf8NodeCache) anyRuneTrie(includeNL bool) []*UTF8Node {
	var runes []rune
	if includeNL {
		runes = []rune{0, 0x10FFFF}
	} else {
		runes = []rune{0, '\n' - 1, '\n' + 1, 0x10FFFF}
	}
	roots := c.runeRangesToUTF8Trie(runes, false)

	// Add disjoint raw byte fallback for invalid UTF-8 bytes.
	// Valid UTF-8 starts are: 00-7F, C2-DF, E0-EF, F0-F4.
	// We exclude 80-BF (continuations) to avoid matching parts of a valid sequence as single runes.
	var br []ByteRange
	br = []ByteRange{
		{0xC0, 0xC1},
		{0xF5, 0xFF},
	}
	roots = append(roots, c.byteRangesToTrie(br)...)

	return roots
}
