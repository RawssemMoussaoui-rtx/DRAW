package orchestrator

import "container/heap"

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
	return nil
}
