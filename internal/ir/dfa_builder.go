package ir

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kamichidu/go-regexp-re/syntax"
)

type ClosureResult struct {
	NextClosure  []NFAPath
	Updates      []PathTagUpdate
	MatchAnchors syntax.EmptyOp
}

type dfaStateKey struct {
	hash          [2]uint64
	matchPriority int
	isSearch      bool
}

func (d *DFA) build(ctx context.Context, s *syntax.Regexp, prog *syntax.Prog, maxMemory int) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if s, ok := r.(string); ok && strings.HasPrefix(s, "regexp: pattern too large or ambiguous") {
				err = fmt.Errorf("%s", s)
			} else {
				panic(r)
			}
		}
	}()
	if err := checkCompatibility(s); err != nil {
		return err
	}
	if err := checkEpsilonLoop(prog); err != nil {
		return err
	}
	for _, inst := range prog.Inst {
		if inst.Op == syntax.InstEmptyWidth {
			d.hasAnchors = true
			break
		}
	}
	d.maskStride = (len(prog.Inst) + 63) / 64
	d.storage = &memoryNfaSetStorage{data: make([][]NFAPath, 1024)}
	nfaToDfa := make(map[dfaStateKey]uint32)
	closureCache := make(map[uint64]ClosureResult)
	getCachedClosure := func(paths []NFAPath) ClosureResult {
		if len(paths) == 0 {
			return ClosureResult{}
		}
		// Calculate the minimum priority to make the hash invariant to absolute offsets
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
			// Use normalized priority for the hash
			h = (h ^ uint64(p.Priority-minP)) * 1099511628211
			h = (h ^ uint64(p.Tags)) * 1099511628211
			h = (h ^ uint64(p.Anchors)) * 1099511628211
		}
		if res, ok := closureCache[h]; ok {
			// Return a copy of Updates to prevent callers from corrupting the cache
			updatesCopy := make([]PathTagUpdate, len(res.Updates))
			copy(updatesCopy, res.Updates)
			resCopy := res
			resCopy.Updates = updatesCopy
			return resCopy
		}

		// Create a normalized copy of paths for epsilonClosure
		normPaths := make([]NFAPath, len(paths))
		copy(normPaths, paths)
		for i := range normPaths {
			normPaths[i].Priority -= minP
		}

		res := epsilonClosureWithAnchorWall(prog, normPaths)
		closureCache[h] = res

		// Return a copy to prevent accidental corruption
		updatesCopy := make([]PathTagUpdate, len(res.Updates))
		copy(updatesCopy, res.Updates)
		resCopy := res
		resCopy.Updates = updatesCopy
		return resCopy
	}
	maxStates := maxMemory / 2048
	if maxStates < 100 {
		maxStates = 100
	}

	addDfaState := func(closure []NFAPath, updates []PathTagUpdate, matchAnchors syntax.EmptyOp, isSearch bool) uint32 {
		minP := int32(1<<30 - 1)
		matchP := 1<<30 - 1
		if len(closure) > 0 {
			for _, s := range closure {
				if s.Priority < minP {
					minP = s.Priority
				}
			}
			// Clone and normalize closure priorities.
			normalizedClosure := make([]NFAPath, len(closure))
			copy(normalizedClosure, closure)
			for i := range normalizedClosure {
				normalizedClosure[i].Priority -= minP
				if prog.Inst[normalizedClosure[i].ID].Op == syntax.InstMatch && normalizedClosure[i].NodeID == 0 {
					if int(normalizedClosure[i].Priority) < matchP {
						matchP = int(normalizedClosure[i].Priority)
					}
				}
			}
			closure = normalizedClosure
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
		d.storage.Put(id, closure)
		d.stateIsSearch = append(d.stateIsSearch, isSearch)

		isAcc := matchP != 1<<30-1
		d.stateMinPriority = append(d.stateMinPriority, minP)
		d.stateMatchPriority = append(d.stateMatchPriority, matchP)
		d.stateEntryTags = append(d.stateEntryTags, updates)
		d.stateIsBestMatch = append(d.stateIsBestMatch, isAcc && matchP <= int(minP))

		d.accepting = append(d.accepting, isAcc)
		d.acceptingGuards = append(d.acceptingGuards, matchAnchors)
		for i := 0; i < 256; i++ {
			d.transitions = append(d.transitions, InvalidState)
		}
		d.numStates++
		return id
	}

	startRes := getCachedClosure([]NFAPath{{ID: uint32(prog.Start), Priority: 0}})
	d.matchState = addDfaState(startRes.NextClosure, startRes.Updates, startRes.MatchAnchors, false)
	if d.accepting[d.matchState] {
		d.matchState |= AcceptingStateFlag
	}
	d.startUpdates = startRes.Updates
	d.searchState = addDfaState(startRes.NextClosure, startRes.Updates, startRes.MatchAnchors, false)
	if d.accepting[d.searchState] {
		d.searchState |= AcceptingStateFlag
	}

	d.recapTables = []GroupRecapTable{{Transitions: make([][]RecapEntry, 0, 1024)}}
	d.tagUpdateIndices = make([]uint32, 0, 1024)
	d.tagUpdates = make([]TransitionUpdate, 0, 1024)

	// Pre-calculate Tries for all instructions that need them.
	// This avoids expensive map lookups and string formatting in the hot loop.
	instructionTries := make([]*Trie, len(prog.Inst))
	contentTries := make(map[string]*Trie)
	for id, inst := range prog.Inst {
		var t *Trie
		var key string
		switch inst.Op {
		case syntax.InstRune, syntax.InstRune1:
			key = fmt.Sprintf("%d:%d:%v", inst.Op, inst.Arg&1, inst.Rune)
		case syntax.InstRuneAny:
			t = GetAnyRuneTrie()
		case syntax.InstRuneAnyNotNL:
			t = GetAnyRuneNotNLTrie()
		default:
			continue
		}

		if t != nil {
			instructionTries[id] = t
			continue
		}

		if cached, ok := contentTries[key]; ok {
			instructionTries[id] = cached
			continue
		}

		t = NewTrie()
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
		} else { // InstRune1
			if len(inst.Rune) > 0 {
				if foldCase {
					r := inst.Rune[0]
					for {
						t.AddRuneRange(r, r)
						r = unicode.SimpleFold(r)
						if r == inst.Rune[0] {
							break
						}
					}
				} else {
					t.AddRuneRange(inst.Rune[0], inst.Rune[0])
				}
			}
		}
		contentTries[key] = t
		instructionTries[id] = t
	}

	scratchBuf := make([]NFAPath, 0, 1024)
	nextPaths := make([]NFAPath, 0, 1024)

	processed := 0
	for processed < d.numStates {
		i := uint32(processed)
		processed++
		currentClosure, _ := d.storage.Get(i, scratchBuf)
		scratchBuf = currentClosure

		for b := 0; b < 256; b++ {
			nextPaths = nextPaths[:0]
			var preGuard syntax.EmptyOp

			// Determine if this transition is warpable.
			isWarpable := b >= 0x80
			hasMultiByte := false
			if isWarpable {
				for _, p := range currentClosure {
					if p.NodeID != 0 {
						continue
					}
					t := instructionTries[p.ID]
					if t != nil {
						if _, ok := t.GetTransitions(p.NodeID, byte(b)); ok {
							hasMultiByte = true
							if t != GetAnyRuneTrie() && t != GetAnyRuneNotNLTrie() {
								isWarpable = false
								break
							}
						}
					}
				}
			}
			isWarpable = isWarpable && hasMultiByte

			minPrioForByte := int32(1<<30 - 1)
			for _, p := range currentClosure {
				t := instructionTries[p.ID]
				if t != nil {
					if _, ok := t.GetTransitions(p.NodeID, byte(b)); ok {
						if p.Priority < minPrioForByte {
							minPrioForByte = p.Priority
						}
					}
				}
			}

			for _, p := range currentClosure {
				inst := prog.Inst[p.ID]
				match := false
				var nextNodeID uint32

				t := instructionTries[p.ID]
				if t != nil {
					if next, ok := t.GetTransitions(p.NodeID, byte(b)); ok {
						match = true
						nextNodeID = next
					}
				}

				if match {
					// Only allow the HIGHEST priority paths to set the anchor requirements (preGuard).
					// This prevents lower-priority paths (like search restarts) from adding
					// restrictive anchors that would invalidate a valid high-priority transition.
					if p.Priority == minPrioForByte {
						preGuard |= p.Anchors
					}

					nextID := inst.Out
					if nextNodeID != UTF8MatchCompleted && !isWarpable {
						nextID = p.ID
					} else {
						nextNodeID = 0
					}

					// Merge with existing path in nextPaths if ID/Priority/NodeID match.
					// Anchors are always 0 for next paths because requirements were checked at the current byte.
					found := false
					for j := range nextPaths {
						if nextPaths[j].ID == nextID && nextPaths[j].NodeID == nextNodeID && nextPaths[j].Priority == p.Priority {
							found = true
							break
						}
					}
					if !found {
						nextPaths = append(nextPaths, NFAPath{ID: nextID, NodeID: nextNodeID, Priority: p.Priority, Tags: 0, Anchors: 0})
					}
				}
			}
			if len(nextPaths) == 0 {
				continue
			}

			nextRes := getCachedClosure(nextPaths)
			if len(nextRes.NextClosure) == 0 {
				continue
			}

			nextDfaID := addDfaState(nextRes.NextClosure, nextRes.Updates, nextRes.MatchAnchors, d.stateIsSearch[i])

			idx := (int(i) << 8) | b
			rawNext := nextDfaID
			if d.accepting[nextDfaID] {
				rawNext |= AcceptingStateFlag
			}
			if preGuard != 0 {
				rawNext |= AnchorVerifyFlag | (uint32(preGuard) << 22)
			}

			if isWarpable {
				rawNext |= WarpStateFlag
			}

			// The penalty/priority shift incurred during normalization of the next state.
			minNextPrio := int32(1<<30 - 1)
			for _, p := range nextPaths {
				if p.Priority < minNextPrio {
					minNextPrio = p.Priority
				}
			}

			for len(d.tagUpdateIndices) <= idx {
				d.tagUpdateIndices = append(d.tagUpdateIndices, 0xFFFFFFFF)
			}

			// Always record updates in RecapTable to ensure Pass 2 can trace the path
			if len(nextRes.Updates) > 0 {
				uIdx := uint32(len(d.tagUpdates))
				basePrio := minNextPrio - d.stateMinPriority[i]
				d.tagUpdates = append(d.tagUpdates, TransitionUpdate{
					BasePriority: basePrio,
					PreUpdates:   nextRes.Updates,
				})
				d.tagUpdateIndices[idx] = uIdx

				hasTags := false
				for _, u := range nextRes.Updates {
					if u.Tags != 0 {
						hasTags = true
						break
					}
				}
				if !hasTags {
					for _, u := range nextRes.Updates {
						for _, eu := range d.stateEntryTags[i] {
							if eu.NextPriority == u.RelativePriority && eu.Tags != 0 {
								hasTags = true
								break
							}
						}
						if hasTags {
							break
						}
					}
				}

				// Only set TaggedStateFlag if there are actual tags OR a priority shift.
				// However, for CCWarp detection, we will separately check if priority shifts are allowed.
				if hasTags || basePrio != 0 {
					rawNext |= TaggedStateFlag
				}
			}

			d.transitions[idx] = rawNext
			for len(d.recapTables[0].Transitions) <= idx {
				d.recapTables[0].Transitions = append(d.recapTables[0].Transitions, nil)
			}

			// Entry updates for the CURRENT state (delta tags from the closure that reached here)
			currentEntryUpdates := d.stateEntryTags[i]

			var entries []RecapEntry
			for _, u := range nextRes.Updates {
				// Find delta tags generated just before consuming this byte.
				var preByteTags uint64
				for _, eu := range currentEntryUpdates {
					if eu.NextPriority == u.RelativePriority {
						preByteTags = eu.Tags
						break
					}
				}

				entries = append(entries, RecapEntry{
					InputPriority: int16(u.RelativePriority),
					NextPriority:  int16(u.NextPriority),
					PreTags:       preByteTags,
					PostTags:      u.Tags,
				})
			}
			d.recapTables[0].Transitions[idx] = entries

		}
	}

	// Post-processing: Detect states that qualify for CCWarp (SWAR Warp).
	d.ccWarpTable = make([]CCWarpInfo, d.numStates)
	for i := 0; i < d.numStates; i++ {
		// A state is CCWarp candidate if it has a large set of self-loops without tags/anchors.
		// CRITICAL: We only count transitions that lead back to the SAME state (self-loop).
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

			// Condition: MUST BE a self-loop to the same state (masking out flags)
			if (next & StateIDMask) != uint32(i) {
				continue
			}

			// Condition: No actual capture group tags or anchors on this transition.
			hasRealTags := false
			if (next & TaggedStateFlag) != 0 {
				uIdx := d.tagUpdateIndices[idx]
				if uIdx != 0xFFFFFFFF {
					update := d.tagUpdates[uIdx]
					for _, u := range update.PreUpdates {
						if u.Tags != 0 {
							hasRealTags = true
							break
						}
					}
					if !hasRealTags {
						for _, u := range update.PreUpdates {
							for _, eu := range d.stateEntryTags[i] {
								if eu.NextPriority == u.RelativePriority && eu.Tags != 0 {
									hasRealTags = true
									break
								}
							}
							if hasRealTags {
								break
							}
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

		// Try to identify a pattern in the self-loops for SWAR kernels.
		if count == 128 {
			d.ccWarpTable[i] = CCWarpInfo{Kernel: CCWarpAnyChar}
			continue
		}

		// 1. Check for Any-Except-Newline (common for .*)
		isAnyExceptNL := true
		for b := 0; b < 128; b++ {
			if b == '\n' {
				if selfLoops[b] {
					isAnyExceptNL = false
					break
				}
			} else {
				if !selfLoops[b] {
					isAnyExceptNL = false
					break
				}
			}
		}
		if isAnyExceptNL {
			d.ccWarpTable[i] = CCWarpInfo{Kernel: CCWarpAnyExceptNL}
			continue
		}

		// 1b. Check for Not-Equal (e.g. [^"])
		if count == 127 {
			excluded := -1
			for b := 0; b < 128; b++ {
				if !selfLoops[b] {
					excluded = b
					break
				}
			}
			if excluded != -1 {
				d.ccWarpTable[i] = CCWarpInfo{
					Kernel: CCWarpNotEqual,
					V0:     uint64(excluded) * 0x0101010101010101,
				}
				continue
			}
		}

		// 1c. Check for Not-Equal-Set (e.g. [^ "])
		excludedCount := 128 - count
		if excludedCount >= 2 && excludedCount <= 16 { // Increased limit as we can combine
			var excludedChars []int
			for b := 0; b < 128; b++ {
				if !selfLoops[b] {
					excludedChars = append(excludedChars, b)
				}
			}

			var bases, masks [8]uint64
			n := 0
			used := make([]bool, len(excludedChars))
			for j := 0; j < len(excludedChars) && n < 8; j++ {
				if used[j] {
					continue
				}
				base := excludedChars[j]
				mask := 0
				used[j] = true
				// Try to find another char to combine
				for k := j + 1; k < len(excludedChars); k++ {
					if used[k] {
						continue
					}
					diff := base ^ excludedChars[k]
					if (diff & (diff - 1)) == 0 { // Differ by exactly 1 bit
						mask = diff
						used[k] = true
						break
					}
				}
				bases[n] = uint64(base) * 0x0101010101010101
				masks[n] = uint64(mask) * 0x0101010101010101
				n++
			}

			extra := make([]uint64, 16)
			copy(extra[0:8], bases[:])
			copy(extra[8:16], masks[:])

			d.ccWarpTable[i] = CCWarpInfo{
				Kernel: CCWarpNotEqualSet,
				Extra:  extra,
			}
			continue
		}

		// 1d. Check for Not-Single-Range (e.g. [^0-9])
		if count >= 100 {
			exLow, exHigh := -1, -1
			isExSingleRange := true
			for b := 0; b < 128; b++ {
				if !selfLoops[b] {
					if exLow == -1 {
						exLow = b
					}
					exHigh = b
				} else if exLow != -1 {
					for j := b + 1; j < 128; j++ {
						if !selfLoops[j] {
							isExSingleRange = false
							break
						}
					}
					break
				}
			}
			if isExSingleRange && exLow != -1 && (exHigh-exLow) >= 1 {
				d.ccWarpTable[i] = CCWarpInfo{
					Kernel: CCWarpNotSingleRange,
					V0:     uint64(exLow) * 0x0101010101010101,
					V1:     uint64(exHigh) * 0x0101010101010101,
				}
				continue
			}
		}

		// 1e. Check for Not-Bitmask (e.g. [^a-z0-9])
		if count >= 100 && count < 128-8 {
			var mask [2]uint64
			for b := 0; b < 128; b++ {
				if !selfLoops[b] {
					mask[b>>6] |= 1 << (b & 63)
				}
			}
			d.ccWarpTable[i] = CCWarpInfo{
				Kernel: CCWarpNotBitmask,
				Extra:  []uint64{mask[0], mask[1]},
			}
			continue
		}

		// 2. Check for single range [low, high]
		low, high := -1, -1
		isSingleRange := true
		for b := 0; b < 128; b++ {
			if selfLoops[b] {
				if low == -1 {
					low = b
				}
				high = b
			} else if low != -1 {
				// Check if there are more self-loops later (multi-range)
				for j := b + 1; j < 128; j++ {
					if selfLoops[j] {
						isSingleRange = false
						break
					}
				}
				break
			}
		}

		if isSingleRange && low != -1 && (high-low) >= 0 { // Allow single character ranges (e.g. a+)
			splatLow := uint64(low) * 0x0101010101010101
			splatHigh := uint64(high) * 0x0101010101010101
			info := CCWarpInfo{}
			if low == high {
				info.Kernel = CCWarpEqual
				info.V0 = splatLow
			} else {
				info.Kernel = CCWarpSingleRange
				info.V0 = splatLow
				info.V1 = splatHigh
			}
			d.ccWarpTable[i] = info
			continue
		}

		// 2b. Check for Equal-Set (disjoint positive set, e.g. [aeiou])
		if count >= 2 && count <= 16 {
			var chars []int
			for b := 0; b < 128; b++ {
				if selfLoops[b] {
					chars = append(chars, b)
				}
			}

			var bases, masks [8]uint64
			n := 0
			used := make([]bool, len(chars))
			for j := 0; j < len(chars) && n < 8; j++ {
				if used[j] {
					continue
				}
				base := chars[j]
				mask := 0
				used[j] = true
				for k := j + 1; k < len(chars); k++ {
					if used[k] {
						continue
					}
					diff := base ^ chars[k]
					if (diff & (diff - 1)) == 0 {
						mask = diff
						used[k] = true
						break
					}
				}
				bases[n] = uint64(base) * 0x0101010101010101
				masks[n] = uint64(mask) * 0x0101010101010101
				n++
			}
			extra := make([]uint64, 16)
			copy(extra[0:8], bases[:])
			copy(extra[8:16], masks[:])

			d.ccWarpTable[i] = CCWarpInfo{
				Kernel: CCWarpEqualSet,
				Extra:  extra,
			}
			continue
		}

		// 3. Fallback to Bitmask
		if count >= 4 {
			var mask [2]uint64
			for b := 0; b < 128; b++ {
				if selfLoops[b] {
					mask[b>>6] |= 1 << (b & 63)
				}
			}
			d.ccWarpTable[i] = CCWarpInfo{
				Kernel: CCWarpBitmask,
				Extra:  []uint64{mask[0], mask[1]},
			}
		}
	}

	// Assign CCWarpFlag to self-loop transitions for states that qualify.
	for i := 0; i < d.numStates; i++ {
		if d.ccWarpTable[i].Kernel == CCWarpNone {
			continue
		}
		for b := 0; b < 256; b++ {
			idx := (i << 8) | b
			if idx < len(d.transitions) && (d.transitions[idx]&StateIDMask) == uint32(i) {
				d.transitions[idx] |= CCWarpFlag
			}
		}
	}

	// Calculate SearchWarp for the entire pattern (Pre-filter)
	searchIdx := int(d.searchState & StateIDMask)
	var firstBytes [2]uint64
	searchCount := 0
	for b := 0; b < 128; b++ {
		idx := (searchIdx << 8) | b
		if idx < len(d.transitions) {
			next := d.transitions[idx]
			if next != InvalidState {
				firstBytes[b>>6] |= 1 << (b & 63)
				searchCount++
			}
		}
	}

	{
		// 1. Check for single range [low, high]
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
		// If it's a very broad range (like [^\n]), SearchWarp is counter-productive.
		if isSingleRange && low != -1 && (high-low) < 64 {
			splatLow := uint64(low) * 0x0101010101010101
			splatHigh := uint64(high) * 0x0101010101010101
			var chars []byte
			for b := low; b <= high; b++ {
				chars = append(chars, byte(b))
			}
			info := CCWarpInfo{
				IndexAny: string(chars),
			}
			if low == high {
				info.Kernel = CCWarpEqual
				info.V0 = splatLow
			} else {
				info.Kernel = CCWarpSingleRange
				info.V0 = splatLow
				info.V1 = splatHigh
			}
			d.searchWarp = info
		} else if searchCount < 64 {
			// Sparse match starts: use Bitmask (with noise characters in mask)
			var mask [2]uint64
			var chars []byte
			noiseCount := 0
			for b := 0; b < 128; b++ {
				if (firstBytes[b>>6] & (1 << (b & 63))) == 0 {
					mask[b>>6] |= 1 << (b & 63)
					noiseCount++
				} else {
					chars = append(chars, byte(b))
				}
			}
			if noiseCount >= 4 {
				d.searchWarp = CCWarpInfo{
					Kernel:   CCWarpBitmask,
					Extra:    []uint64{mask[0], mask[1]},
					IndexAny: string(chars),
				}
			}
		}
	}

	return nil
}

func epsilonClosureWithAnchorWall(prog *syntax.Prog, paths []NFAPath) ClosureResult {
	type stateKey struct {
		ID, NodeID uint32
		Anchors    syntax.EmptyOp
	}
	// Track min priority reached per state PER source priority.
	minPriority := make(map[int32]map[stateKey]int32)

	type pathWithMeta struct {
		p          NFAPath
		newTags    uint64
		sourcePrio int32
		sourceTags uint64
	}
	stack := make([]pathWithMeta, 0, len(paths))
	var updates []PathTagUpdate
	for _, p := range paths {
		if _, ok := minPriority[p.Priority]; !ok {
			minPriority[p.Priority] = make(map[stateKey]int32)
		}
		minPriority[p.Priority][stateKey{p.ID, p.NodeID, p.Anchors}] = p.Priority
		stack = append(stack, pathWithMeta{p, 0, p.Priority, p.Tags})

		// Initial path identity must be recorded as a potential transition target.
		inst := prog.Inst[p.ID]
		if p.NodeID != 0 || !isEpsilon(inst.Op) {
			updates = append(updates, PathTagUpdate{
				RelativePriority: p.Priority,
				NextPriority:     p.Priority,
				Tags:             0,
			})
		}
	}

	var resultPathsMap = make(map[stateKey]NFAPath)
	var matchAnchors syntax.EmptyOp
	for len(stack) > 0 {
		ph := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		p := ph.p

		sk := stateKey{p.ID, p.NodeID, p.Anchors}
		if p.Priority > minPriority[ph.sourcePrio][sk] {
			continue
		}

		inst := prog.Inst[p.ID]
		if p.NodeID != 0 || !isEpsilon(inst.Op) {
			rk := stateKey{p.ID, p.NodeID, p.Anchors}
			p.Tags |= ph.newTags

			// Always record the update for this specific source priority.
			// RECORD ONLY delta tags (ph.newTags) for the RecapTable.
			updates = append(updates, PathTagUpdate{
				RelativePriority: ph.sourcePrio,
				NextPriority:     p.Priority,
				Tags:             ph.newTags,
			})

			if existing, ok := resultPathsMap[rk]; !ok || p.Priority < existing.Priority {
				resultPathsMap[rk] = p
			} else if p.Priority == existing.Priority {
				existing.Tags |= p.Tags
				resultPathsMap[rk] = existing
			}
			if p.NodeID == 0 && inst.Op == syntax.InstMatch {
				matchAnchors |= p.Anchors
			}
			continue
		}

		// Epsilon transition
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
						sourceTags: ph.sourceTags,
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

				// Standard TDFA logic for capturing groups in loops:
				// If a tag is already set in the cumulative path (p.Tags),
				// do NOT add it to newTags for the current transition.
				nextNewTags := ph.newTags
				if (p.Tags & tagBit) == 0 {
					nextNewTags |= tagBit
				}

				stack = append(stack, pathWithMeta{
					p:          NFAPath{ID: inst.Out, Priority: p.Priority, Anchors: p.Anchors, Tags: p.Tags | tagBit},
					newTags:    nextNewTags,
					sourcePrio: ph.sourcePrio,
					sourceTags: ph.sourceTags,
				})
			}
		case syntax.InstEmptyWidth:
			newAnchors := p.Anchors | syntax.EmptyOp(inst.Arg)
			nsk := stateKey{inst.Out, 0, newAnchors}
			if _, ok := minPriority[ph.sourcePrio]; !ok {
				minPriority[ph.sourcePrio] = make(map[stateKey]int32)
			}
			if m, ok := minPriority[ph.sourcePrio][nsk]; !ok || p.Priority <= m {
				minPriority[ph.sourcePrio][nsk] = p.Priority
				stack = append(stack, pathWithMeta{
					p:          NFAPath{ID: inst.Out, Priority: p.Priority, Anchors: newAnchors, Tags: p.Tags},
					newTags:    ph.newTags,
					sourcePrio: ph.sourcePrio,
					sourceTags: ph.sourceTags,
				})
			}
		case syntax.InstNop:
			nsk := stateKey{inst.Out, 0, p.Anchors}
			if _, ok := minPriority[ph.sourcePrio]; !ok {
				minPriority[ph.sourcePrio] = make(map[stateKey]int32)
			}
			if m, ok := minPriority[ph.sourcePrio][nsk]; !ok || p.Priority <= m {
				minPriority[ph.sourcePrio][nsk] = p.Priority
				stack = append(stack, pathWithMeta{
					p:          NFAPath{ID: inst.Out, Priority: p.Priority, Anchors: p.Anchors, Tags: p.Tags},
					newTags:    ph.newTags,
					sourcePrio: ph.sourcePrio,
					sourceTags: ph.sourceTags,
				})
			}
		}
	}

	var resPaths []NFAPath
	for _, p := range resultPathsMap {
		resPaths = append(resPaths, p)
	}
	minTotalPrio := int32(1 << 30)
	if len(resPaths) > 0 {
		minTotalPrio = resPaths[0].Priority
		for _, p := range resPaths {
			if p.Priority < minTotalPrio {
				minTotalPrio = p.Priority
			}
		}
		for i := range resPaths {
			resPaths[i].Priority -= minTotalPrio
		}
		for i := range updates {
			updates[i].NextPriority -= minTotalPrio
		}
	}
	sort.Slice(resPaths, func(i, j int) bool {
		if resPaths[i].ID != resPaths[j].ID {
			return resPaths[i].ID < resPaths[j].ID
		}
		if resPaths[i].NodeID != resPaths[j].NodeID {
			return resPaths[i].NodeID < resPaths[j].NodeID
		}
		if resPaths[i].Priority != resPaths[j].Priority {
			return resPaths[i].Priority < resPaths[j].Priority
		}
		if resPaths[i].Anchors != resPaths[j].Anchors {
			return resPaths[i].Anchors < resPaths[j].Anchors
		}
		return resPaths[i].Tags < resPaths[j].Tags
	})
	return ClosureResult{resPaths, updates, matchAnchors}
}

func isEpsilon(op syntax.InstOp) bool {
	switch op {
	case syntax.InstAlt, syntax.InstAltMatch, syntax.InstCapture, syntax.InstNop, syntax.InstEmptyWidth:
		return true
	}
	return false
}

func checkEpsilonLoop(prog *syntax.Prog) error {
	visited := make([]bool, len(prog.Inst))
	onStack := make([]bool, len(prog.Inst))

	var dfs func(int) error
	dfs = func(id int) error {
		if onStack[id] {
			return &syntax.UnsupportedError{Op: "epsilon loop"}
		}
		if visited[id] {
			return nil
		}

		visited[id] = true
		onStack[id] = true
		defer func() { onStack[id] = false }()

		inst := prog.Inst[id]
		if isEpsilon(inst.Op) {
			if err := dfs(int(inst.Out)); err != nil {
				return err
			}
			if inst.Op == syntax.InstAlt || inst.Op == syntax.InstAltMatch {
				if err := dfs(int(inst.Arg)); err != nil {
					return err
				}
			}
		}
		return nil
	}

	for i := range prog.Inst {
		if !visited[i] {
			if err := dfs(i); err != nil {
				return err
			}
		}
	}
	return nil
}

func hashSet(paths []NFAPath, naked bool) [2]uint64 {
	var h1 uint64 = 14695981039346656037
	for _, p := range paths {
		h1 ^= uint64(p.ID)
		h1 *= 1099511628211
		h1 ^= uint64(p.NodeID)
		h1 *= 1099511628211
		if !naked {
			h1 ^= uint64(p.Priority)
			h1 *= 1099511628211
		}
	}
	return [2]uint64{h1, 0}
}

func NewDFAWithMemoryLimit(ctx context.Context, s *syntax.Regexp, prog *syntax.Prog, maxMemory int, naked bool) (*DFA, error) {
	d := &DFA{Naked: naked, numSubexp: prog.NumCap/2 - 1}
	if err := d.build(ctx, s, prog, maxMemory); err != nil {
		return nil, err
	}
	return d, nil
}

func checkCompatibility(re *syntax.Regexp) error {
	if re == nil {
		return nil
	}
	switch re.Op {
	case syntax.OpCapture:
		if hasEmptyAlternative(re.Sub[0]) {
			return &syntax.UnsupportedError{Op: "empty alternative in capture"}
		}
	case syntax.OpQuest:
		if hasCapture(re.Sub[0]) && matchesEmpty(re.Sub[0]) {
			return &syntax.UnsupportedError{Op: "optional empty capture"}
		}
	}
	for _, sub := range re.Sub {
		if err := checkCompatibility(sub); err != nil {
			return err
		}
	}
	return nil
}

func simplifyRegexp(re *syntax.Regexp) *syntax.Regexp {
	for re.Op == syntax.OpCapture {
		re = re.Sub[0]
	}
	return re
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
		// If a concat consists only of empty matches, it's an empty alternative
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

func isGreedyQuantifier(re *syntax.Regexp) bool {
	return (re.Op == syntax.OpStar || re.Op == syntax.OpPlus || re.Op == syntax.OpQuest || re.Op == syntax.OpRepeat) && (re.Flags&syntax.NonGreedy == 0)
}

type ByteSet [4]uint64 // 256 bits

func (s *ByteSet) Set(b byte) { s[b>>6] |= 1 << (b & 63) }
func (s *ByteSet) Overlaps(other ByteSet) bool {
	return (s[0]&other[0]) != 0 || (s[1]&other[1]) != 0 || (s[2]&other[2]) != 0 || (s[3]&other[3]) != 0
}

func GetFirstBytes(re *syntax.Regexp) ByteSet {
	var set ByteSet
	switch re.Op {
	case syntax.OpLiteral:
		if len(re.Rune) > 0 {
			var buf [4]byte
			utf8.EncodeRune(buf[:], re.Rune[0])
			set.Set(buf[0])
		}
	case syntax.OpCharClass:
		for i := 0; i < len(re.Rune); i += 2 {
			lo, hi := re.Rune[i], re.Rune[i+1]
			if lo <= 0x7F && hi <= 0x7F {
				for b := byte(lo); b <= byte(hi); b++ {
					set.Set(b)
				}
			} else {
				var buf [4]byte
				utf8.EncodeRune(buf[:], lo)
				set.Set(buf[0])
				if lo != hi {
					utf8.EncodeRune(buf[:], hi)
					set.Set(buf[0])
				}
			}
		}
	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		for b := 0; b < 256; b++ {
			set.Set(byte(b))
		}
	case syntax.OpCapture, syntax.OpStar, syntax.OpPlus, syntax.OpQuest, syntax.OpRepeat:
		return GetFirstBytes(re.Sub[0])
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			s := GetFirstBytes(sub)
			for i := range set {
				set[i] |= s[i]
			}
			if !matchesEmpty(sub) {
				break
			}
		}
	case syntax.OpAlternate:
		for _, sub := range re.Sub {
			s := GetFirstBytes(sub)
			for i := range set {
				set[i] |= s[i]
			}
		}
	}
	return set
}

func matchesEmpty(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpEmptyMatch, syntax.OpStar, syntax.OpQuest:
		return true
	case syntax.OpRepeat:
		return re.Min == 0
	case syntax.OpCapture:
		return matchesEmpty(re.Sub[0])
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			if !matchesEmpty(sub) {
				return false
			}
		}
		return true
	case syntax.OpAlternate:
		for _, sub := range re.Sub {
			if matchesEmpty(sub) {
				return true
			}
		}
		return false
	}
	return false
}

func overlaps(a, b *syntax.Regexp) bool {
	sa := GetFirstBytes(a)
	sb := GetFirstBytes(b)
	return sa.Overlaps(sb)
}
