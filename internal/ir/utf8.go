package ir

import (
	"unicode/utf8"
)

const UTF8MatchCompleted uint32 = 0xFFFFFFFF

type UTF8Transition struct {
	Lo, Hi byte
	Next   uint32
}

type UTF8Node struct {
	Transitions []UTF8Transition
}

type Trie struct {
	Nodes []UTF8Node
}

func NewTrie() *Trie {
	return &Trie{Nodes: []UTF8Node{{}}}
}

func (t *Trie) AddRuneRange(lo, hi rune) {
	ranges := runeRangeToUTF8(lo, hi)
	for _, seq := range ranges {
		t.addSequence(0, seq)
	}
}

func (t *Trie) addSequence(nodeID uint32, seq []utf8Range) {
	if len(seq) == 0 {
		return
	}
	r := seq[0]
	if len(seq) == 1 {
		t.addTransition(nodeID, r.lo, r.hi, UTF8MatchCompleted)
		return
	}

	var nextID uint32
	found := false
	for _, tr := range t.Nodes[nodeID].Transitions {
		if tr.Lo == r.lo && tr.Hi == r.hi && tr.Next != UTF8MatchCompleted {
			nextID = tr.Next
			found = true
			break
		}
	}

	if !found {
		nextID = uint32(len(t.Nodes))
		t.Nodes = append(t.Nodes, UTF8Node{})
		t.addTransition(nodeID, r.lo, r.hi, nextID)
	}
	t.addSequence(nextID, seq[1:])
}

func (t *Trie) addTransition(nodeID uint32, lo, hi byte, next uint32) {
	t.Nodes[nodeID].Transitions = append(t.Nodes[nodeID].Transitions, UTF8Transition{lo, hi, next})
}

func (t *Trie) GetTransitions(nodeID uint32, b byte) (next uint32, ok bool) {
	if nodeID >= uint32(len(t.Nodes)) {
		return 0, false
	}
	for _, tr := range t.Nodes[nodeID].Transitions {
		if b >= tr.Lo && b <= tr.Hi {
			return tr.Next, true
		}
	}
	return 0, false
}

type utf8Range struct {
	lo, hi byte
}

func runeRangeToUTF8(lo, hi rune) [][]utf8Range {
	var res [][]utf8Range
	rangeToUTF8(lo, hi, &res)
	return res
}

func rangeToUTF8(lo, hi rune, res *[][]utf8Range) {
	if lo > hi {
		return
	}

	var blo, bhi [utf8.UTFMax]byte
	for {
		nlo := utf8.EncodeRune(blo[:], lo)
		nhi := utf8.EncodeRune(bhi[:], hi)

		if nlo == nhi {
			split(blo[:nlo], bhi[:nhi], 0, nil, res)
			return
		}

		// Different lengths.
		max := maxRuneForBytes(nlo)
		var bmax [utf8.UTFMax]byte
		utf8.EncodeRune(bmax[:], max)
		split(blo[:nlo], bmax[:nlo], 0, nil, res)

		lo = max + 1
		if lo > hi || lo == 0 { // lo == 0 handles overflow if hi is MaxRune
			return
		}
	}
}

func split(lo, hi []byte, depth int, prefix []utf8Range, res *[][]utf8Range) {
	if depth == len(lo) {
		*res = append(*res, prefix)
		return
	}

	l, h := lo[depth], hi[depth]
	if l == h {
		nextPrefix := make([]utf8Range, len(prefix)+1)
		copy(nextPrefix, prefix)
		nextPrefix[len(prefix)] = utf8Range{l, l}
		split(lo, hi, depth+1, nextPrefix, res)
		return
	}

	// First byte range: [l, l]
	p1 := make([]utf8Range, len(prefix)+1)
	copy(p1, prefix)
	p1[len(prefix)] = utf8Range{l, l}
	if depth+1 < len(lo) {
		hi2 := make([]byte, len(lo))
		copy(hi2, lo)
		for i := depth + 1; i < len(lo); i++ {
			hi2[i] = 0xBF
		}
		split(lo[depth+1:], hi2[depth+1:], 0, p1, res)
	} else {
		*res = append(*res, p1)
	}

	// Middle byte ranges: [l+1, h-1]
	if l+1 <= h-1 {
		p2 := make([]utf8Range, len(prefix)+1)
		copy(p2, prefix)
		p2[len(prefix)] = utf8Range{l + 1, h - 1}
		for i := depth + 1; i < len(lo); i++ {
			p2 = append(p2, utf8Range{0x80, 0xBF})
		}
		*res = append(*res, p2)
	}

	// Last byte range: [h, h]
	p3 := make([]utf8Range, len(prefix)+1)
	copy(p3, prefix)
	p3[len(prefix)] = utf8Range{h, h}
	if depth+1 < len(hi) {
		lo2 := make([]byte, len(hi))
		copy(lo2, hi)
		for i := depth + 1; i < len(hi); i++ {
			lo2[i] = 0x80
		}
		split(lo2[depth+1:], hi[depth+1:], 0, p3, res)
	} else {
		*res = append(*res, p3)
	}
}

func maxRuneForBytes(n int) rune {
	switch n {
	case 1:
		return 0x7F
	case 2:
		return 0x7FF
	case 3:
		return 0xFFFF
	case 4:
		return 0x10FFFF
	}
	return 0
}

var anyRuneTrie *Trie

func GetAnyRuneTrie() *Trie {
	if anyRuneTrie != nil {
		return anyRuneTrie
	}
	t := NewTrie()
	t.AddRuneRange(0, utf8.MaxRune)
	anyRuneTrie = t
	return anyRuneTrie
}

var anyRuneNotNLTrie *Trie

func GetAnyRuneNotNLTrie() *Trie {
	if anyRuneNotNLTrie != nil {
		return anyRuneNotNLTrie
	}
	t := NewTrie()
	t.AddRuneRange(0, '\n'-1)
	t.AddRuneRange('\n'+1, utf8.MaxRune)
	anyRuneNotNLTrie = t
	return anyRuneNotNLTrie
}
