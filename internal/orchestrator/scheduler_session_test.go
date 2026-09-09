package orchestrator

import (
	"testing"
	"time"

	"draw/internal/config"
	"draw/internal/frontier"
	"draw/internal/model"
)

// SVE-2: Session-scoped dedup — ResetSession must purge only the given
// session's materialized / keyIndex / sessionKeys entries while leaving
// other sessions intact. The frontier itself is cleaned by the MemoryFrontier.
func TestSchedulerResetSessionSVE2(t *testing.T) {
	cfg := config.Defaults()
	cfg.MaxGlobalConcurrency = 3
	rc := NewResourceController(cfg, NoopResourceSampler{})
	fr := frontier.NewMemoryFrontier(cfg, NoopSourceRegistry{})
	s := NewScheduler(cfg, fr, rc, NoopSourceRegistry{}, DefaultTaskCapabilities())
	// No manager registered → drainFrontier still runs (materializing frontier
	// candidates), but no task is eligible for admission. The candidates
	// therefore remain in pending / keyIndex / materialized after Admit.
	now := time.Unix(1000, 0)

	s1 := model.SessionID("ses1")
	s2 := model.SessionID("ses2")
	u := mustURL(t, "https://a.example/x")
	if err := fr.Push([]frontier.URLCandidate{
		{SessionID: s1, URL: u, Domain: "a.example", PriorityHint: 5},
		{SessionID: s2, URL: u, Domain: "a.example", PriorityHint: 5},
	}); err != nil {
		t.Fatal(err)
	}

	// Drive drainFrontier via Admit. Both sessions materialize the same URL
	// independently thanks to session-scoped materialized keys.
	if _, _, _, ok := s.Admit(now); ok {
		t.Fatal("expected no admit without a registered manager")
	}

	// Pre-reset assertions: both sessions present in all indices.
	mk1 := sessionTaskKey(s1, frontier.CanonicalKey(u))
	mk2 := sessionTaskKey(s2, frontier.CanonicalKey(u))
	s.mu.Lock()
	if len(s.materialized) != 2 {
		t.Fatalf("materialized entries: got %d want 2", len(s.materialized))
	}
	if len(s.keyIndex) != 2 {
		t.Fatalf("keyIndex entries: got %d want 2", len(s.keyIndex))
	}
	if len(s.sessionKeys) != 2 {
		t.Fatalf("sessionKeys entries: got %d want 2", len(s.sessionKeys))
	}
	if len(s.sessionKeys[s1]) != 2 {
		t.Errorf("sessionKeys[S1] len: got %d want 2", len(s.sessionKeys[s1]))
	}
	if len(s.sessionKeys[s2]) != 2 {
		t.Errorf("sessionKeys[S2] len: got %d want 2", len(s.sessionKeys[s2]))
	}
	if len(s.pending) != 2 {
		t.Errorf("pending len: got %d want 2", len(s.pending))
	}
	s.mu.Unlock()

	// Reset only S1.
	s.ResetSession(s1)

	// Post-reset: S1 gone, S2 intact across materialized, keyIndex, pending,
	// sessionKeys, and the frontier.
	s.mu.Lock()
	if s.materialized[mk1] {
		t.Error("materialized[S1] still present after ResetSession(S1)")
	}
	if !s.materialized[mk2] {
		t.Error("materialized[S2] missing after ResetSession(S1), expected intact")
	}
	if len(s.materialized) != 1 {
		t.Errorf("materialized entries after reset: got %d want 1 (S2 only)", len(s.materialized))
	}
	if len(s.keyIndex) != 1 {
		t.Errorf("keyIndex entries after reset: got %d want 1 (S2 only)", len(s.keyIndex))
	}
	for key := range s.keyIndex {
		if key == sessionTaskKey(s1, s.keyIndex[key].task.TaskKey) {
			t.Errorf("keyIndex still contains S1 entry: %q", key)
		}
	}
	if keys, ok := s.sessionKeys[s1]; ok {
		t.Errorf("sessionKeys[S1] still present after ResetSession: %v", keys)
	}
	if keys, ok := s.sessionKeys[s2]; !ok {
		t.Error("sessionKeys[S2] missing after ResetSession(S1), expected intact")
	} else if len(keys) != 2 {
		t.Errorf("sessionKeys[S2] len after reset: got %d want 2", len(keys))
	}
	if len(s.pending) != 1 {
		t.Errorf("pending len after reset: got %d want 1 (S2 only)", len(s.pending))
	}
	s.mu.Unlock()

	if fr.Len() != 1 {
		t.Errorf("frontier len after reset: got %d want 1 (S2 only)", fr.Len())
	}
}
