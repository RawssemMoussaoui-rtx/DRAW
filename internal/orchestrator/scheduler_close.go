package orchestrator

import (
	"container/heap"

	"draw/internal/model"
)

func (s *Scheduler) ResetSession(sessionID model.SessionID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := s.sessionKeys[sessionID]
	for _, key := range keys {
		delete(s.materialized, key)
		if it, exists := s.keyIndex[key]; exists {
			idx := it.idx
			if idx >= 0 && idx < len(s.pending) {
				heap.Remove(&s.pending, idx)
			}
			delete(s.keyIndex, key)
		}
	}
	delete(s.sessionKeys, sessionID)
	_ = s.fr.ResetSession(sessionID)
}

// TotalMaterialized returns the count of materialized frontier task entries
// still tracked by the scheduler across all sessions. Exposed for testing
// session-scoped dedup cleanup (SVE-3).
func (s *Scheduler) TotalMaterialized() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.materialized)
}

// Close drains all pending tasks from the scheduler's heap and marks it
// as closed. Once closed, subsequent calls are no-ops. No task execution
// occurs; pending tasks are silently discarded.
func (s *Scheduler) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return nil
	}
	for s.pending.Len() > 0 {
		heap.Pop(&s.pending)
	}
	s.pending = nil
	s.keyIndex = nil
	s.materialized = nil
	s.sessionKeys = nil
	return nil
}
