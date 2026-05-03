package landscape

import (
	"encoding/json"
	"io"
	"sort"
)

// Result represents a single benchmark point in the landscape.
type Result struct {
	EngineID   string  `json:"engine_id"`
	PatternID  string  `json:"pattern_id"`
	InputID    string  `json:"input_id"`
	SBL        SBL     `json:"sbl"`
	Speedup    float64 `json:"speedup"` // Relative to stdlib
	Throughput float64 `json:"throughput_mb_s"`
}

// Landscape holds all results and provides aggregation.
type Landscape struct {
	Results []Result `json:"results"`
}

// GridPoint represents an aggregated cell in the 3D grid.
type GridPoint struct {
	S     float64 `json:"s"`
	B     float64 `json:"b"`
	L     float64 `json:"l"`
	Value float64 `json:"value"` // Average speedup
	Count int     `json:"count"`
}

// ExportJSON writes the landscape data to an io.Writer.
func (l *Landscape) ExportJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(l)
}

// BuildGrid aggregates results into a 3D grid with specified bin sizes.
func (l *Landscape) BuildGrid(bins int) []GridPoint {
	type key struct{ s, b, l int }
	grid := make(map[key]*GridPoint)

	for _, r := range l.Results {
		si := int(r.SBL.S * float64(bins-1))
		bi := int(r.SBL.B * float64(bins-1))
		li := int(r.SBL.L * float64(bins-1))

		k := key{si, bi, li}
		if _, ok := grid[k]; !ok {
			grid[k] = &GridPoint{
				S: float64(si) / float64(bins-1),
				B: float64(bi) / float64(bins-1),
				L: float64(li) / float64(bins-1),
			}
		}
		grid[k].Value += r.Speedup
		grid[k].Count++
	}

	var points []GridPoint
	for _, p := range grid {
		p.Value /= float64(p.Count)
		points = append(points, *p)
	}

	sort.Slice(points, func(i, j int) bool {
		if points[i].S != points[j].S {
			return points[i].S < points[j].S
		}
		if points[i].B != points[j].B {
			return points[i].B < points[j].B
		}
		return points[i].L < points[j].L
	})

	return points
}
