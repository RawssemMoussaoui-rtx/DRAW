package evalharness

import (
	"context"
	"testing"
	"time"

	"draw/internal/model"
	"draw/internal/storage"
)

// baseTime is a FIXED timestamp (NOT time.Now()) so that report generation and
// determinism signatures are reproducible across runs (P13).
var baseTime = time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

func setupTestStore(t *testing.T) testStore {
	t.Helper()
	ctx := context.Background()
	// Open through the counting driver wrapper (see perf.go) so that, in
	// addition to the usual in-memory store, we obtain a live *int64 counter
	// for DB query counting. Behaviour is identical to storage.Open(":memory:")
	// — same DSN, same migrations, same stores — only the driver is wrapped.
	db, qcount, err := openCountingDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := storage.Apply(ctx, db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	es, err := storage.NewSQLiteEvidenceStore(db)
	if err != nil {
		t.Fatalf("new evidence store: %v", err)
	}
	evs, err := storage.NewSQLiteEventStore(db)
	if err != nil {
		t.Fatalf("new event store: %v", err)
	}
	return testStore{db: db, ES: es, EVS: evs, sid: "test-session", tid: "test-task", queryCount: qcount}
}

func putEvidence(t *testing.T, s Scenario, st *testStore) []model.Evidence {
	evs := buildEvidence(s, st.sid, st.tid)
	for i := range evs {
		if _, err := st.ES.Put(evs[i]); err != nil {
			t.Fatalf("put evidence %s: %v", evs[i].ID, err)
		}
	}
	return evs
}

func appendSyntheticEvents(t *testing.T, st *testStore, sid model.SessionID) {
	events := []storage.Event{
		{SessionID: sid, TaskID: &st.tid, Kind: "task_completed", Level: "INFO", Message: "task completed", Data: "{}", TS: baseTime},
		{SessionID: sid, TaskID: &st.tid, Kind: "task_completed", Level: "INFO", Message: "task completed", Data: "{}", TS: baseTime},
		{SessionID: sid, TaskID: &st.tid, Kind: "replan_triggered", Level: "WARN", Message: "replan", Data: "{}", TS: baseTime},
		{SessionID: sid, TaskID: &st.tid, Kind: "terminal", Level: "INFO", Message: "terminal", Data: "{}", TS: baseTime},
	}
	for _, e := range events {
		if err := st.EVS.Append(e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
}

// readBack mirrors the R3 read path: Query evidence, FindRelations, Load events.
func readBack(t *testing.T, st *testStore, s Scenario) ObservedState {
	evs := st.ES.Query(storage.EvidenceFilter{SessionID: st.sid})
	edges := st.ES.FindRelations(s.Topic)
	var events []storage.Event
	afterID := int64(0)
	for {
		batch := st.EVS.Load(st.sid, afterID, 100000)
		if len(batch) == 0 {
			break
		}
		events = append(events, batch...)
		afterID = batch[len(batch)-1].ID
		if len(batch) < 100000 {
			break
		}
	}
	if events == nil {
		events = []storage.Event{}
	}
	verif := make(map[model.EvidenceID]model.VerificationState, len(evs))
	for _, ev := range evs {
		verif[ev.ID] = ev.Verification
	}
	return ObservedState{Evidence: evs, Edges: edges, Events: events, Verifications: verif}
}

// qualityMap builds EvidenceID -> source quality score from the scenario.
func qualityMap(s Scenario, evs []model.Evidence) map[model.EvidenceID]float64 {
	m := make(map[model.EvidenceID]float64, len(evs))
	for i, src := range s.Sources {
		m[evidenceID(s.ID, i)] = src.Quality
	}
	return m
}

func countEdges(edges []model.EvidenceRelation, kind string) int {
	var n int
	for _, e := range edges {
		if string(e.Kind) == kind {
			n++
		}
	}
	return n
}
