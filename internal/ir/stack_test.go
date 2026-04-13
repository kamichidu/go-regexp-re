package ir

import (
	"reflect"
	"testing"
)

func TestCaptureStack(t *testing.T) {
	var s CaptureStack

	// Test basic push/pop
	s.Push(0, 1<<0, 10)
	s.Push(0, 1<<1, 20)

	if s.Len() != 2 {
		t.Errorf("expected len 2, got %d", s.Len())
	}

	records := s.All()
	expected := []CaptureRecord{
		{0, 1 << 0, 10},
		{0, 1 << 1, 20},
	}
	if !reflect.DeepEqual(records, expected) {
		t.Errorf("expected %v, got %v", expected, records)
	}

	// Test extended storage
	s.Reset()
	for i := 0; i < 200; i++ {
		s.Push(0, uint64(1<<uint(i%64)), i)
	}
	if s.Len() != 200 {
		t.Errorf("expected len 200, got %d", s.Len())
	}
	if len(s.extended) != 200-128 {
		t.Errorf("expected extended len %d, got %d", 200-128, len(s.extended))
	}

	// Test Reset
	s.Reset()
	if s.Len() != 0 {
		t.Errorf("expected len 0 after reset, got %d", s.Len())
	}
}

func TestCaptureStack_Large(t *testing.T) {
	var s CaptureStack
	for i := 0; i < 1000; i++ {
		s.Push(0, 1, i)
	}
	if s.Len() != 1000 {
		t.Errorf("expected 1000, got %d", s.Len())
	}
	records := s.All()
	if len(records) != 1000 {
		t.Errorf("expected records 1000, got %d", len(records))
	}
}
