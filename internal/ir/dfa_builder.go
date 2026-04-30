package ir

import (
	"context"
	"sort"
	"unicode"

	"github.com/kamichidu/go-regexp-re/syntax"
)

type ClosureResult struct {
	NextClosure  []NFAPath
	Updates      []PathTagUpdate
	MatchAnchors syntax.EmptyOp
}

type dfaStateKey struct {
	Hash     uint64
	MatchP   int
	IsSearch bool
}

func NewDFAWithMemoryLimit(ctx context.Context, re *syntax.Regexp, prog *syntax.Prog, maxMemory int, naked bool) (*DFA, error) {
	d := &DFA{
		storage: &memoryNfaSetStorage{},
		Naked:   naked,
	}

	instructionTries := make([]*Trie, len(prog.Inst))
	for id, inst := range prog.Inst {
		if isEpsilon(inst.Op) {
			continue
		}
		t := NewTrie()
		foldCase := (inst.Arg & 1) != 0
		if inst.Op == syntax.InstRune {
			if len(inst.Rune) == 1 && foldCase {
				r := inst.Rune[0]
				for {
					t.AddRuneRange(r, r)
					r = unicode.SimpleFold(r)
					if r == inst.Rune[0] {
						break
					}
				}
			} else {
				for i := 0; i+1 < len(inst.Rune); i += 2 {
					t.AddRuneRange(inst.Rune[i], inst.Rune[i+1])
				}
			}
		} else if inst.Op == syntax.InstRune1 {
			t.AddRuneRange(inst.Rune[0], inst.Rune[0])
		} else if inst.Op == syntax.InstRuneAny {
			t.AddRuneRange(0, unicode.MaxRune)
			t.AddInvalidUTF8()
		} else if inst.Op == syntax.InstRuneAnyNotNL {
			t.AddRuneRange(0, '\n'-1)
			t.AddRuneRange('\n'+1, unicode.MaxRune)
			t.AddInvalidUTF8()
		}
		instructionTries[id] = t
	}

	closureCache := make(map[uint64]ClosureResult)
	getCachedClosure := func(paths []NFAPath) ClosureResult {
		if len(paths) == 0 {
			return ClosureResult{}
		}
		minP := paths[0].Priority
		for _, p := range paths {
			if p.Priority < minP {
				minP = p.Priority
			}
		}
		h := uint64(14695981039346656037)
		for _, p := range paths {
			h = (h ^ uint64(p.ID)) * 1099511628211
			h = (h ^ uint64(p.NodeID)) * 1099511628211
			h = (h ^ uint64(p.Priority-minP)) * 1099511628211
			h = (h ^ uint64(p.Tags)) * 1099511628211
			h = (h ^ uint64(p.Anchors)) * 1099511628211
		}
		if res, ok := closureCache[h]; ok {
			return res
		}
		normPaths := make([]NFAPath, len(paths))
		copy(normPaths, paths)
		for i := range normPaths {
			normPaths[i].Priority -= minP
		}
		res := epsilonClosureWithAnchorWall(prog, normPaths)
		closureCache[h] = res
		return res
	}

	nfaToDfa := make(map[dfaStateKey]uint32)
	addDfaState := func(closure []NFAPath, updates []PathTagUpdate, matchAnchors syntax.EmptyOp, isSearch bool) uint32 {
		minP := int32(1<<30 - 1)
		matchP := 1<<30 - 1
		for _, s := range closure {
			if s.Priority < minP {
				minP = s.Priority
			}
			if prog.Inst[s.ID].Op == syntax.InstMatch && s.NodeID == 0 {
				if int(s.Priority) < matchP {
					matchP = int(s.Priority)
				}
			}
		}
		if len(closure) > 0 {
			normalized := make([]NFAPath, len(closure))
			copy(normalized, closure)
			for i := range normalized {
				normalized[i].Priority -= minP
			}
			closure = normalized
			matchP -= int(minP)
		}
		key := dfaStateKey{hashSet(closure, d.Naked), matchP, isSearch}
		if id, ok := nfaToDfa[key]; ok {
			return id
		}
		id := uint32(d.numStates)
		nfaToDfa[key] = id
		_ = d.storage.Put(id, closure)
		d.stateIsSearch = append(d.stateIsSearch, isSearch)
		d.stateMinPriority = append(d.stateMinPriority, minP)
		d.stateMatchPriority = append(d.stateMatchPriority, matchP)
		d.stateEntryTags = append(d.stateEntryTags, updates)
		d.stateIsBestMatch = append(d.stateIsBestMatch, matchP != 1<<30-1 && matchP <= 0)
		d.accepting = append(d.accepting, matchP != 1<<30-1)
		d.acceptingGuards = append(d.acceptingGuards, matchAnchors)
		d.numStates++
		return id
	}

	startRes := getCachedClosure([]NFAPath{{ID: uint32(prog.Start), Priority: 0}})
	d.matchState = addDfaState(startRes.NextClosure, startRes.Updates, startRes.MatchAnchors, false)
	d.startUpdates = startRes.Updates
	d.searchState = addDfaState(startRes.NextClosure, startRes.Updates, startRes.MatchAnchors, false)

	d.recapTables = []GroupRecapTable{{Transitions: make([][]RecapEntry, 0, 1024)}}

	for i := 0; i < d.numStates; i++ {
		closure, _ := d.storage.Get(uint32(i), nil)
		for b := 0; b < 256; b++ {
			idx := (i << 8) | b
			for len(d.recapTables[0].Transitions) <= idx {
				d.recapTables[0].Transitions = append(d.recapTables[0].Transitions, nil)
			}
			for len(d.transitions) <= idx {
				d.transitions = append(d.transitions, InvalidState)
			}
			for len(d.tagUpdateIndices) <= idx {
				d.tagUpdateIndices = append(d.tagUpdateIndices, 0xFFFFFFFF)
			}

			nextPaths := make([]NFAPath, 0, len(closure))
			var nextAnchors syntax.EmptyOp
			for _, p := range closure {
				t := instructionTries[p.ID]
				if t == nil {
					continue
				}
				for _, tr := range t.Nodes[p.NodeID].Transitions {
					if byte(b) >= tr.Lo && byte(b) <= tr.Hi {
						nextID, nextNodeID := p.ID, tr.Next
						if tr.Next == 0xFFFFFFFF {
							nextID = uint32(prog.Inst[p.ID].Out)
							nextNodeID = 0
						}
						nextPaths = append(nextPaths, NFAPath{ID: nextID, NodeID: nextNodeID, Priority: p.Priority, Tags: p.Tags})
						nextAnchors |= p.Anchors
						break
					}
				}
			}
			if len(nextPaths) == 0 {
				continue
			}

			nextRes := getCachedClosure(nextPaths)
			nextDfaID := addDfaState(nextRes.NextClosure, nextRes.Updates, nextRes.MatchAnchors, d.stateIsSearch[i])
			rawNext := nextDfaID
			if d.accepting[nextDfaID] {
				rawNext |= AcceptingStateFlag
			}
			if nextAnchors != 0 {
				rawNext |= AnchorVerifyFlag | (uint32(nextAnchors) << 22)
			}

			if len(nextRes.Updates) > 0 {
				minNextPrio := nextRes.NextClosure[0].Priority
				for _, p := range nextRes.NextClosure {
					if p.Priority < minNextPrio {
						minNextPrio = p.Priority
					}
				}
				basePrio := minNextPrio // Simplified for now
				d.tagUpdates = append(d.tagUpdates, TransitionUpdate{BasePriority: basePrio, PreUpdates: nextRes.Updates})
				d.tagUpdateIndices[idx] = uint32(len(d.tagUpdates) - 1)
				rawNext |= TaggedStateFlag
			}
			d.transitions[idx] = rawNext

			var entries []RecapEntry
			for _, u := range nextRes.Updates {
				var pre uint64
				for _, eu := range d.stateEntryTags[i] {
					if eu.NextPriority == u.RelativePriority {
						pre |= eu.Tags
					}
				}
				entries = append(entries, RecapEntry{InputPriority: int16(u.RelativePriority), NextPriority: int16(u.NextPriority), PreTags: pre, PostTags: u.Tags})
			}
			d.recapTables[0].Transitions[idx] = entries
		}
	}
	d.ccWarpTable = make([]CCWarpInfo, d.numStates)
	return d, nil
}

func epsilonClosureWithAnchorWall(prog *syntax.Prog, paths []NFAPath) ClosureResult {
	type stateKey struct {
		ID, NodeID uint32
		Anchors    syntax.EmptyOp
	}
	minPriority := make(map[int32]map[stateKey]int32)
	type pathWithMeta struct {
		p          NFAPath
		newTags    uint64
		sourcePrio int32
	}
	stack := make([]pathWithMeta, 0, len(paths))
	var updates []PathTagUpdate
	for _, p := range paths {
		sk := stateKey{p.ID, p.NodeID, p.Anchors}
		if _, ok := minPriority[p.Priority]; !ok {
			minPriority[p.Priority] = make(map[stateKey]int32)
		}
		minPriority[p.Priority][sk] = p.Priority
		stack = append(stack, pathWithMeta{p, 0, p.Priority})
		inst := prog.Inst[p.ID]
		if p.NodeID != 0 || !isEpsilon(inst.Op) {
			updates = append(updates, PathTagUpdate{RelativePriority: p.Priority, NextPriority: p.Priority, Tags: 0})
		}
	}
	resMap := make(map[stateKey]NFAPath)
	var matchAnchors syntax.EmptyOp
	for len(stack) > 0 {
		ph := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		p := ph.p
		if p.Priority > minPriority[ph.sourcePrio][stateKey{p.ID, p.NodeID, p.Anchors}] {
			continue
		}
		inst := prog.Inst[p.ID]
		if p.NodeID != 0 || !isEpsilon(inst.Op) {
			p.Tags |= ph.newTags
			updates = append(updates, PathTagUpdate{RelativePriority: ph.sourcePrio, NextPriority: p.Priority, Tags: ph.newTags})
			rk := stateKey{p.ID, p.NodeID, p.Anchors}
			if existing, ok := resMap[rk]; !ok || p.Priority < existing.Priority {
				resMap[rk] = p
			} else if p.Priority == existing.Priority {
				existing.Tags |= p.Tags
				resMap[rk] = existing
			}
			if p.NodeID == 0 && inst.Op == syntax.InstMatch {
				matchAnchors |= p.Anchors
			}
			continue
		}
		switch inst.Op {
		case syntax.InstAlt, syntax.InstAltMatch:
			for _, next := range []struct {
				id uint32
				p  int32
			}{{inst.Arg, p.Priority + 1}, {inst.Out, p.Priority}} {
				nsk := stateKey{next.id, 0, p.Anchors}
				if _, ok := minPriority[ph.sourcePrio]; !ok {
					minPriority[ph.sourcePrio] = make(map[stateKey]int32)
				}
				if m, ok := minPriority[ph.sourcePrio][nsk]; !ok || next.p <= m {
					minPriority[ph.sourcePrio][nsk] = next.p
					stack = append(stack, pathWithMeta{NFAPath{ID: next.id, Priority: next.p, Anchors: p.Anchors, Tags: p.Tags}, ph.newTags, ph.sourcePrio})
				}
			}
		case syntax.InstCapture:
			tagBit := uint64(1 << inst.Arg)
			nsk := stateKey{inst.Out, 0, p.Anchors}
			if _, ok := minPriority[ph.sourcePrio]; !ok {
				minPriority[ph.sourcePrio] = make(map[stateKey]int32)
			}
			if m, ok := minPriority[ph.sourcePrio][nsk]; !ok || p.Priority <= m {
				minPriority[ph.sourcePrio][nsk] = p.Priority
				nt := ph.newTags
				if (inst.Arg&1 != 0) || (p.Tags&tagBit) == 0 {
					nt |= tagBit
				}
				stack = append(stack, pathWithMeta{NFAPath{ID: inst.Out, Priority: p.Priority, Anchors: p.Anchors, Tags: p.Tags | tagBit}, nt, ph.sourcePrio})
			}
		case syntax.InstEmptyWidth, syntax.InstNop:
			na := p.Anchors
			if inst.Op == syntax.InstEmptyWidth {
				na |= syntax.EmptyOp(inst.Arg)
			}
			nsk := stateKey{inst.Out, 0, na}
			if _, ok := minPriority[ph.sourcePrio]; !ok {
				minPriority[ph.sourcePrio] = make(map[stateKey]int32)
			}
			if m, ok := minPriority[ph.sourcePrio][nsk]; !ok || p.Priority <= m {
				minPriority[ph.sourcePrio][nsk] = p.Priority
				stack = append(stack, pathWithMeta{NFAPath{ID: inst.Out, Priority: p.Priority, Anchors: na, Tags: p.Tags}, ph.newTags, ph.sourcePrio})
			}
		}
	}
	var resPaths []NFAPath
	for _, p := range resMap {
		resPaths = append(resPaths, p)
	}
	if len(resPaths) > 0 {
		minT := resPaths[0].Priority
		for _, p := range resPaths {
			if p.Priority < minT {
				minT = p.Priority
			}
		}
		for i := range resPaths {
			resPaths[i].Priority -= minT
		}
		for i := range updates {
			updates[i].NextPriority -= minT
		}
	}
	sort.Slice(resPaths, func(i, j int) bool {
		if resPaths[i].ID != resPaths[j].ID {
			return resPaths[i].ID < resPaths[j].ID
		}
		return resPaths[i].Priority < resPaths[j].Priority
	})
	type updateKey struct{ rel, next int32 }
	dedup := make(map[updateKey]uint64)
	for _, u := range updates {
		dedup[updateKey{u.RelativePriority, u.NextPriority}] |= u.Tags
	}
	updates = updates[:0]
	for k, v := range dedup {
		updates = append(updates, PathTagUpdate{RelativePriority: k.rel, NextPriority: k.next, Tags: v})
	}
	return ClosureResult{resPaths, updates, matchAnchors}
}

func isEpsilon(op syntax.InstOp) bool {
	switch op {
	case syntax.InstAlt, syntax.InstAltMatch, syntax.InstCapture, syntax.InstEmptyWidth, syntax.InstNop:
		return true
	}
	return false
}

func hashSet(closure []NFAPath, naked bool) uint64 {
	h := uint64(14695981039346656037)
	for _, p := range closure {
		h = (h ^ uint64(p.ID)) * 1099511628211
		h = (h ^ uint64(p.NodeID)) * 1099511628211
		h = (h ^ uint64(p.Priority)) * 1099511628211
		if !naked {
			h = (h ^ uint64(p.Tags)) * 1099511628211
		}
		h = (h ^ uint64(p.Anchors)) * 1099511628211
	}
	return h
}
