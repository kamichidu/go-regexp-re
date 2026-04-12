package ir

// CaptureRecord represents a single tag update event.
type CaptureRecord struct {
	Priority int
	Tags     uint64
	Pos      int
}

// CaptureStack is a specialized stack for recording capture boundaries.
// It uses a small embedded array to avoid allocations in most cases.
type CaptureStack struct {
	embedded [128]CaptureRecord // Increased to 128
	extended []CaptureRecord
	top      int
}

func (s *CaptureStack) Push(priority int, tags uint64, pos int) {
	if tags == 0 {
		return
	}
	if s.top < len(s.embedded) {
		s.embedded[s.top] = CaptureRecord{Priority: priority, Tags: tags, Pos: pos}
		s.top++
		return
	}
	if s.extended == nil {
		s.extended = make([]CaptureRecord, 0, 128)
	}
	s.extended = append(s.extended, CaptureRecord{Priority: priority, Tags: tags, Pos: pos})
	s.top++
}

func (s *CaptureStack) Reset() {
	s.top = 0
	if s.extended != nil {
		s.extended = s.extended[:0]
	}
}

func (s *CaptureStack) All() []CaptureRecord {
	if s.top <= len(s.embedded) {
		return s.embedded[:s.top]
	}
	res := make([]CaptureRecord, s.top)
	copy(res, s.embedded[:])
	copy(res[len(s.embedded):], s.extended)
	return res
}

func (s *CaptureStack) Len() int {
	return s.top
}
