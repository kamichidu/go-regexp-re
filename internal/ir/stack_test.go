package ir

import (
	"testing"
)

func TestCaptureStack(t *testing.T) {
	var s CaptureStack

	// Test basic push and reset
	s.Push(1, 100)
	s.Push(2, 200)
	if s.Len() != 2 {
		t.Errorf("expected len 2, got %d", s.Len())
	}
	all := s.All()
	if len(all) != 2 || all[0].Tags != 1 || all[1].Pos != 200 {
		t.Errorf("unexpected stack content: %+v", all)
	}

	s.Reset()
	if s.Len() != 0 {
		t.Errorf("expected len 0 after reset, got %d", s.Len())
	}

	// Test overflow (more than 64)
	for i := 0; i < 100; i++ {
		s.Push(uint64(i), i*10)
	}
	if s.Len() != 100 {
		t.Errorf("expected len 100, got %d", s.Len())
	}
	all = s.All()
	if len(all) != 100 {
		t.Errorf("expected all len 100, got %d", len(all))
	}
	if all[0].Tags != 0 || all[64].Tags != 64 || all[99].Pos != 990 {
		t.Errorf("unexpected content after overflow: %+v", all[60:70])
	}

	// Test reset after overflow
	s.Reset()
	if s.Len() != 0 {
		t.Errorf("expected len 0 after reset, got %d", s.Len())
	}
	s.Push(42, 420)
	if s.Len() != 1 {
		t.Errorf("expected len 1, got %d", s.Len())
	}
	all = s.All()
	if all[0].Tags != 42 {
		t.Errorf("unexpected tag: %d", all[0].Tags)
	}
}
