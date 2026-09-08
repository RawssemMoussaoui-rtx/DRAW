package storage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"draw/internal/model"
)

func TestSQLiteEvidenceStore_OriginURLAndExtractionSeq_RoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteEvidenceStore(db)
	if err != nil {
		t.Fatal(err)
	}

	originURL := "https://seed.example.com/page"
	seq := 3
	e := model.Evidence{
		ID:           model.NewEvidenceID(),
		SessionID:    model.NewSessionID(),
		TaskID:       model.NewTaskID(),
		SourceID:     model.NewSourceID("seed.example.com"),
		Topic:        "research",
		Claim:        "price",
		Value:        "100",
		Confidence:   0.9,
		Verification: model.VerificationUnverified,
		CollectedAt:  time.Now().UTC(),
		OriginURL:    &originURL,
		ExtractionSeq: &seq,
	}

	id, err := store.Put(e)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if id != e.ID {
		t.Errorf("Put returned id %s, want %s", id, e.ID)
	}

	got := store.Query(EvidenceFilter{SessionID: e.SessionID})
	if len(got) != 1 {
		t.Fatalf("expected 1 evidence, got %d", len(got))
	}
	row := got[0]
	if row.OriginURL == nil {
		t.Fatal("expected OriginURL non-nil after round-trip")
	}
	if *row.OriginURL != originURL {
		t.Errorf("OriginURL = %q, want %q", *row.OriginURL, originURL)
	}
	if row.ExtractionSeq == nil {
		t.Fatal("expected ExtractionSeq non-nil after round-trip")
	}
	if *row.ExtractionSeq != seq {
		t.Errorf("ExtractionSeq = %d, want %d", *row.ExtractionSeq, seq)
	}
}

func TestSQLiteEvidenceStore_OriginURLEvidenceColumnsDirectlyPopulated(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteEvidenceStore(db)
	if err != nil {
		t.Fatal(err)
	}

	originURL := "https://direct.example.com/p"
	seq := 7
	e := model.Evidence{
		ID:           model.NewEvidenceID(),
		SessionID:    model.NewSessionID(),
		TaskID:       model.NewTaskID(),
		SourceID:     model.NewSourceID("direct.example.com"),
		Topic:        "research",
		Claim:        "claim1",
		Value:        "val1",
		Confidence:   0.8,
		Verification: model.VerificationUnverified,
		CollectedAt:  time.Now().UTC(),
		OriginURL:    &originURL,
		ExtractionSeq: &seq,
	}

	if _, err := store.Put(e); err != nil {
		t.Fatalf("Put: %v", err)
	}

	var rawURL sql.NullString
	var rawSeq sql.NullInt64
	q := `SELECT origin_url, extraction_seq FROM evidence WHERE id = ?`
	if err := db.QueryRowContext(ctx, q, string(e.ID)).Scan(&rawURL, &rawSeq); err != nil {
		t.Fatalf("raw SELECT: %v", err)
	}
	if !rawURL.Valid {
		t.Fatal("origin_url column is NULL in evidence table; expected populated (G2: no JOIN needed)")
	}
	if rawURL.String != originURL {
		t.Errorf("origin_url column = %q, want %q", rawURL.String, originURL)
	}
	if !rawSeq.Valid {
		t.Fatal("extraction_seq column is NULL in evidence table; expected populated (G2: no JOIN needed)")
	}
	if int(rawSeq.Int64) != seq {
		t.Errorf("extraction_seq column = %d, want %d", rawSeq.Int64, seq)
	}
}

func TestSQLiteEvidenceStore_PreMigrationRow_NullOriginURLAndExtractionSeq(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteEvidenceStore(db)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	const insertNulls = `INSERT INTO evidence (id, session_id, task_id, source_id, topic, claim, value, confidence, verification_state, collected_at, origin_url, extraction_seq) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL)`
	_, err = db.ExecContext(ctx, insertNulls,
		"ev_pre", "s_pre", "t_pre", "src_pre", "research", "claim", "value", 0.5, "UNVERIFIED", now,
	)
	if err != nil {
		t.Fatalf("insert pre-migration row: %v", err)
	}

	got := store.Query(EvidenceFilter{SessionID: "s_pre"})
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	row := got[0]
	if row.OriginURL != nil {
		t.Errorf("expected OriginURL nil for pre-migration NULL row, got %v", *row.OriginURL)
	}
	if row.ExtractionSeq != nil {
		t.Errorf("expected ExtractionSeq nil for pre-migration NULL row, got %v", *row.ExtractionSeq)
	}
}
