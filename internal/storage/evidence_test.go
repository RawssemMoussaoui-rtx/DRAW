package storage

import (
	"testing"

	"draw/internal/model"
)

func TestNoopEvidenceStore_PutReturnsNonEmptyIDAndNoError(t *testing.T) {
	var s EvidenceStore = NoopEvidenceStore{}
	id, err := s.Put(model.Evidence{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty EvidenceID")
	}
}

func TestNoopEvidenceStore_PutRelationNoError(t *testing.T) {
	var s EvidenceStore = NoopEvidenceStore{}
	if err := s.PutRelation(model.EvidenceRelation{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNoopEvidenceStore_QueryReturnsEmpty(t *testing.T) {
	var s EvidenceStore = NoopEvidenceStore{}
	got := s.Query(EvidenceFilter{})
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %v", got)
	}
}

func TestNoopEvidenceStore_FindRelationsReturnsEmpty(t *testing.T) {
	var s EvidenceStore = NoopEvidenceStore{}
	got := s.FindRelations("topic")
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %v", got)
	}
}

func TestNoopEvidenceStore_SatisfiesInterface(t *testing.T) {
	var _ EvidenceStore = NoopEvidenceStore{}
}
