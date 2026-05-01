package ir

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/kamichidu/go-regexp-re/syntax"
)

type ClosureResult struct {
	NextClosure  []NFAPath
	Updates      []PathTagUpdate
	MatchAnchors syntax.EmptyOp
}

type dfaStateKey struct {
	hash          uint64
	matchPriority int
	isSearch      bool
}

func NewDFAWithMemoryLimit(ctx context.Context, re *syntax.Regexp, prog *syntax.Prog, maxMemory int, naked bool) (d *DFA, err error) {
	defer func() {
		if r := recover(); r != nil {
			if s, ok := r.(string); ok && strings.HasPrefix(s, "regexp: ") {
				err = fmt.Errorf("%s", s)
				return
			}
			panic(r)
		}
	}()
	if err := CheckCompatibility(re); err != nil {
		return nil, err
	}
	if err := checkEpsilonLoop(prog); err != nil {
		return nil, err
	}
	d = &DFA{
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
			if !naked {
				h = (h ^ uint64(p.Priority-minP)) * 1099511628211
				h = (h ^ uint64(p.Tags)) * 1099511628211
			}
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
	maxStates := maxMemory / 2048
	if maxStates < 100 {
		maxStates = 100
	}

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
			if matchP != 1<<30-1 {
				matchP -= int(minP)
			}
		}
		key := dfaStateKey{hashSet(closure, d.Naked), matchP, isSearch}
		if id, ok := nfaToDfa[key]; ok {
			return id
		}
		if d.numStates >= maxStates {
			panic(fmt.Sprintf("regexp: pattern too large or ambiguous (states: %d, max: %d)", d.numStates, maxStates))
		}
		id := uint32(d.numStates)
		nfaToDfa[key] = id
		_ = d.storage.Put(id, closure)
		d.stateIsSearch = append(d.stateIsSearch, isSearch)
		d.stateMinPriority = append(d.stateMinPriority, minP)
		d.stateMatchPriority = append(d.stateMatchPriority, matchP)
		d.stateEntryTags = append(d.stateEntryTags, updates)
		// A state has an unbeatable match only if its match priority is 0 AND
		// no other path (including restarts) has a lower priority.
		d.stateIsBestMatch = append(d.stateIsBestMatch, matchP == 0 && minP == 0)

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
				d.hasAnchors = true
			}

			minNextPrio := int32(1<<30 - 1)
			for _, p := range nextPaths {
				if p.Priority < minNextPrio {
					minNextPrio = p.Priority
				}
			}

			if len(nextRes.Updates) > 0 {
				d.tagUpdates = append(d.tagUpdates, TransitionUpdate{BasePriority: minNextPrio, PreUpdates: nextRes.Updates})
				d.tagUpdateIndices[idx] = uint32(len(d.tagUpdates) - 1)
				rawNext |= TaggedStateFlag
			}
			d.transitions[idx] = rawNext

			var entries []RecapEntry
			for _, u := range nextRes.Updates {
				entries = append(entries, RecapEntry{
					InputPriority: u.RelativePriority + minNextPrio,
					NextPriority:  u.NextPriority,
					IsMatch:       u.IsMatch,
					PreTags:       u.PreTags,
					PostTags:      u.PostTags,
				})
			}
			d.recapTables[0].Transitions[idx] = entries
		}
	}

	for _, g := range d.acceptingGuards {
		if g != 0 {
			d.hasAnchors = true
			break
		}
	}

	// CCWarp Detection
	d.ccWarpTable = make([]CCWarpInfo, d.numStates)
	for i := 0; i < d.numStates; i++ {
		var selfLoops [256]bool
		count := 0
		for b := 0; b < 128; b++ {
			idx := (i << 8) | b
			if idx >= len(d.transitions) {
				continue
			}
			next := d.transitions[idx]
			if next == InvalidState {
				continue
			}
			if (next & StateIDMask) != uint32(i) {
				continue
			}

			// No tags or anchors on self-loop
			hasRealTags := false
			if (next & TaggedStateFlag) != 0 {
				uIdx := d.tagUpdateIndices[idx]
				if uIdx != 0xFFFFFFFF {
					update := d.tagUpdates[uIdx]
					if update.BasePriority != 0 {
						hasRealTags = true
					}
					for _, u := range update.PreUpdates {
						if u.PreTags|u.PostTags != 0 {
							hasRealTags = true
							break
						}
					}
				}
			}
			if !hasRealTags && (next&AnchorVerifyFlag) == 0 {
				selfLoops[b] = true
				count++
			}
		}

		if count == 0 {
			continue
		}
		if count == 128 {
			d.ccWarpTable[i] = CCWarpInfo{Kernel: CCWarpAnyChar}
		} else {
			low, high := -1, -1
			isSingleRange := true
			for b := 0; b < 128; b++ {
				if selfLoops[b] {
					if low == -1 {
						low = b
					}
					high = b
				} else if low != -1 {
					for j := b + 1; j < 128; j++ {
						if selfLoops[j] {
							isSingleRange = false
							break
						}
					}
					break
				}
			}
			if isSingleRange && low != -1 {
				d.ccWarpTable[i] = CCWarpInfo{
					Kernel: CCWarpSingleRange,
					V0:     uint64(low),
					V1:     uint64(high),
				}
			}
		}

		// Apply flag to self-loops
		if d.ccWarpTable[i].Kernel != CCWarpNone {
			for b := 0; b < 256; b++ {
				idx := (i << 8) | b
				if idx < len(d.transitions) && (d.transitions[idx]&StateIDMask) == uint32(i) {
					d.transitions[idx] |= CCWarpFlag
				}
			}
		}
	}

	// SearchWarp Pre-filter
	searchIdx := int(d.searchState & StateIDMask)
	var firstBytes [2]uint64
	searchCount := 0
	for b := 0; b < 128; b++ {
		idx := (searchIdx << 8) | b
		if idx < len(d.transitions) && d.transitions[idx] != InvalidState {
			firstBytes[b>>6] |= 1 << (b & 63)
			searchCount++
		}
	}
	if searchCount > 0 && searchCount < 64 {
		low, high := -1, -1
		isSingleRange := true
		for b := 0; b < 128; b++ {
			if (firstBytes[b>>6] & (1 << (b & 63))) != 0 {
				if low == -1 {
					low = b
				}
				high = b
			} else if low != -1 {
				for j := b + 1; j < 128; j++ {
					if (firstBytes[j>>6] & (1 << (j & 63))) != 0 {
						isSingleRange = false
						break
					}
				}
				break
			}
		}
		if isSingleRange && low != -1 {
			var chars []byte
			for b := low; b <= high; b++ {
				chars = append(chars, byte(b))
			}
			d.searchWarp = CCWarpInfo{
				Kernel:   CCWarpSingleRange,
				V0:       uint64(low),
				V1:       uint64(high),
				IndexAny: string(chars),
			}
		}
	}

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
		preTags    uint64
	}
	stack := make([]pathWithMeta, 0, len(paths))
	var updates []PathTagUpdate
	for _, p := range paths {
		sk := stateKey{p.ID, p.NodeID, p.Anchors}
		if _, ok := minPriority[p.Priority]; !ok {
			minPriority[p.Priority] = make(map[stateKey]int32)
		}
		minPriority[p.Priority][sk] = p.Priority
		stack = append(stack, pathWithMeta{p, 0, p.Priority, p.Tags})
		inst := prog.Inst[p.ID]
		if p.NodeID != 0 || !isEpsilon(inst.Op) {
			updates = append(updates, PathTagUpdate{RelativePriority: p.Priority, NextPriority: p.Priority, IsMatch: p.NodeID == 0 && inst.Op == syntax.InstMatch, PreTags: p.Tags, PostTags: 0})
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
			rk := stateKey{p.ID, p.NodeID, p.Anchors}
			updates = append(updates, PathTagUpdate{
				RelativePriority: ph.sourcePrio,
				NextPriority:     p.Priority,
				IsMatch:          p.NodeID == 0 && inst.Op == syntax.InstMatch,
				PreTags:          ph.preTags,
				PostTags:         ph.newTags,
			})
			p.Tags = ph.preTags | ph.newTags
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
					stack = append(stack, pathWithMeta{
						p:          NFAPath{ID: next.id, Priority: next.p, Anchors: p.Anchors, Tags: p.Tags},
						newTags:    ph.newTags,
						sourcePrio: ph.sourcePrio,
						preTags:    ph.preTags,
					})
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
				nt := ph.newTags | tagBit
				stack = append(stack, pathWithMeta{
					p:          NFAPath{ID: inst.Out, Priority: p.Priority, Anchors: p.Anchors, Tags: p.Tags | tagBit},
					newTags:    nt,
					sourcePrio: ph.sourcePrio,
					preTags:    ph.preTags,
				})
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
				stack = append(stack, pathWithMeta{
					p:          NFAPath{ID: inst.Out, Priority: p.Priority, Anchors: na, Tags: p.Tags},
					newTags:    ph.newTags,
					sourcePrio: ph.sourcePrio,
					preTags:    ph.preTags,
				})
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
	type updateKey struct {
		rel, next int32
		isMatch   bool
	}
	dedup := make(map[updateKey]PathTagUpdate)
	for _, u := range updates {
		k := updateKey{u.RelativePriority, u.NextPriority, u.IsMatch}
		existing := dedup[k]
		existing.RelativePriority, existing.NextPriority, existing.IsMatch = u.RelativePriority, u.NextPriority, u.IsMatch
		existing.PreTags |= u.PreTags
		existing.PostTags |= u.PostTags
		dedup[k] = existing
	}
	updates = updates[:0]
	keys := make([]updateKey, 0, len(dedup))
	for k := range dedup {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].rel != keys[j].rel {
			return keys[i].rel < keys[j].rel
		}
		if keys[i].next != keys[j].next {
			return keys[i].next < keys[j].next
		}
		return !keys[i].isMatch && keys[j].isMatch
	})
	for _, k := range keys {
		updates = append(updates, dedup[k])
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
		if !naked {
			h = (h ^ uint64(p.Priority)) * 1099511628211
			h = (h ^ uint64(p.Tags)) * 1099511628211
		}
		h = (h ^ uint64(p.Anchors)) * 1099511628211
	}
	return h
}
