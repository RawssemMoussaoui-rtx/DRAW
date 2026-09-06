package storage

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"draw/internal/model"
)

func TestSQLiteEvidenceStore_SatisfiesInterface(t *testing.T) {
	var _ EvidenceStore = (*SQLiteEvidenceStore)(nil)
}

func TestSQLiteEvidenceStore_PutGeneratesID(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteEvidenceStore(db)
	if err != nil {
		t.Fatal(err)
	}
	e := mkTestEvidence("s1", "t1", "src1", "physics", "", "", 0)
	id, err := store.Put(e)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty ID")
	}
	if !strings.HasPrefix(string(id), "evd_") {
		t.Fatalf("expected id prefix evd_, got %s", id)
	}
}

func TestSQLiteEvidenceStore_PutAndQuery(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteEvidenceStore(db)
	if err != nil {
		t.Fatal(err)
	}
	session := model.NewSessionID()
	task1 := model.NewTaskID()
	task2 := model.NewTaskID()
	src1 := model.NewSourceID("src1.example")
	src2 := model.NewSourceID("src2.example")
	e1 := model.Evidence{
		SessionID:    session,
		TaskID:       task1,
		SourceID:     src1,
		Topic:        "physics",
		Claim:        "claim1",
		Value:        "value1",
		Confidence:   0.8,
		Verification: model.VerificationUnverified,
		CollectedAt:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	e2 := model.Evidence{
		SessionID:    session,
		TaskID:       task2,
		SourceID:     src2,
		Topic:        "physics",
		Claim:        "claim2",
		Value:        "value2",
		Confidence:   0.9,
		Verification: model.VerificationVerified,
		CollectedAt:  time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
	}
	id1, err := store.Put(e1)
	if err != nil {
		t.Fatalf("Put e1: %v", err)
	}
	id2, err := store.Put(e2)
	if err != nil {
		t.Fatalf("Put e2: %v", err)
	}
	if id1 == "" || id2 == "" {
		t.Fatal("expected non-empty IDs")
	}
	if id1 == id2 {
		t.Fatal("expected distinct IDs")
	}

	got := store.Query(EvidenceFilter{SessionID: session})
	if len(got) != 2 {
		t.Fatalf("expected 2 evidence, got %d", len(got))
	}
	first := got[0]
	second := got[1]
	if first.ID != id1 {
		t.Errorf("first ID = %s, want %s", first.ID, id1)
	}
	if second.ID != id2 {
		t.Errorf("second ID = %s, want %s", second.ID, id2)
	}
	if first.SessionID != session {
		t.Errorf("first SessionID = %s, want %s", first.SessionID, session)
	}
	if first.Topic != "physics" {
		t.Errorf("first Topic = %q, want physics", first.Topic)
	}
	if first.Claim != "claim1" {
		t.Errorf("first Claim = %q, want claim1", first.Claim)
	}
	if first.Value != "value1" {
		t.Errorf("first Value = %q, want value1", first.Value)
	}
	if first.TaskID != task1 {
		t.Errorf("first TaskID = %s, want %s", first.TaskID, task1)
	}
	if first.SourceID != src1 {
		t.Errorf("first SourceID = %s, want %s", first.SourceID, src1)
	}
	if first.Verification != model.VerificationUnverified {
		t.Errorf("first Verification = %s, want UNVERIFIED", first.Verification)
	}
	if !approxEqual(first.Confidence, 0.8) {
		t.Errorf("first Confidence = %v, want 0.8", first.Confidence)
	}
	if !first.CollectedAt.Equal(e1.CollectedAt) {
		t.Errorf("first CollectedAt = %v, want %v", first.CollectedAt, e1.CollectedAt)
	}
	if second.Verification != model.VerificationVerified {
		t.Errorf("second Verification = %s, want VERIFIED", second.Verification)
	}
}

func TestSQLiteEvidenceStore_PutUpdatesVerificationState(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteEvidenceStore(db)
	if err != nil {
		t.Fatal(err)
	}
	session := model.NewSessionID()
	e := model.Evidence{
		SessionID:    session,
		TaskID:       model.NewTaskID(),
		SourceID:     model.NewSourceID("src1"),
		Topic:        "physics",
		Claim:        "claim",
		Value:        "value",
		Confidence:   0.5,
		Verification: model.VerificationUnverified,
		CollectedAt:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	id, err := store.Put(e)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	updated := e
	updated.ID = id
	updated.Verification = model.VerificationDisputed
	updated.Claim = "updated claim"
	updated.Confidence = 0.2
	if _, err := store.Put(updated); err != nil {
		t.Fatalf("Put update: %v", err)
	}
	got := store.Query(EvidenceFilter{SessionID: session})
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
	if got[0].ID != id {
		t.Errorf("ID = %s, want %s", got[0].ID, id)
	}
	if got[0].Verification != model.VerificationDisputed {
		t.Errorf("Verification = %s, want DISPUTED", got[0].Verification)
	}
	if got[0].Claim != "updated claim" {
		t.Errorf("Claim = %q, want updated claim", got[0].Claim)
	}
	if !approxEqual(got[0].Confidence, 0.2) {
		t.Errorf("Confidence = %v, want 0.2", got[0].Confidence)
	}
}

func TestSQLiteEvidenceStore_PutRelationUpsert(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteEvidenceStore(db)
	if err != nil {
		t.Fatal(err)
	}
	e1 := mkTestEvidenceWithID("ev1", "s1", "t1", "src1", "physics", "claim", "value", 0.5)
	e2 := mkTestEvidenceWithID("ev2", "s1", "t2", "src2", "physics", "claim", "value", 0.5)
	if _, err := store.Put(e1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(e2); err != nil {
		t.Fatal(err)
	}
	rel1 := model.EvidenceRelation{
		From:     "ev1",
		To:       "ev2",
		Kind:     model.EvidenceRelationSupports,
		Strength: 0.4,
	}
	if err := store.PutRelation(rel1); err != nil {
		t.Fatalf("PutRelation first: %v", err)
	}
	rel2 := model.EvidenceRelation{
		From:     "ev1",
		To:       "ev2",
		Kind:     model.EvidenceRelationSupports,
		Strength: 0.9,
	}
	if err := store.PutRelation(rel2); err != nil {
		t.Fatalf("PutRelation second: %v", err)
	}
	rels := store.FindRelations("physics")
	if len(rels) != 1 {
		t.Fatalf("expected 1 relation, got %d", len(rels))
	}
	if !approxEqual(rels[0].Strength, 0.9) {
		t.Errorf("Strength = %v, want 0.9", rels[0].Strength)
	}
}

func TestSQLiteEvidenceStore_PutRelationsBatch(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteEvidenceStore(db)
	if err != nil {
		t.Fatal(err)
	}
	e1 := mkTestEvidenceWithID("ev1", "s1", "t1", "src1", "physics", "claim", "value", 0.5)
	e2 := mkTestEvidenceWithID("ev2", "s1", "t2", "src2", "physics", "claim", "value", 0.5)
	e3 := mkTestEvidenceWithID("ev3", "s1", "t3", "src3", "physics", "claim", "value", 0.5)
	for _, e := range []model.Evidence{e1, e2, e3} {
		if _, err := store.Put(e); err != nil {
			t.Fatal(err)
		}
	}

	rels := []model.EvidenceRelation{
		{From: "ev1", To: "ev2", Kind: model.EvidenceRelationSupports, Strength: 0.4},
		{From: "ev1", To: "ev3", Kind: model.EvidenceRelationContradicts, Strength: 1.5},
		{From: "ev2", To: "ev3", Kind: model.EvidenceRelationSupports, Strength: -0.2},
		{From: "ev1", To: "ev2", Kind: model.EvidenceRelationSupports, Strength: 0.9},
	}
	if err := store.PutRelations(rels); err != nil {
		t.Fatalf("PutRelations: %v", err)
	}

	got := store.FindRelations("physics")
	if len(got) != 3 {
		t.Fatalf("expected 3 relations (2 upserted, 1 new), got %d", len(got))
	}

	want := map[string]float64{
		"ev1|ev2|SUPPORTS":          0.9,
		"ev1|ev3|CONTRADICTS":       1.0,
		"ev2|ev3|SUPPORTS":          0.0,
	}
	for _, r := range got {
		key := string(r.From) + "|" + string(r.To) + "|" + string(r.Kind)
		exp, ok := want[key]
		if !ok {
			t.Errorf("unexpected relation %s", key)
			continue
		}
		if !approxEqual(r.Strength, exp) {
			t.Errorf("Strength for %s = %v, want %v", key, r.Strength, exp)
		}
	}

	if err := store.PutRelations(nil); err != nil {
		t.Fatalf("PutRelations(nil) returned error: %v", err)
	}
}

func TestSQLiteEvidenceStore_PutRelationsRollbackOnError(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteEvidenceStore(db)
	if err != nil {
		t.Fatal(err)
	}
	e1 := mkTestEvidenceWithID("ev1", "s1", "t1", "src1", "physics", "claim", "value", 0.5)
	if _, err := store.Put(e1); err != nil {
		t.Fatal(err)
	}

	valid := model.EvidenceRelation{From: "ev1", To: "ev2", Kind: model.EvidenceRelationSupports, Strength: 0.5}
	invalid := model.EvidenceRelation{From: "ev1", To: "ev3", Kind: "BOGUS_KIND", Strength: 0.5}

	if err := store.PutRelations([]model.EvidenceRelation{valid, invalid}); err == nil {
		t.Fatal("expected error for invalid relation kind")
	}

	got := store.FindRelations("physics")
	if len(got) != 0 {
		t.Fatalf("expected rollback: 0 relations, got %d", len(got))
	}
}

func TestSQLiteEvidenceStore_FindRelationsByTopic(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteEvidenceStore(db)
	if err != nil {
		t.Fatal(err)
	}
	e1 := mkTestEvidenceWithID("e1", "s1", "t1", "src1", "T1", "claim1", "value1", 0.5)
	e2 := mkTestEvidenceWithID("e2", "s1", "t2", "src2", "T1", "claim2", "value2", 0.5)
	e3 := mkTestEvidenceWithID("e3", "s1", "t3", "src3", "T2", "claim3", "value3", 0.5)
	for _, e := range []model.Evidence{e1, e2, e3} {
		if _, err := store.Put(e); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	rel := model.EvidenceRelation{From: "e1", To: "e2", Kind: model.EvidenceRelationContradicts, Strength: 0.8}
	if err := store.PutRelation(rel); err != nil {
		t.Fatalf("PutRelation: %v", err)
	}

	t1rels := store.FindRelations("T1")
	if len(t1rels) != 1 {
		t.Fatalf("expected 1 relation for T1, got %d", len(t1rels))
	}
	if t1rels[0].From != "e1" || t1rels[0].To != "e2" {
		t.Errorf("relation = %s -> %s, want e1 -> e2", t1rels[0].From, t1rels[0].To)
	}
	if t1rels[0].Kind != model.EvidenceRelationContradicts {
		t.Errorf("Kind = %s, want CONTRADICTS", t1rels[0].Kind)
	}
	if !approxEqual(t1rels[0].Strength, 0.8) {
		t.Errorf("Strength = %v, want 0.8", t1rels[0].Strength)
	}

	t2rels := store.FindRelations("T2")
	if len(t2rels) != 0 {
		t.Fatalf("expected 0 relations for T2, got %d", len(t2rels))
	}
}

func TestSQLiteEvidenceStore_QueryByVerification(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteEvidenceStore(db)
	if err != nil {
		t.Fatal(err)
	}
	e1 := mkTestEvidenceWithID("ev1", "s1", "t1", "src1", "physics", "claim", "value", 0.5)
	e1.Verification = model.VerificationUnverified
	e2 := mkTestEvidenceWithID("ev2", "s1", "t2", "src2", "physics", "claim", "value", 0.5)
	e2.Verification = model.VerificationDisputed
	if _, err := store.Put(e1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(e2); err != nil {
		t.Fatal(err)
	}
	got := store.Query(EvidenceFilter{Verification: model.VerificationDisputed})
	if len(got) != 1 {
		t.Fatalf("expected 1 DISPUTED, got %d", len(got))
	}
	if got[0].Verification != model.VerificationDisputed {
		t.Errorf("Verification = %s, want DISPUTED", got[0].Verification)
	}
}

func TestSQLiteEvidenceStore_EmptyFilterReturnsAll(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteEvidenceStore(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, id := range []string{"a", "b", "c"} {
		e := mkTestEvidenceWithID(id, "s9", "t"+id, "src"+id, "topic", "claim"+id, "value"+id, 0.5)
		e.Verification = model.VerificationUnverified
		e.CollectedAt = now.Add(time.Duration(i) * time.Hour)
		if _, err := store.Put(e); err != nil {
			t.Fatalf("Put %s: %v", id, err)
		}
	}
	got := store.Query(EvidenceFilter{})
	if len(got) != 3 {
		t.Fatalf("expected 3 evidence, got %d", len(got))
	}
	for run := 0; run < 3; run++ {
		r := store.Query(EvidenceFilter{})
		if len(r) != 3 {
			t.Fatalf("run %d: expected 3, got %d", run, len(r))
		}
		if !reflect.DeepEqual(r, got) {
			t.Errorf("run %d: Query results differ from baseline", run)
		}
	}
}

func mkTestEvidenceWithID(id, session, task, source, topic, claim, value string, confidence float64) model.Evidence {
	return model.Evidence{
		ID:           model.EvidenceID(id),
		SessionID:    model.SessionID(session),
		TaskID:       model.TaskID(task),
		SourceID:     model.SourceID(source),
		Topic:        topic,
		Claim:        claim,
		Value:        value,
		Confidence:   confidence,
		Verification: model.VerificationUnverified,
		CollectedAt:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func mkTestEvidence(session, task, source, topic string, claim, value string, confidence float64) model.Evidence {
	return mkTestEvidenceWithID("", session, task, source, topic, claim, value, confidence)
}

func approxEqual(a, b float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < 1e-9
}
