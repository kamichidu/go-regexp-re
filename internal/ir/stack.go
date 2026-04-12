package ir

// CaptureRecord represents a single tag update event.
type CaptureRecord struct {
	Tags uint64
	Pos  int
}

// CaptureStack is a specialized stack for recording capture boundaries.
// It uses a small embedded array to avoid allocations in most cases.
type CaptureStack struct {
	embedded [64]CaptureRecord
	extended []CaptureRecord
	top      int
}

func (s *CaptureStack) Push(tags uint64, pos int) {
	if s.top < len(s.embedded) {
		s.embedded[s.top] = CaptureRecord{Tags: tags, Pos: pos}
		s.top++
		return
	}
	if s.extended == nil {
		s.extended = make([]CaptureRecord, 0, 128)
	}
	s.extended = append(s.extended, CaptureRecord{Tags: tags, Pos: pos})
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
	// This part is less efficient but only hit for very complex regexps
	res := make([]CaptureRecord, s.top)
	copy(res, s.embedded[:])
	copy(res[len(s.embedded):], s.extended)
	return res
}

func (s *CaptureStack) Len() int {
	return s.top
}
