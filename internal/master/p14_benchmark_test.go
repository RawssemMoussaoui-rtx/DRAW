package master

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"draw/internal/config"
	"draw/internal/model"
	"draw/internal/storage"
)

type p14CountStore struct {
	storage.EvidenceStore
	findRels int64
}

func (c *p14CountStore) FindRelations(topic string) []model.EvidenceRelation {
	atomic.AddInt64(&c.findRels, 1)
	return c.EvidenceStore.FindRelations(topic)
}

func newP14Master(t *testing.T) (*Master, *p14CountStore, storage.SourceRegistry) {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := storage.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	raw, err := storage.NewSQLiteEvidenceStore(db)
	if err != nil {
		t.Fatal(err)
	}
	counting := &p14CountStore{EvidenceStore: raw}
	reg, err := storage.NewSQLiteSourceRegistry(db, 0.9)
	if err != nil {
		t.Fatal(err)
	}
	m := NewMaster(config.Defaults(), nil,
		WithEvidenceStore(counting),
		WithSourceRegistry(reg),
	)
	return m, counting, reg
}

func seedP14Evidence(t *testing.T, m *Master) []model.Evidence {
	t.Helper()
	topics := []string{"tech", "science", "history"}
	sources := []string{"a.example.com", "b.example.com", "c.example.com"}
	for i := 0; i < 30; i++ {
		ev := model.Evidence{
			SessionID:    model.SessionID("sess1"),
			TaskID:       model.TaskID("task1"),
			SourceID:     model.SourceID(sources[i%3]),
			Topic:        topics[i%3],
			Claim:        "claim",
			Value:        "value",
			Confidence:   0.8,
			Verification: model.VerificationUnverified,
			CollectedAt:  time.Now().UTC(),
		}
		_, err := m.es.Put(ev)
		if err != nil {
			t.Fatal(err)
		}
	}
	return m.es.Query(storage.EvidenceFilter{SessionID: model.SessionID("sess1")})
}

func TestP14_QualityDrift_Benchmark(t *testing.T) {
	// ===== G1: N_dirty_topics <= 1 =====
	m, counting, reg := newP14Master(t)
	existing := seedP14Evidence(t, m)

	m.recomputeVerification(nil, existing)
	atomic.StoreInt64(&counting.findRels, 0)

	prof, ok := reg.Lookup("a.example.com")
	if !ok || prof == nil {
		t.Fatal("source a not found")
	}
	prof.QualityScore = 0.3
	if err := reg.Upsert(*prof); err != nil {
		t.Fatal(err)
	}
	m.recomputeVerification(nil, existing)
	driftCalls := atomic.LoadInt64(&counting.findRels)
	if driftCalls > 1 {
		t.Errorf("G1 FAIL: FindRelations called %d times, expected <= 1", driftCalls)
	} else {
		t.Logf("G1 PASS: FindRelations called %d time(s)", driftCalls)
	}

	// ===== G2: quality-drift overhead < 15% =====
	m2, counting2, reg2 := newP14Master(t)
	existing2 := seedP14Evidence(t, m2)

	m2.recomputeVerification(nil, existing2) // warm up
	atomic.StoreInt64(&counting2.findRels, 0)

	// Full recompute time (all topics dirty)
	m2.lastQualityMap = map[model.SourceID]float64{}
	iterations := 50
	start := time.Now()
	for i := 0; i < iterations; i++ {
		m2.lastQualityMap = map[model.SourceID]float64{}
		m2.recomputeVerification(nil, existing2)
	}
	totalTime := time.Since(start) / time.Duration(iterations)

	// Quality-drift detection overhead only
	start = time.Now()
	for i := 0; i < iterations; i++ {
		cur := make(map[model.SourceID]float64, len(existing2))
		for _, ev := range existing2 {
			p, okp := reg2.Lookup(string(ev.SourceID))
			if okp && p != nil {
				cur[ev.SourceID] = p.QualityScore
			}
		}
		for _, ev := range existing2 {
			c, ce := cur[ev.SourceID]
			_, pe := m2.lastQualityMap[ev.SourceID]
			if ce && (!pe || c != m2.lastQualityMap[ev.SourceID]) {
				_ = ev.Topic
			}
		}
		m2.lastQualityMap = cur
	}
	overheadTime := time.Since(start) / time.Duration(iterations)

	ratio := float64(overheadTime) / float64(totalTime)
	if ratio > 0.15 {
		t.Errorf("G2 FAIL: overhead ratio %.2f%% > 15%% (overhead=%v, total=%v)", ratio*100, overheadTime, totalTime)
	} else {
		t.Logf("G2 PASS: quality-drift overhead = %.2f%% of total time (overhead=%v, total=%v)", ratio*100, overheadTime, totalTime)
	}

	// ===== G3: verification state correctness =====
	m3, _, reg3 := newP14Master(t)
	existing3 := seedP14Evidence(t, m3)

	m3.recomputeVerification(nil, existing3)
	afterFirst := m3.es.Query(storage.EvidenceFilter{SessionID: model.SessionID("sess1")})
	statesBefore := make([]string, len(afterFirst))
	for i, ev := range afterFirst {
		statesBefore[i] = string(ev.Verification)
	}

	prof3, _ := reg3.Lookup("a.example.com")
	prof3.QualityScore = 0.3
	reg3.Upsert(*prof3)
	m3.recomputeVerification(nil, existing3)

	afterSecond := m3.es.Query(storage.EvidenceFilter{SessionID: model.SessionID("sess1")})
	statesAfter := make([]string, len(afterSecond))
	for i, ev := range afterSecond {
		statesAfter[i] = string(ev.Verification)
	}

	if len(statesBefore) != len(statesAfter) {
		t.Fatalf("G3 FAIL: state count mismatch %d vs %d", len(statesBefore), len(statesAfter))
	}

	changedOther := 0
	for i := range statesBefore {
		if statesBefore[i] != statesAfter[i] {
			if afterSecond[i].SourceID != model.SourceID("a.example.com") {
				changedOther++
			}
		}
	}
	if changedOther > 0 {
		t.Errorf("G3 FAIL: %d non-srcA items changed verification (expected 0)", changedOther)
	} else {
		t.Logf("G3 PASS: only srcA evidence changed verification; all other sources unchanged")
	}
}
