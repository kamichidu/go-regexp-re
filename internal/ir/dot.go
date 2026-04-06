package ir

import (
	"fmt"
	"sort"
	"strings"
)

// ToDOT returns the DFA in Graphviz DOT format for debugging.
func ToDOT(d *DFA) string {
	var sb strings.Builder
	sb.WriteString("digraph DFA {\n")
	sb.WriteString("  rankdir=LR;\n")
	sb.WriteString("  node [shape=circle];\n")

	for i := 0; i < d.numStates; i++ {
		s := StateID(i)
		shape := "circle"
		if d.IsAccepting(s) {
			shape = "doublecircle"
		}

		var labels []string
		labels = append(labels, fmt.Sprintf("S%d", i))
		if s == d.startState {
			labels[0] += " (start)"
		}

		// Show NFA state IDs if available
		if i < len(d.dfaToNfa) {
			var nfaIDs []string
			for idx, p := range d.dfaToNfa[i] {
				nfaIDs = append(nfaIDs, fmt.Sprintf("%d:%d", idx, p.ID))
			}
			labels = append(labels, "NFA(idx:id): "+strings.Join(nfaIDs, ","))
		}

		// Show accepting priority
		if d.IsAccepting(s) {
			labels = append(labels, fmt.Sprintf("Priority: %d", d.AcceptingPriority(s)))
		}

		label := strings.Join(labels, "\n")
		sb.WriteString(fmt.Sprintf("  %d [label=%q, shape=%s];\n", i, label, shape))

		// Group transitions by (targetState, tagsMapping)
		type edgeKey struct {
			target StateID
			tags   string
		}

		type edgeInfo struct {
			bytes []int
		}

		groupedEdges := make(map[edgeKey]*edgeInfo)
		var keys []edgeKey

		for b := 0; b < d.stride; b++ {
			next := d.Next(s, b)
			if next != InvalidState {
				sources, tags := d.TransitionInfo(s, b)
				var tParts []string
				for k, src := range sources {
					if len(tags[k]) > 0 {
						tParts = append(tParts, fmt.Sprintf("%d>%d%s", src, k, formatTags(tags[k])))
					} else {
						tParts = append(tParts, fmt.Sprintf("%d>%d", src, k))
					}
				}
				tStr := strings.Join(tParts, "|")
				key := edgeKey{next, tStr}
				if _, ok := groupedEdges[key]; !ok {
					groupedEdges[key] = &edgeInfo{}
					keys = append(keys, key)
				}
				groupedEdges[key].bytes = append(groupedEdges[key].bytes, b)
			}
		}

		// Sort keys for deterministic output
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].target != keys[j].target {
				return keys[i].target < keys[j].target
			}
			return keys[i].tags < keys[j].tags
		})

		for _, key := range keys {
			info := groupedEdges[key]
			edgeLabel := formatBytes(info.bytes)
			if key.tags != "" {
				edgeLabel += " / " + key.tags
			}
			sb.WriteString(fmt.Sprintf("  %d -> %d [label=%q];\n", i, key.target, edgeLabel))
		}
	}

	sb.WriteString("}\n")
	return sb.String()
}

func formatTags(tags []TagOp) string {
	if len(tags) == 0 {
		return ""
	}
	var res []string
	for _, t := range tags {
		after := ""
		if t.After() {
			after = "a"
		}
		res = append(res, fmt.Sprintf("%d%s", t.Index(), after))
	}
	return "{" + strings.Join(res, ",") + "}"
}

func formatBytes(bytes []int) string {
	if len(bytes) == 0 {
		return ""
	}
	sort.Ints(bytes)

	var parts []string
	start := -1
	end := -1

	flush := func() {
		if start != -1 {
			if start == end {
				parts = append(parts, formatByte(start))
			} else {
				parts = append(parts, fmt.Sprintf("%s-%s", formatByte(start), formatByte(end)))
			}
		}
	}

	for _, b := range bytes {
		if start == -1 {
			start = b
			end = b
		} else if b == end+1 {
			end = b
		} else {
			flush()
			start = b
			end = b
		}
	}
	flush()

	return strings.Join(parts, ",")
}

func formatByte(b int) string {
	if b >= 256 {
		switch b {
		case VirtualBeginLine:
			return "^L"
		case VirtualEndLine:
			return "$L"
		case VirtualBeginText:
			return "^T"
		case VirtualEndText:
			return "$T"
		case VirtualWordBoundary:
			return "\\b"
		case VirtualNoWordBoundary:
			return "\\B"
		default:
			return fmt.Sprintf("V%d", b)
		}
	}

	if b >= 32 && b <= 126 {
		c := byte(b)
		if c == '"' || c == '\\' {
			return "\\" + string(c)
		}
		return string(c)
	}
	return fmt.Sprintf("0x%02X", b)
}
