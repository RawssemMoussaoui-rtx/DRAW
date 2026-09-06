package storage

import "draw/internal/model"

type EvidenceFilter struct {
	SessionID    model.SessionID
	Topic        string
	Verification model.VerificationState
}

type EvidenceStore interface {
	Put(model.Evidence) (model.EvidenceID, error)
	PutRelation(model.EvidenceRelation) error
	PutRelations([]model.EvidenceRelation) error
	Query(EvidenceFilter) []model.Evidence
	FindRelations(topic string) []model.EvidenceRelation
}

type NoopEvidenceStore struct{}

func (NoopEvidenceStore) Put(e model.Evidence) (model.EvidenceID, error) {
	id := model.NewEvidenceID()
	return id, nil
}
func (NoopEvidenceStore) PutRelation(model.EvidenceRelation) error        { return nil }
func (NoopEvidenceStore) PutRelations([]model.EvidenceRelation) error      { return nil }
func (NoopEvidenceStore) Query(EvidenceFilter) []model.Evidence            { return nil }
func (NoopEvidenceStore) FindRelations(string) []model.EvidenceRelation    { return nil }
