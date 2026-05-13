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
		{"LiteralShort", []byte("a"), false, false, 1*5 + 1*10},                 // length=1, unique=1 -> 15
		{"LiteralRepeat", []byte("aaa"), false, false, 3*5 + 1*10},              // length=3, unique=1 -> 25
		{"LiteralDiverse", []byte("abc"), false, false, 3*5 + 3*10},             // length=3, unique=3 -> 45
		{"LiteralLong", []byte("abcdefgh"), false, false, 8*5 + 8*10 + 20 + 40}, // length=8, unique=8, bonus 20+40 -> 180
		{"AnchorJoinedShort", []byte("a"), true, false, 15 + 200},               // 215
		{"ClassOnly", nil, false, true, -15},                                    // (1*5 + 1*10) - 30 = -15
		{"AnchorJoinedClass", nil, true, true, 185},                             // (1*5 + 1*10) + 200 - 30 = 185
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CalculateSelectivity(tt.anchor, tt.isAnchorJoined, tt.hasClass); got != tt.want {
				t.Errorf("CalculateSelectivity(%q, %v, %v) = %v; want %v", tt.anchor, tt.isAnchorJoined, tt.hasClass, got, tt.want)
			}
		})
	}
}
