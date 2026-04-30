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
	Hash     uint64
	MatchP   int
	IsSearch bool
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
					InputPriority: u.RelativePriority + minNextPrio - d.stateMinPriority[i],
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
			updates = append(updates, PathTagUpdate{RelativePriority: ph.sourcePrio, NextPriority: p.Priority, IsMatch: p.NodeID == 0 && inst.Op == syntax.InstMatch, PreTags: ph.preTags, PostTags: ph.newTags})
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
					stack = append(stack, pathWithMeta{NFAPath{ID: next.id, Priority: next.p, Anchors: p.Anchors, Tags: p.Tags}, ph.newTags, ph.sourcePrio, ph.preTags})
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
				stack = append(stack, pathWithMeta{NFAPath{ID: inst.Out, Priority: p.Priority, Anchors: p.Anchors, Tags: p.Tags | tagBit}, nt, ph.sourcePrio, ph.preTags})
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
				stack = append(stack, pathWithMeta{NFAPath{ID: inst.Out, Priority: p.Priority, Anchors: na, Tags: p.Tags}, ph.newTags, ph.sourcePrio, ph.preTags})
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

func CheckCompatibility(re *syntax.Regexp) error {
	if re == nil {
		return nil
	}
	switch re.Op {
	case syntax.OpCapture:
		if hasEmptyAlternative(re.Sub[0]) {
			return &syntax.UnsupportedError{Op: "empty alternative in capture"}
		}
	case syntax.OpQuest, syntax.OpStar, syntax.OpPlus, syntax.OpRepeat:
		if hasCapture(re.Sub[0]) && matchesEmpty(re.Sub[0]) {
			return &syntax.UnsupportedError{Op: "optional empty capture"}
		}
	}
	for _, sub := range re.Sub {
		if err := CheckCompatibility(sub); err != nil {
			return err
		}
	}
	return nil
}

func hasCapture(re *syntax.Regexp) bool {
	if re.Op == syntax.OpCapture {
		return true
	}
	for _, sub := range re.Sub {
		if hasCapture(sub) {
			return true
		}
	}
	return false
}

func hasEmptyAlternative(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpEmptyMatch:
		return true
	case syntax.OpCapture:
		return hasEmptyAlternative(re.Sub[0])
	case syntax.OpAlternate:
		for _, sub := range re.Sub {
			if hasEmptyAlternative(sub) {
				return true
			}
		}
	case syntax.OpConcat:
		if len(re.Sub) == 0 {
			return true
		}
		for _, sub := range re.Sub {
			if !hasEmptyAlternative(sub) {
				return false
			}
		}
		return true
	}
	return false
}

func matchesEmpty(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpEmptyMatch, syntax.OpStar, syntax.OpQuest:
		return true
	case syntax.OpRepeat:
		if re.Min == 0 {
			return true
		}
	case syntax.OpCapture, syntax.OpAlternate:
		for _, sub := range re.Sub {
			if matchesEmpty(sub) {
				return true
			}
		}
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			if !matchesEmpty(sub) {
				return false
			}
		}
		return true
	}
	return false
}

func checkEpsilonLoop(prog *syntax.Prog) error {
	for i := range prog.Inst {
		if hasEpsilonCycle(prog, uint32(i), make(map[uint32]bool), make(map[uint32]bool)) {
			return &syntax.UnsupportedError{Op: "epsilon loop"}
		}
	}
	return nil
}

func hasEpsilonCycle(prog *syntax.Prog, id uint32, visited, onStack map[uint32]bool) bool {
	if onStack[id] {
		return true
	}
	if visited[id] {
		return false
	}
	visited[id] = true
	onStack[id] = true
	defer func() { onStack[id] = false }()
	inst := prog.Inst[id]
	if !isEpsilon(inst.Op) {
		return false
	}
	switch inst.Op {
	case syntax.InstAlt, syntax.InstAltMatch:
		return hasEpsilonCycle(prog, inst.Out, visited, onStack) || hasEpsilonCycle(prog, inst.Arg, visited, onStack)
	case syntax.InstCapture, syntax.InstEmptyWidth, syntax.InstNop:
		return hasEpsilonCycle(prog, inst.Out, visited, onStack)
	}
	return false
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
