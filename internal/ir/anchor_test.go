package ir

import (
	"testing"
)

func TestCalculateSelectivity(t *testing.T) {
	tests := []struct {
		name           string
		anchor         []byte
		isAnchorJoined bool
		hasClass       bool
		want           int
	}{
		{"LiteralShort", []byte("a"), false, false, 1*5 + 1*15},                 // length=1, unique=1 -> 20
		{"LiteralRepeat", []byte("aaa"), false, false, 3*5 + 1*15},              // length=3, unique=1 -> 30
		{"LiteralDiverse", []byte("abc"), false, false, 3*5 + 3*15},             // length=3, unique=3 -> 60
		{"LiteralLong", []byte("abcdefgh"), false, false, 8*5 + 8*15 + 20 + 40}, // length=8, unique=8, bonus 20+40 -> 220
		{"AnchorJoinedShort", []byte("a"), true, false, 20 + 200},               // 220
		{"ClassOnly", nil, false, true, 1*5 + 1*15 - 30},                        // -10
		{"AnchorJoinedClass", nil, true, true, 180 + 10},                        // (15 + 5 + 200) - 30 = 190
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CalculateSelectivity(tt.anchor, tt.isAnchorJoined, tt.hasClass); got != tt.want {
				t.Errorf("CalculateSelectivity(%q, %v, %v) = %v; want %v", tt.anchor, tt.isAnchorJoined, tt.hasClass, got, tt.want)
			}
		})
	}
}
