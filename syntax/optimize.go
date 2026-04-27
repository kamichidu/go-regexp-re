package syntax

import (
	gosyntax "regexp/syntax"
)

// Simplify returns a simplified version of the regular expression.
func Simplify(re *Regexp) *Regexp {
	return re.Simplify()
}

// Optimize returns an optimized version of the regular expression.
// It merges common prefixes in alternations to reduce the number of DFA states.
func Optimize(re *Regexp) *Regexp {
	if re == nil {
		return nil
	}

	for i, sub := range re.Sub {
		re.Sub[i] = Optimize(sub)
	}

	switch re.Op {
	case OpAlternate:
		for i, sub := range re.Sub {
			if sub.Op == OpConcat {
				sub = flattenConcat(sub)
				sub = aggregateLiterals(sub)
				re.Sub[i] = sub
			}
		}
		re = factorAlternate(re)
	case OpConcat:
		re = flattenConcat(re)
		re = aggregateLiterals(re)
		if len(re.Sub) == 1 {
			return re.Sub[0]
		}
	}

	return re
}

func flattenConcat(re *Regexp) *Regexp {
	if re.Op != OpConcat {
		return re
	}

	hasNested := false
	for _, sub := range re.Sub {
		if sub.Op == OpConcat {
			hasNested = true
			break
		}
	}
	if !hasNested {
		return re
	}

	var subs []*Regexp
	for _, sub := range re.Sub {
		if sub.Op == OpConcat {
			subs = append(subs, sub.Sub...)
		} else {
			subs = append(subs, sub)
		}
	}
	re.Sub = subs
	return re
}

func aggregateLiterals(re *Regexp) *Regexp {
	if re.Op != OpConcat || len(re.Sub) <= 1 {
		return re
	}

	var newSubs []*Regexp
	var lastLiteral *Regexp

	for _, sub := range re.Sub {
		if sub.Op == OpEmptyMatch {
			continue
		}
		if sub.Op == OpLiteral && sub.Flags&gosyntax.FoldCase == 0 {
			if lastLiteral != nil && lastLiteral.Flags&gosyntax.FoldCase == 0 {
				lastLiteral.Rune = append(lastLiteral.Rune, sub.Rune...)
			} else {
				lastLiteral = &Regexp{
					Op:    OpLiteral,
					Rune:  append([]rune(nil), sub.Rune...),
					Flags: sub.Flags,
				}
				newSubs = append(newSubs, lastLiteral)
			}
		} else {
			newSubs = append(newSubs, sub)
			lastLiteral = nil
		}
	}

	if len(newSubs) == 0 {
		return &Regexp{Op: OpEmptyMatch}
	}
	if len(newSubs) == 1 {
		return newSubs[0]
	}

	if len(newSubs) == len(re.Sub) {
		return re
	}
	re.Sub = newSubs
	return re
}

func factorAlternate(re *Regexp) *Regexp {
	if len(re.Sub) <= 1 {
		return re
	}

	for {
		oldLen := len(re.Sub)

		// 1. Flatten nested alternates
		var subs []*Regexp
		changed := false
		for _, sub := range re.Sub {
			if sub.Op == OpAlternate {
				subs = append(subs, sub.Sub...)
				changed = true
			} else {
				subs = append(subs, sub)
			}
		}

		// 2. Prefix factoring
		newSubs := factorPrefix(subs)
		if len(newSubs) != len(subs) {
			changed = true
		}
		subs = newSubs

		// 3. Suffix factoring
		newSubs = factorSuffix(subs)
		if len(newSubs) != len(subs) {
			changed = true
		}
		subs = newSubs

		re.Sub = subs
		if len(re.Sub) == 1 {
			return re.Sub[0]
		}
		if !changed && len(re.Sub) == oldLen {
			break
		}
	}

	return re
}

func factorPrefix(subs []*Regexp) []*Regexp {
	if len(subs) <= 1 {
		return subs
	}

	type group struct {
		prefix *Regexp // Common prefix (can be a partial literal)
		items  []*Regexp
	}
	type entry struct {
		item  *Regexp
		group *group
	}
	var entries []*entry

	for _, sub := range subs {
		if sub.Op == OpEmptyMatch {
			entries = append(entries, &entry{item: sub})
			continue
		}

		found := false
		for _, e := range entries {
			if e.group == nil {
				continue
			}
			g := e.group
			n := commonPrefixLenRune(g.prefix, sub)
			if n > 0 {
				// We found a common prefix of length n.
				// If n is shorter than g.prefix, we must split g.prefix.
				if g.prefix.Op == OpLiteral && n < len(g.prefix.Rune) {
					prefix := &Regexp{Op: OpLiteral, Flags: g.prefix.Flags, Rune: g.prefix.Rune[:n]}
					suffix := &Regexp{Op: OpLiteral, Flags: g.prefix.Flags, Rune: g.prefix.Rune[n:]}

					var newItems []*Regexp
					for _, item := range g.items {
						newItems = append(newItems, combineHead(suffix, item))
					}
					g.prefix = prefix
					g.items = newItems
				}

				_, rest := splitAtRune(sub, n)
				g.items = append(g.items, rest)
				found = true
				break
			} else if equal(g.prefix, sub) {
				g.items = append(g.items, &Regexp{Op: OpEmptyMatch})
				found = true
				break
			}
		}
		if !found {
			head, rest := splitHead(sub)
			if head.Op == OpEmptyMatch {
				entries = append(entries, &entry{item: sub})
			} else {
				g := &group{head, []*Regexp{rest}}
				entries = append(entries, &entry{group: g})
			}
		}
	}

	var newSubs []*Regexp
	for _, e := range entries {
		if e.item != nil {
			newSubs = append(newSubs, e.item)
			continue
		}
		g := e.group
		if len(g.items) == 1 {
			newSubs = append(newSubs, combineHead(g.prefix, g.items[0]))
		} else {
			significant := false
			if g.prefix.Op == OpLiteral {
				significant = len(g.prefix.Rune) > 1
			} else if g.prefix.Op != OpEmptyMatch {
				significant = true
			}

			if significant {
				var flattenedItems []*Regexp
				for _, item := range g.items {
					if item.Op == OpAlternate {
						flattenedItems = append(flattenedItems, item.Sub...)
					} else {
						flattenedItems = append(flattenedItems, item)
					}
				}
				alt := &Regexp{Op: OpAlternate, Sub: flattenedItems}
				newSubs = append(newSubs, combineHead(g.prefix, alt))
			} else {
				for _, item := range g.items {
					newSubs = append(newSubs, combineHead(g.prefix, item))
				}
			}
		}
	}

	return newSubs
}

func commonPrefixLenRune(prefix, re *Regexp) int {
	if prefix.Op == OpEmptyMatch {
		return 0
	}
	if prefix.Op != OpLiteral {
		head, _ := splitHead(re)
		if equal(prefix, head) {
			return 1
		}
		return 0
	}

	head, _ := splitHead(re)
	if head.Op != OpLiteral || head.Flags != prefix.Flags {
		return 0
	}

	n := 0
	for n < len(prefix.Rune) && n < len(head.Rune) && prefix.Rune[n] == head.Rune[n] {
		n++
	}
	return n
}

func splitAtRune(re *Regexp, n int) (head, rest *Regexp) {
	if re.Op == OpLiteral {
		head = &Regexp{Op: OpLiteral, Flags: re.Flags, Rune: re.Rune[:n]}
		if n == len(re.Rune) {
			rest = &Regexp{Op: OpEmptyMatch}
		} else {
			rest = &Regexp{Op: OpLiteral, Flags: re.Flags, Rune: re.Rune[n:]}
		}
		return head, rest
	}

	if re.Op == OpConcat && len(re.Sub) > 0 && re.Sub[0].Op == OpLiteral {
		lit := re.Sub[0]
		head = &Regexp{Op: OpLiteral, Flags: lit.Flags, Rune: lit.Rune[:n]}
		var restSubs []*Regexp
		if n < len(lit.Rune) {
			restSubs = append(restSubs, &Regexp{Op: OpLiteral, Flags: lit.Flags, Rune: lit.Rune[n:]})
		}
		restSubs = append(restSubs, re.Sub[1:]...)

		if len(restSubs) == 0 {
			rest = &Regexp{Op: OpEmptyMatch}
		} else if len(restSubs) == 1 {
			rest = restSubs[0]
		} else {
			rest = &Regexp{Op: OpConcat, Sub: restSubs}
		}
		return head, rest
	}

	return splitHead(re)
}

func factorSuffix(subs []*Regexp) []*Regexp {
	if len(subs) <= 1 {
		return subs
	}

	type group struct {
		suffix *Regexp // Common suffix (can be a partial literal)
		items  []*Regexp
	}
	type entry struct {
		item  *Regexp
		group *group
	}
	var entries []*entry

	for _, sub := range subs {
		if sub.Op == OpEmptyMatch {
			entries = append(entries, &entry{item: sub})
			continue
		}

		found := false
		for _, e := range entries {
			if e.group == nil {
				continue
			}
			g := e.group
			n := commonSuffixLenRune(g.suffix, sub)
			if n > 0 {
				if g.suffix.Op == OpLiteral && n < len(g.suffix.Rune) {
					prefix := &Regexp{Op: OpLiteral, Flags: g.suffix.Flags, Rune: g.suffix.Rune[:len(g.suffix.Rune)-n]}
					suffix := &Regexp{Op: OpLiteral, Flags: g.suffix.Flags, Rune: g.suffix.Rune[len(g.suffix.Rune)-n:]}

					var newItems []*Regexp
					for _, item := range g.items {
						newItems = append(newItems, combineTail(item, prefix))
					}
					g.suffix = suffix
					g.items = newItems
				}

				rest, _ := splitTailAtRune(sub, n)
				g.items = append(g.items, rest)
				found = true
				break
			} else if equal(g.suffix, sub) {
				g.items = append(g.items, &Regexp{Op: OpEmptyMatch})
				found = true
				break
			}
		}
		if !found {
			rest, tail := splitTail(sub)
			if tail.Op == OpEmptyMatch {
				entries = append(entries, &entry{item: sub})
			} else {
				g := &group{tail, []*Regexp{rest}}
				entries = append(entries, &entry{group: g})
			}
		}
	}

	var newSubs []*Regexp
	for _, e := range entries {
		if e.item != nil {
			newSubs = append(newSubs, e.item)
			continue
		}
		g := e.group
		if len(g.items) == 1 {
			newSubs = append(newSubs, combineTail(g.items[0], g.suffix))
		} else {
			significant := false
			if g.suffix.Op == OpLiteral {
				significant = len(g.suffix.Rune) > 1
			} else if g.suffix.Op != OpEmptyMatch {
				significant = true
			}

			if significant {
				var flattenedItems []*Regexp
				for _, item := range g.items {
					if item.Op == OpAlternate {
						flattenedItems = append(flattenedItems, item.Sub...)
					} else {
						flattenedItems = append(flattenedItems, item)
					}
				}
				alt := &Regexp{Op: OpAlternate, Sub: flattenedItems}
				newSubs = append(newSubs, combineTail(alt, g.suffix))
			} else {
				for _, item := range g.items {
					newSubs = append(newSubs, combineTail(item, g.suffix))
				}
			}
		}
	}

	return newSubs
}

func commonSuffixLenRune(suffix, re *Regexp) int {
	if suffix.Op == OpEmptyMatch {
		return 0
	}
	if suffix.Op != OpLiteral {
		_, tail := splitTail(re)
		if equal(suffix, tail) {
			return 1
		}
		return 0
	}

	_, tail := splitTail(re)
	if tail.Op != OpLiteral || tail.Flags != suffix.Flags {
		return 0
	}

	n := 0
	for n < len(suffix.Rune) && n < len(tail.Rune) && suffix.Rune[len(suffix.Rune)-1-n] == tail.Rune[len(tail.Rune)-1-n] {
		n++
	}
	return n
}

func splitTailAtRune(re *Regexp, n int) (rest, tail *Regexp) {
	if re.Op == OpLiteral {
		tail = &Regexp{Op: OpLiteral, Flags: re.Flags, Rune: re.Rune[len(re.Rune)-n:]}
		if n == len(re.Rune) {
			rest = &Regexp{Op: OpEmptyMatch}
		} else {
			rest = &Regexp{Op: OpLiteral, Flags: re.Flags, Rune: re.Rune[:len(re.Rune)-n]}
		}
		return rest, tail
	}

	if re.Op == OpConcat && len(re.Sub) > 0 && re.Sub[len(re.Sub)-1].Op == OpLiteral {
		lit := re.Sub[len(re.Sub)-1]
		tail = &Regexp{Op: OpLiteral, Flags: lit.Flags, Rune: lit.Rune[len(lit.Rune)-n:]}
		var restSubs []*Regexp
		restSubs = append(restSubs, re.Sub[:len(re.Sub)-1]...)
		if n < len(lit.Rune) {
			restSubs = append(restSubs, &Regexp{Op: OpLiteral, Flags: lit.Flags, Rune: lit.Rune[:len(lit.Rune)-n]})
		}

		if len(restSubs) == 0 {
			rest = &Regexp{Op: OpEmptyMatch}
		} else if len(restSubs) == 1 {
			rest = restSubs[0]
		} else {
			rest = &Regexp{Op: OpConcat, Sub: restSubs}
		}
		return rest, tail
	}

	return splitTail(re)
}

func splitHead(re *Regexp) (head, rest *Regexp) {
	if re.Op == OpConcat && len(re.Sub) > 0 {
		head = re.Sub[0]
		if len(re.Sub) == 1 {
			rest = &Regexp{Op: OpEmptyMatch}
		} else if len(re.Sub) == 2 {
			rest = re.Sub[1]
		} else {
			rest = &Regexp{Op: OpConcat, Sub: re.Sub[1:]}
		}
		return head, rest
	}
	return re, &Regexp{Op: OpEmptyMatch}
}

func combineHead(head, rest *Regexp) *Regexp {
	if rest.Op == OpEmptyMatch {
		return head
	}
	if head.Op == OpEmptyMatch {
		return rest
	}
	res := &Regexp{Op: OpConcat}
	if rest.Op == OpConcat {
		res.Sub = append([]*Regexp{head}, rest.Sub...)
	} else {
		res.Sub = []*Regexp{head, rest}
	}
	return res
}

func splitTail(re *Regexp) (rest, tail *Regexp) {
	if re.Op == OpConcat && len(re.Sub) > 0 {
		tail = re.Sub[len(re.Sub)-1]
		if len(re.Sub) == 1 {
			rest = &Regexp{Op: OpEmptyMatch}
		} else if len(re.Sub) == 2 {
			rest = re.Sub[0]
		} else {
			rest = &Regexp{Op: OpConcat, Sub: re.Sub[:len(re.Sub)-1]}
		}
		return rest, tail
	}
	return re, &Regexp{Op: OpEmptyMatch}
}

func combineTail(rest, tail *Regexp) *Regexp {
	if rest.Op == OpEmptyMatch {
		return tail
	}
	if tail.Op == OpEmptyMatch {
		return rest
	}
	res := &Regexp{Op: OpConcat}
	if rest.Op == OpConcat {
		res.Sub = append(rest.Sub, tail)
	} else {
		res.Sub = []*Regexp{rest, tail}
	}
	return res
}

func equal(a, b *Regexp) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if a.Op != b.Op || a.Flags != b.Flags {
		return false
	}
	if len(a.Rune) != len(b.Rune) {
		return false
	}
	for i := range a.Rune {
		if a.Rune[i] != b.Rune[i] {
			return false
		}
	}
	if a.Min != b.Min || a.Max != b.Max || a.Cap != b.Cap || a.Name != b.Name {
		return false
	}
	if len(a.Sub) != len(b.Sub) {
		return false
	}
	for i := range a.Sub {
		if !equal(a.Sub[i], b.Sub[i]) {
			return false
		}
	}
	return true
}
