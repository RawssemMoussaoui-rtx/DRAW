package master

import (
	"context"
	"net/http"
	"testing"
	"time"

	"draw/internal/config"
	"draw/internal/model"
	"draw/internal/storage"
)

type fakeSourceRegistry struct{}

func (f *fakeSourceRegistry) Lookup(string) (*model.SourceProfile, bool) { return nil, false }
func (f *fakeSourceRegistry) Upsert(model.SourceProfile) error          { return nil }
func (f *fakeSourceRegistry) Deny(string) error                          { return nil }
func (f *fakeSourceRegistry) ProvisionalUpsert(model.SourceProfile) error { return nil }

func TestStoreEvidenceReader_Counts(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("storage open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := storage.Apply(ctx, db); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	es, err := storage.NewSQLiteEvidenceStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteEvidenceStore: %v", err)
	}
	reader := &storeEvidenceReader{es: es}

	sid := model.NewSessionID()
	for i := 0; i < 3; i++ {
		_, err := es.Put(model.Evidence{
			SessionID:    sid,
			TaskID:       model.NewTaskID(),
			SourceID:     model.NewSourceID("src.example"),
			Topic:        "research",
			Claim:        "claim",
			Value:        "value",
			Confidence:   0.5,
			Verification: model.VerificationDisputed,
			CollectedAt:  time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("Put disputed evidence: %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		_, err := es.Put(model.Evidence{
			SessionID:    sid,
			TaskID:       model.NewTaskID(),
			SourceID:     model.NewSourceID("other.example"),
			Topic:        "research",
			Claim:        "claim",
			Value:        "value",
			Confidence:   0.5,
			Verification: model.VerificationUnverified,
			CollectedAt:  time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("Put unverified evidence: %v", err)
		}
	}

	counts := reader.Counts(sid)
	if counts.Contradictions != 3 {
		t.Errorf("expected 3 contradictions (DISPUTED count), got %d", counts.Contradictions)
	}
}

func TestWithEvidenceStore_SetsFields(t *testing.T) {
	es := &storage.NoopEvidenceStore{}
	m := NewMaster(config.Defaults(), newFakeScheduler(&fakeManager{}, 4),
		WithEvidenceStore(es),
	)
	if m.es == nil {
		t.Fatal("expected m.es to be set after WithEvidenceStore")
	}
	if m.evidence == nil {
		t.Fatal("expected m.evidence to be set after WithEvidenceStore")
	}
	if _, ok := m.evidence.(*storeEvidenceReader); !ok {
		t.Fatalf("expected m.evidence to be *storeEvidenceReader, got %T", m.evidence)
	}
}

func TestWithSourceRegistry_SetsFields(t *testing.T) {
	reg := &fakeSourceRegistry{}
	m := NewMaster(config.Defaults(), newFakeScheduler(&fakeManager{}, 4),
		WithSourceRegistry(reg),
	)
	if m.reg == nil {
		t.Fatal("expected m.reg to be set after WithSourceRegistry")
	}
}

func TestExtractAndStoreEvidence_NilStoreIsNoOp(t *testing.T) {
	m := NewMaster(config.Defaults(), newFakeScheduler(&fakeManager{}, 4))
	if m.es != nil {
		t.Fatal("expected m.es to be nil without WithEvidenceStore")
	}

	task := model.Task{
		SessionID:    model.NewSessionID(),
		Type:         model.TaskTypeFetchHTTP,
		SourceTarget: "example.com",
		CreatedAt:    time.Now().UTC(),
	}
	result := &model.TaskResult{
		Status: model.RetrievalStatusSuccess,
		Data:   []byte(`[{"key":"value"}]`),
		Headers: http.Header{
			"Content-Type": []string{"application/json"},
		},
	}

	m.extractAndStoreEvidence(context.Background(), task, result)

	if len(result.Evidence) != 0 {
		t.Errorf("expected no evidence appended with nil store, got %d", len(result.Evidence))
	}
}

func TestExtractAndStoreEvidence_JSONExtraction(t *testing.T) {
	m, _, _, _ := newMasterWithEvidence(t)
	sid := model.NewSessionID()

	task := model.Task{
		SessionID:    sid,
		Type:         model.TaskTypeFetchHTTP,
		SourceTarget: "data.example.com",
		CreatedAt:    time.Now().UTC(),
	}
	result := &model.TaskResult{
		Status: model.RetrievalStatusSuccess,
		Data:   []byte(`[{"temperature":10},{"humidity":50}]`),
		Headers: http.Header{
			"Content-Type": []string{"application/json"},
		},
	}

	m.extractAndStoreEvidence(context.Background(), task, result)

	if len(result.Evidence) == 0 {
		t.Fatal("expected evidence IDs to be appended to result.Evidence")
	}

	stored := m.es.Query(storage.EvidenceFilter{SessionID: sid})
	if len(stored) == 0 {
		t.Fatal("expected evidence to be stored in SQLite store")
	}
	if len(stored) != len(result.Evidence) {
		t.Errorf("stored evidence count: got %d, want %d", len(stored), len(result.Evidence))
	}
}

func TestExtractAndStoreEvidence_ContradictionCircuit(t *testing.T) {
	m, _, _, _ := newMasterWithEvidence(t)
	sid := model.NewSessionID()

	sources := []string{"alpha.example", "beta.example", "gamma.example"}
	values := []string{"10", "20", "30"}

	for i, src := range sources {
		task := model.Task{
			SessionID:    sid,
			Type:         model.TaskTypeFetchHTTP,
			SourceTarget: src,
			CreatedAt:    time.Now().UTC(),
		}
		result := &model.TaskResult{
			Status: model.RetrievalStatusSuccess,
			Data:   []byte(`[{"temperature":` + values[i] + `}]`),
			Headers: http.Header{
				"Content-Type": []string{"application/json"},
			},
		}
		m.extractAndStoreEvidence(context.Background(), task, result)
	}

	reader := &storeEvidenceReader{es: m.es}
	counts := reader.Counts(sid)
	if counts.Contradictions < 2 {
		t.Errorf("expected at least 2 DISPUTED contradictions, got %d", counts.Contradictions)
	}
}

func TestExtractAndStoreEvidence_PopulatesTaskResultEvidence(t *testing.T) {
	m, _, _, _ := newMasterWithEvidence(t)
	sid := model.NewSessionID()

	task := model.Task{
		SessionID:    sid,
		Type:         model.TaskTypeFetchHTTP,
		SourceTarget: "test.example.com",
		CreatedAt:    time.Now().UTC(),
	}
	result := &model.TaskResult{
		Status: model.RetrievalStatusSuccess,
		Data:   []byte(`[{"key1":"val1"},{"key2":"val2"}]`),
		Headers: http.Header{
			"Content-Type": []string{"application/json"},
		},
	}

	m.extractAndStoreEvidence(context.Background(), task, result)

	if len(result.Evidence) == 0 {
		t.Fatal("expected r.Evidence to be populated after extraction")
	}
	for _, id := range result.Evidence {
		if id == "" {
			t.Error("found empty evidence ID in result.Evidence")
		}
	}
}
