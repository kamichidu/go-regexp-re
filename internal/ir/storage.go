package ir

import (
	"sync"
)

type NFAPathStorage interface {
	Put(id uint32, paths []NFAPath) error
	Get(id uint32, buf []NFAPath) ([]NFAPath, error)
	Close() error
}

type memoryNfaSetStorage struct {
	data [][]NFAPath
	mu   sync.RWMutex
}

func (s *memoryNfaSetStorage) Put(id uint32, paths []NFAPath) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := int(id & StateIDMask)
	if idx >= len(s.data) {
		s.data = append(s.data, make([][]NFAPath, 1024)...)
	}
	s.data[idx] = append([]NFAPath(nil), paths...)
	return nil
}

func (s *memoryNfaSetStorage) Get(id uint32, buf []NFAPath) ([]NFAPath, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx := int(id & StateIDMask)
	if idx >= len(s.data) {
		return nil, nil
	}
	return s.data[idx], nil
}

func (s *memoryNfaSetStorage) Close() error { return nil }
