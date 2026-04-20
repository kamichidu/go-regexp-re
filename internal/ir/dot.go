package ir

import (
	"fmt"
	"sort"
	"strings"
)

func ToDOT(d *DFA) string {
	var sb strings.Builder
	sb.WriteString("digraph DFA {\n")
	sb.WriteString("  rankdir=LR;\n")
	sb.WriteString("  node [shape=circle];\n")

	for i := 0; i < d.numStates; i++ {
		u := uint32(i)
		shape := "circle"
		if d.IsAccepting(u) {
			shape = "doublecircle"
		}

		var labels []string
		labels = append(labels, fmt.Sprintf("S%d", i))

		if u == d.SearchState() {
			labels[0] += " (search)"
		}
		if u == d.MatchState() {
			labels[0] += " (match)"
		}

		if d.IsAccepting(u) {
			labels = append(labels, fmt.Sprintf("Priority: %d", d.MatchPriority(u)))
		}

		label := strings.Join(labels, "\n")
		sb.WriteString(fmt.Sprintf("  %d [label=%q, shape=%s];\n", i, label, shape))

		type edgeKey struct{ target uint32 }
		type edgeInfo struct{ bytes []int }
		groupedEdges := make(map[edgeKey]*edgeInfo)
		var keys []edgeKey

		for b := 0; b < 256; b++ {
			target := d.Next(u, b)
			if target == InvalidState {
				continue
			}
			ek := edgeKey{target}
			if _, ok := groupedEdges[ek]; !ok {
				groupedEdges[ek] = &edgeInfo{}
				keys = append(keys, ek)
			}
			groupedEdges[ek].bytes = append(groupedEdges[ek].bytes, b)
		}

		sort.Slice(keys, func(i, j int) bool { return keys[i].target < keys[j].target })

		for _, ek := range keys {
			info := groupedEdges[ek]
			ranges := formatByteRanges(info.bytes)
			sb.WriteString(fmt.Sprintf("  %d -> %d [label=%q];\n", i, int(ek.target&StateIDMask), ranges))
		}
	}
	sb.WriteString("}\n")
	return sb.String()
}

func formatByteRanges(bytes []int) string {
	if len(bytes) == 0 {
		return ""
	}
	sort.Ints(bytes)
	var parts []string
	start := bytes[0]
	for i := 1; i <= len(bytes); i++ {
		if i == len(bytes) || bytes[i] != bytes[i-1]+1 {
			if start == bytes[i-1] {
				parts = append(parts, fmt.Sprintf("%02X", start))
			} else {
				parts = append(parts, fmt.Sprintf("%02X-%02X", start, bytes[i-1]))
			}
			if i < len(bytes) {
				start = bytes[i]
			}
		}
	}
	return strings.Join(parts, ",")
}
