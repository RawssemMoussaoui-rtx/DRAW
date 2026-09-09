package master_test

import (
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"draw/internal/frontier"
	"draw/internal/model"
	"draw/internal/storage"
)

// TestSVE3_RealConcurrentSessions runs 3 sessions CONCURRENTLY (real goroutines
// + sync.WaitGroup) against the SAME shared Master/Scheduler/Frontier/EvidenceStore
// in order to stress the session-scoped dedup layer introduced by the SVE-1 rekeying
// change: composite session-scoped keys in frontier.byKey (sessionKey) and in
// scheduler.sessionKeys/materialized/keyIndex (sessionTaskKey).
//
// Design note on Master.Run (single-session by contract):
// SubmitIntent overwrites the Master's single m.state and Scheduler.Admit is
// session-agnostic (it pops the highest-priority task across ALL sessions). Two
// Master.Run loops on one shared Scheduler therefore race: Admit in one session
// can steal another session's task, and once stolen the victim Master never sees
// that task complete in its own m.pending -> drained() stays false forever
// (deadlock) and m.state is overwritten (cross-session state bleed). The existing
// TestSVE3_SessionScopedDedup is sequential (a for-loop) precisely because the
// Master is intentionally single-session. This concurrent test therefore exercises
// the dedup surface that SVE-3 actually protects (Frontier.Push/Has/ResetSession
// + Scheduler.Submit/ResetSession) under real goroutines with overlapping
// sessions, then runs one sequential Master.Run on the shared graph to confirm the
// end-to-end Master path still behaves on the shared infra.
func TestSVE3_RealConcurrentSessions(t *testing.T) {
	m, sched, fr, es, _ := sve3Setup(t)

	const n = 3
	sids := []model.SessionID{
		model.NewSessionID(),
		model.NewSessionID(),
		model.NewSessionID(),
	}
	// Pairwise-and-triple-overlapping host sets: every host is shared by 2-3
	// sessions so a re-keying bug (non-composite or session-agnostic dedup) would
	// collapse entries across sessions.
	hosts := [][]string{
		{"alpha.example", "beta.example", "gamma.example"},
		{"beta.example", "gamma.example", "delta.example"},
		{"gamma.example", "delta.example", "epsilon.example"},
	}

	// --- Phase A: concurrent session-scoped frontier + task ingestion ---
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			cands := make([]frontier.URLCandidate, 0, len(hosts[i]))
			for _, h := range hosts[i] {
				u, err := url.Parse("https://" + h + "/p")
				if err != nil {
					t.Errorf("goroutine %d url.Parse(%q): %v", i, h, err)
					return
				}
				cands = append(cands, frontier.URLCandidate{
					SessionID:    sids[i],
					URL:          u,
					Domain:       h,
					SourceClass:  model.SourceClassUnknown,
					PriorityHint: 500,
				})
			}
			if err := fr.Push(cands); err != nil {
				t.Errorf("goroutine %d fr.Push: %v", i, err)
				return
			}

			// The same TaskKey string is reused across sessions for each index j;
			// only the session-scoped composite key (sessionTaskKey = sid:taskKey)
			// should keep these distinct, yielding 3 tasks/admitted per session.
			for j := 0; j < len(hosts[i]); j++ {
				u, _ := url.Parse("https://" + hosts[i][j])
				tk := model.Task{
					ID:            model.NewTaskID(),
					SessionID:     sids[i],
					Type:          model.TaskTypeFetchHTTP,
					State:         model.TaskStateReady,
					Priority:      500,
					SourceClass:   model.SourceClassUnknown,
					SourceTarget:  hosts[i][j],
					URL:           u,
					EstimatedCost: 1,
					TaskKey:       fmt.Sprintf("shared-key-%d", j),
					CreatedAt:     time.Unix(1000, 0),
				}
				if err := sched.Submit(tk); err != nil {
					t.Errorf("goroutine %d sched.Submit: %v", i, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()

	// Composite keys keep every (session,url) and (session,taskkey) distinct:
	// 9 distinct frontier entries (alpha x1, beta x2, gamma x3, delta x2, epsilon x1),
	// 9 queued tasks, nothing materialized yet (no Run/drainFrontier).
	if got := fr.Len(); got != 9 {
		t.Errorf("frontier len after concurrent push: got %d want 9 (no cross-session collapse)", got)
	}
	if got := sched.Stats().Queued; got != 9 {
		t.Errorf("scheduler queued after concurrent submit: got %d want 9", got)
	}
	if got := sched.TotalMaterialized(); got != 0 {
		t.Errorf("scheduler materialized after direct submit: got %d want 0", got)
	}

	// Has() is session-agnostic by design, so a URL still owned by other
	// sessions must remain findable after ONE session is reset. ResetSession
	// must purge ONLY the named session's entries (no cross-session leakage).
	// s0 owned alpha only. sched.ResetSession cascades to the Frontier, so both
	// the scheduler's task index and the frontier's byKey/sessionKeys are pruned.
	sched.ResetSession(sids[0])
	if got := fr.Len(); got != 6 {
		t.Errorf("frontier len after reset s0: got %d want 6 (s1+s2 intact)", got)
	}
	if got := sched.Stats().Queued; got != 6 {
		t.Errorf("scheduler queued after reset s0: got %d want 6", got)
	}
	if fr.Has("alpha.example", "https://alpha.example/p") {
		t.Error("alpha was owned only by s0; Has must be false after ResetSession(s0)")
	}
	if !fr.Has("beta.example", "https://beta.example/p") {
		t.Error("beta still owned by s1; Has must remain true after ResetSession(s0) (no leakage)")
	}
	if !fr.Has("epsilon.example", "https://epsilon.example/p") {
		t.Error("epsilon still owned by s2; Has must remain true after ResetSession(s0)")
	}

	// s1 owned beta + shared(gamma,delta). After reset s1, gamma/delta still in s2.
	sched.ResetSession(sids[1])
	if got := fr.Len(); got != 3 {
		t.Errorf("frontier len after reset s1: got %d want 3 (s2 intact)", got)
	}
	if got := sched.Stats().Queued; got != 3 {
		t.Errorf("scheduler queued after reset s1: got %d want 3", got)
	}
	if fr.Has("beta.example", "https://beta.example/p") {
		t.Error("beta fully owned by s0+s1 and both reset; Has must be false")
	}
	if !fr.Has("delta.example", "https://delta.example/p") {
		t.Error("delta still owned by s2; Has must remain true after ResetSession(s1)")
	}
	if !fr.Has("epsilon.example", "https://epsilon.example/p") {
		t.Error("epsilon still owned by s2; Has must remain true after ResetSession(s1)")
	}

	// s2 owned gamma+delta+epsilon. Final reset -> everything clean.
	sched.ResetSession(sids[2])
	if got := fr.Len(); got != 0 {
		t.Errorf("frontier len after reset s2: got %d want 0", got)
	}
	if got := sched.Stats().Queued; got != 0 {
		t.Errorf("scheduler queued after reset s2: got %d want 0", got)
	}
	if got := sched.TotalMaterialized(); got != 0 {
		t.Errorf("scheduler materialized after reset s2: got %d want 0", got)
	}
	q := sched.Stats()
	if q.Queued != 0 || q.Active != 0 {
		t.Errorf("scheduler not fully drained after resets: queued=%d active=%d", q.Queued, q.Active)
	}
	if fr.Has("gamma.example", "https://gamma.example/p") {
		t.Error("gamma fully owned by all sessions and all reset; Has must be false")
	}

	// --- Phase B: single sequential session through the shared Master ---
	// Master.Run is single-session by design, so we drive one session through
	// the shared graph to confirm the rekeyed Scheduler+Frontier still behave
	// correctly end-to-end and leave clean state.
	sidB := sve3RunSession(t, m, fr,
		[]string{"https://solo.example"},
		[]string{"solo.example", "alt.example"})
	st := m.State()
	if !st.Terminal {
		t.Errorf("shared master Run not terminal: Terminal=%v reason=%q", st.Terminal, st.TerminalReason)
	}
	if got := fr.Len(); got != 0 {
		t.Errorf("frontier not clean after Run defer ResetSession: got %d want 0", got)
	}
	if got := sched.TotalMaterialized(); got != 0 {
		t.Errorf("scheduler materialized not clean after Run: got %d want 0", got)
	}
	qq := sched.Stats()
	if qq.Queued != 0 || qq.Active != 0 {
		t.Errorf("scheduler not drained after Run: queued=%d active=%d", qq.Queued, qq.Active)
	}
	// Deterministic evidence: the fake manager is deterministic per host, so the
	// sequential session on the shared graph yields a stable, non-empty result.
	evs := es.Query(storage.EvidenceFilter{SessionID: sidB})
	if len(evs) == 0 {
		t.Error("expected non-empty deterministic evidence for the sequential session")
	}
}
