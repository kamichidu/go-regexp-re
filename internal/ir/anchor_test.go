package ir

import "testing"

func TestCalculateSelectivity(t *testing.T) {
	tests := []struct {
		name           string
		length         int
		isAnchorJoined bool
		hasClass       bool
		want           int
	}{
		{"LiteralShort", 1, false, false, 10},
		{"LiteralLong", 10, false, false, 100},
		{"AnchorJoinedShort", 1, true, false, 1010},
		{"AnchorJoinedLong", 5, true, false, 1050},
		{"ClassShort", 1, false, true, -40},
		{"ClassLong", 5, false, true, 0},
		{"AnchorJoinedClass", 1, true, true, 960},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CalculateSelectivity(tt.length, tt.isAnchorJoined, tt.hasClass); got != tt.want {
				t.Errorf("CalculateSelectivity(%v, %v, %v) = %v; want %v", tt.length, tt.isAnchorJoined, tt.hasClass, got, tt.want)
			}
		})
	}
}
