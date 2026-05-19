package main

import (
	"math"
	"testing"
)

func TestCalculateMetrics(t *testing.T) {
	tests := []struct {
		name       string
		results    []BenchResult
		wantAvg    float64
		wantMin    float64
		wantMax    float64
		wantOk     bool
	}{
		{
			name: "Simple pairing by category",
			results: []BenchResult{
				{Engine: "GoRegexp", Category: "Suite/Test1", Throughput: 100.0},
				{Engine: "GoRegexpRe", Category: "Suite/Test1", Throughput: 200.0}, // 2x
				{Engine: "GoRegexp", Category: "Suite/Test2", Throughput: 50.0},
				{Engine: "GoRegexpRe", Category: "Suite/Test2", Throughput: 25.0},  // 0.5x
			},
			// GeoMean: exp((ln(2.0) + ln(0.5))/2) = exp(0) = 1.0
			wantAvg: 1.0,
			wantMin: 0.5,
			wantMax: 2.0,
			wantOk:  true,
		},
		{
			name: "Pairing by SBL fallback",
			results: []BenchResult{
				{Engine: "GoRegexp", S: 0.5, B: 0.5, L: 0.5, Throughput: 100.0},
				{Engine: "GoRegexpRe", S: 0.5, B: 0.5, L: 0.5, Throughput: 400.0}, // 4x
			},
			wantAvg: 4.0,
			wantMin: 4.0,
			wantMax: 4.0,
			wantOk:  true,
		},
		{
			name: "No matches for GoRegexpRe",
			results: []BenchResult{
				{Engine: "GoRegexp", Category: "Suite/Test1", Throughput: 100.0},
				{Engine: "Coregex", Category: "Suite/Test1", Throughput: 200.0},
			},
			wantOk: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			avg, min, max, ok := calculateMetrics(tt.results)
			if ok != tt.wantOk {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOk)
			}
			if !ok {
				return
			}

			epsilon := 0.0001
			if math.Abs(avg-tt.wantAvg) > epsilon {
				t.Errorf("avg = %v, want %v", avg, tt.wantAvg)
			}
			if math.Abs(min-tt.wantMin) > epsilon {
				t.Errorf("min = %v, want %v", min, tt.wantMin)
			}
			if math.Abs(max-tt.wantMax) > epsilon {
				t.Errorf("max = %v, want %v", max, tt.wantMax)
			}
		})
	}
}
