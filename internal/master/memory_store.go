package master

import (
	"sync"
	"time"

	"draw/internal/model"
)

type MemorySessionStore struct {
	sync.RWMutex
	byID   map[model.SessionID]*model.Session
	byUser map[string]*model.Session
}

func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{
		byID:   map[model.SessionID]*model.Session{},
		byUser: map[string]*model.Session{},
	}
}

func (m *MemorySessionStore) Current(user string) (*model.Session, bool) {
	m.RLock()
	defer m.RUnlock()
	s, ok := m.byUser[user]
	if !ok {
		return nil, false
	}
	cp := *s
	return &cp, true
}

func (m *MemorySessionStore) Save(s *model.Session) error {
	m.Lock()
	defer m.Unlock()
	cp := *s
	m.byID[s.ID] = &cp
	if s.UserID != "" {
		m.byUser[s.UserID] = &cp
	}
	return nil
}

func (m *MemorySessionStore) Archive(id model.SessionID) error { return m.mark(id, model.SessionStateArchived) }

func (m *MemorySessionStore) mark(id model.SessionID, state model.SessionState) error {
	m.Lock()
	defer m.Unlock()
	s, ok := m.byID[id]
	if !ok {
		return nil
	}
	s.State = state
	return nil
}

func (m *MemorySessionStore) Recover(id model.SessionID) (*model.Session, error) {
	m.RLock()
	defer m.RUnlock()
	s, ok := m.byID[id]
	if !ok {
		return nil, nil
	}
	cp := *s
	return &cp, nil
}

func (m *MemorySessionStore) PruneExpired(now time.Time) int { return 0 }
