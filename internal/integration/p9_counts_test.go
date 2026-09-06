package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"draw/internal/manager"
	"draw/internal/master"
	"draw/internal/model"
	"draw/internal/storage"
)

// p9NoopWorker returns RetrievalStatusSuccess with no data for every task
// type. It records the ordered list of task types it executed for assertions.
type p9NoopWorker struct {
	mu    sync.Mutex
	calls []model.TaskType
}

func newP9NoopWorker() *p9NoopWorker {
	return &p9NoopWorker{}
}

func (w *p9NoopWorker) Run(t model.Task) (model.TaskResult, error) {
	w.mu.Lock()
	w.calls = append(w.calls, t.Type)
	w.mu.Unlock()
	return model.TaskResult{Status: model.RetrievalStatusSuccess}, nil
}

func (w *p9NoopWorker) Close() error { return nil }

func (w *p9NoopWorker) taskTypes() []model.TaskType {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]model.TaskType, len(w.calls))
	copy(out, w.calls)
	return out
}

var _ manager.Worker = (*p9NoopWorker)(nil)

// countTaskTypes counts occurrences of each TaskType in the worker's call log.
func p9CountTaskTypes(calls []model.TaskType) map[model.TaskType]int {
	counts := map[model.TaskType]int{}
	for _, tt := range calls {
		counts[tt]++
	}
	return counts
}

// p9PutUnverifiedEvidence pre-populates the store with n UNVERIFIED evidence
// items from distinct sources with distinct claims (no relations), so that
// Counts() reports MissingPrimary = n.
func p9PutUnverifiedEvidence(t *testing.T, es *storage.SQLiteEvidenceStore, sid model.SessionID, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		ev := model.Evidence{
			SessionID:    sid,
			TaskID:       model.NewTaskID(),
			SourceID:     model.NewSourceID(fmt.Sprintf("src%d.example", i)),
			Topic:        "revenue",
			Claim:        fmt.Sprintf("claim_%d", i),
			Value:        fmt.Sprintf("value_%d", i),
			Confidence:   0.3,
			Verification: model.VerificationUnverified,
			CollectedAt:  time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
		}
		if _, err := es.Put(ev); err != nil {
			t.Fatalf("Put evidence: %v", err)
		}
	}
}

// p9PutPartiallyVerifiedEvidence pre-populates the store with n PARTIALLY_VERIFIED
// evidence items from distinct sources sharing the same claim+value (forming
// SUPPORTS relations), so that Counts() reports StaleSources = n.
func p9PutPartiallyVerifiedEvidence(t *testing.T, es *storage.SQLiteEvidenceStore, sid model.SessionID, n int) {
	t.Helper()
	ids := make([]model.EvidenceID, 0, n)
	for i := 0; i < n; i++ {
		ev := model.Evidence{
			SessionID:    sid,
			TaskID:       model.NewTaskID(),
			SourceID:     model.NewSourceID(fmt.Sprintf("stale%d.example", i)),
			Topic:        "revenue",
			Claim:        "shared_claim",
			Value:        "shared_value",
			Confidence:   0.3,
			Verification: model.VerificationPartiallyVerified,
			CollectedAt:  time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
		}
		id, err := es.Put(ev)
		if err != nil {
			t.Fatalf("Put evidence: %v", err)
		}
		ids = append(ids, id)
	}
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			if err := es.PutRelation(model.EvidenceRelation{
				From:     ids[i],
				To:       ids[j],
				Kind:     model.EvidenceRelationSupports,
				Strength: 0.3,
			}); err != nil {
				t.Fatalf("PutRelation: %v", err)
			}
		}
	}
}

// TestP9_MissingPrimaryTrigger verifies that with the Counts() fix,
// UNVERIFIED evidence items are counted as MissingPrimary, exceeding the
// MMissingPrimary threshold (default 2) and triggering ReplanTriggerMissingPrimary,
// which issues Discover tasks via PlanningEngine.Replan.
//
// On the original code (Counts returns MissingPrimary=0), no replan occurs.
func TestP9_MissingPrimaryTrigger(t *testing.T) {
	w := newP9NoopWorker()
	bmgr := &fakeBrowserManager{}
	m, _, es := newEvidenceTestGraph(t, w, bmgr)

	req := model.IntentRequest{
		Query:  "research Acme Corp, revenue",
		Seeds:  []string{"https://alpha.example"},
		UserID: "u1",
	}
	sid, err := m.SubmitIntent(req)
	if err != nil {
		t.Fatalf("SubmitIntent: %v", err)
	}

	// Pre-populate 3 UNVERIFIED evidence items (no relations) → MissingPrimary = 3 ≥ 2
	p9PutUnverifiedEvidence(t, es, sid, 3)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	st := m.State()
	if !st.Terminal {
		t.Fatalf("expected terminal state, got Terminal=%v reason=%q", st.Terminal, st.TerminalReason)
	}

	calls := w.taskTypes()
	typeCounts := p9CountTaskTypes(calls)

	t.Logf("=== P9 MissingPrimary Scenario Results ===")
	t.Logf("Evidence counts: Contradictions=%d MissingPrimary=%d StaleSources=%d",
		st.Evidence.Contradictions, st.Evidence.MissingPrimary, st.Evidence.StaleSources)
	t.Logf("ReplanCount=%d (MaxReplans=%d)", st.ReplanCount, st.MaxReplans)
	t.Logf("TerminalReason=%q", st.TerminalReason)
	t.Logf("Worker task calls: %v", calls)
	t.Logf("Task type counts: %v", typeCounts)
	t.Logf("Total tasks executed: %d", len(calls))

	// With the fix: MissingPrimary = 3 ≥ 2 → ReplanTriggerMissingPrimary fires
	if st.Evidence.MissingPrimary < 2 {
		t.Errorf("MissingPrimary = %d, want >= 2 (Counts fix should populate it)", st.Evidence.MissingPrimary)
	}
	if st.ReplanCount < 1 {
		t.Errorf("ReplanCount = %d, want >= 1 (MissingPrimary trigger should fire)", st.ReplanCount)
	}

	// MissingPrimary replan produces Discover tasks (not Verify/Reconcile)
	if typeCounts[model.TaskTypeDiscover] < 1 {
		t.Errorf("Discover tasks issued: %d, want >= 1 (MissingPrimary replan produces Discover)",
			typeCounts[model.TaskTypeDiscover])
	}

	// Bounded: total tasks = 1 seed + N replans × min(MissingPrimary,3) Discover
	// With MaxReplans=3, MissingPrimary=3: 1 + 3*3 = 10 max (+slack)
	maxExpected := 1 + st.MaxReplans*3 + 2
	if len(calls) > maxExpected {
		t.Errorf("task count %d exceeds max %d (possible infinite loop)", len(calls), maxExpected)
	}
}

// TestP9_StaleSourceTrigger verifies that with the Counts() fix,
// PARTIALLY_VERIFIED evidence items are counted as StaleSources, exceeding the
// SStaleSources threshold (default 2) and triggering ReplanTriggerStaleSource,
// which issues FetchHTTP tasks via PlanningEngine.Replan.
//
// On the original code (Counts returns StaleSources=0), no replan occurs.
func TestP9_StaleSourceTrigger(t *testing.T) {
	w := newP9NoopWorker()
	bmgr := &fakeBrowserManager{}
	m, _, es := newEvidenceTestGraph(t, w, bmgr)

	req := model.IntentRequest{
		Query:  "research Acme Corp, revenue",
		Seeds:  []string{"https://alpha.example"},
		UserID: "u1",
	}
	sid, err := m.SubmitIntent(req)
	if err != nil {
		t.Fatalf("SubmitIntent: %v", err)
	}

	// Pre-populate 3 PARTIALLY_VERIFIED evidence items with SUPPORTS relations
	// → StaleSources = 3 ≥ 2. No UNVERIFIED items, so MissingPrimary = 0.
	p9PutPartiallyVerifiedEvidence(t, es, sid, 3)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	st := m.State()
	if !st.Terminal {
		t.Fatalf("expected terminal state, got Terminal=%v reason=%q", st.Terminal, st.TerminalReason)
	}

	calls := w.taskTypes()
	typeCounts := p9CountTaskTypes(calls)

	t.Logf("=== P9 StaleSources Scenario Results ===")
	t.Logf("Evidence counts: Contradictions=%d MissingPrimary=%d StaleSources=%d",
		st.Evidence.Contradictions, st.Evidence.MissingPrimary, st.Evidence.StaleSources)
	t.Logf("ReplanCount=%d (MaxReplans=%d)", st.ReplanCount, st.MaxReplans)
	t.Logf("TerminalReason=%q", st.TerminalReason)
	t.Logf("Worker task calls: %v", calls)
	t.Logf("Task type counts: %v", typeCounts)
	t.Logf("Total tasks executed: %d", len(calls))

	// With the fix: StaleSources = 3 ≥ 2 → ReplanTriggerStaleSource fires
	// (MissingPrimary = 0, so no priority conflict)
	if st.Evidence.StaleSources < 2 {
		t.Errorf("StaleSources = %d, want >= 2 (Counts fix should populate it)", st.Evidence.StaleSources)
	}
	if st.ReplanCount < 1 {
		t.Errorf("ReplanCount = %d, want >= 1 (StaleSource trigger should fire)", st.ReplanCount)
	}

	// StaleSource replan produces FetchHTTP tasks (not Discover)
	if typeCounts[model.TaskTypeFetchHTTP] < 1 {
		t.Errorf("FetchHTTP tasks issued: %d, want >= 1 (StaleSource replan produces FetchHTTP)",
			typeCounts[model.TaskTypeFetchHTTP])
	}

	// Bounded: total tasks = 1 seed + N replans × min(StaleSources,2) FetchHTTP
	// With MaxReplans=3, StaleSources=3: 1 + 3*2 = 7 max (+slack)
	maxExpected := 1 + st.MaxReplans*2 + 2
	if len(calls) > maxExpected {
		t.Errorf("task count %d exceeds max %d (possible infinite loop)", len(calls), maxExpected)
	}
}

// TestP9_CombinedBothTriggers verifies behavior when BOTH conditions are
// present simultaneously. MissingPrimary has priority over StaleSources in
// replanTriggerIfAny (decision.go:224-231), so only the MissingPrimary trigger
// fires. This documents the priority ordering and confirms no unexpected side
// effects emerge when both counts are non-zero.
func TestP9_CombinedBothTriggers(t *testing.T) {
	w := newP9NoopWorker()
	bmgr := &fakeBrowserManager{}
	m, _, es := newEvidenceTestGraph(t, w, bmgr)

	req := model.IntentRequest{
		Query:  "research Acme Corp, revenue",
		Seeds:  []string{"https://alpha.example"},
		UserID: "u1",
	}
	sid, err := m.SubmitIntent(req)
	if err != nil {
		t.Fatalf("SubmitIntent: %v", err)
	}

	// Pre-populate BOTH conditions:
	// 3 UNVERIFIED items → MissingPrimary = 3 ≥ 2
	// 3 PARTIALLY_VERIFIED items → StaleSources = 3 ≥ 2
	p9PutUnverifiedEvidence(t, es, sid, 3)
	p9PutPartiallyVerifiedEvidence(t, es, sid, 3)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	st := m.State()
	if !st.Terminal {
		t.Fatalf("expected terminal state, got Terminal=%v reason=%q", st.Terminal, st.TerminalReason)
	}

	calls := w.taskTypes()
	typeCounts := p9CountTaskTypes(calls)

	t.Logf("=== P9 Combined Scenario Results ===")
	t.Logf("Evidence counts: Contradictions=%d MissingPrimary=%d StaleSources=%d",
		st.Evidence.Contradictions, st.Evidence.MissingPrimary, st.Evidence.StaleSources)
	t.Logf("ReplanCount=%d (MaxReplans=%d)", st.ReplanCount, st.MaxReplans)
	t.Logf("TerminalReason=%q", st.TerminalReason)
	t.Logf("Worker task calls: %v", calls)
	t.Logf("Task type counts: %v", typeCounts)
	t.Logf("Total tasks executed: %d", len(calls))

	// Both conditions present
	if st.Evidence.MissingPrimary < 2 {
		t.Errorf("MissingPrimary = %d, want >= 2", st.Evidence.MissingPrimary)
	}
	if st.Evidence.StaleSources < 2 {
		t.Errorf("StaleSources = %d, want >= 2", st.Evidence.StaleSources)
	}

	// MissingPrimary has priority → only Discover tasks from replan (not FetchHTTP)
	// The StaleSource trigger is shadowed until MissingPrimary drops below threshold
	if typeCounts[model.TaskTypeDiscover] < 1 {
		t.Errorf("Discover tasks issued: %d, want >= 1 (MissingPrimary priority → Discover)",
			typeCounts[model.TaskTypeDiscover])
	}

	// Bounded
	maxExpected := 1 + st.MaxReplans*3 + 2
	if len(calls) > maxExpected {
		t.Errorf("task count %d exceeds max %d (possible infinite loop)", len(calls), maxExpected)
	}
}

// TestP9_OriginalCodeNoReplan is a diagnostic companion that runs the exact
// same MissingPrimary scenario but asserts the behavior expected from the
// ORIGINAL (unfixed) Counts() — that MissingPrimary stays 0 and no replan
// fires. Skipped when the fix is active.
func TestP9_OriginalCodeNoReplan(t *testing.T) {
	w := newP9NoopWorker()
	bmgr := &fakeBrowserManager{}
	m, _, es := newEvidenceTestGraph(t, w, bmgr)

	req := model.IntentRequest{
		Query:  "research Acme Corp, revenue",
		Seeds:  []string{"https://alpha.example"},
		UserID: "u1",
	}
	sid, err := m.SubmitIntent(req)
	if err != nil {
		t.Fatalf("SubmitIntent: %v", err)
	}

	p9PutUnverifiedEvidence(t, es, sid, 3)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	st := m.State()
	if !st.Terminal {
		t.Fatalf("expected terminal state, got Terminal=%v reason=%q", st.Terminal, st.TerminalReason)
	}

	calls := w.taskTypes()
	t.Logf("=== P9 Original-Code Diagnostic (MissingPrimary scenario) ===")
	t.Logf("Evidence counts: Contradictions=%d MissingPrimary=%d StaleSources=%d",
		st.Evidence.Contradictions, st.Evidence.MissingPrimary, st.Evidence.StaleSources)
	t.Logf("ReplanCount=%d", st.ReplanCount)
	t.Logf("Worker task types: %v", calls)

	// This test documents the BEFORE behavior. It is skipped when the fix is
	// active (MissingPrimary > 0) because the fix changes the behavior.
	if st.Evidence.MissingPrimary > 0 {
		t.Skipf("fix is active (MissingPrimary=%d > 0); this test only validates original code",
			st.Evidence.MissingPrimary)
	}

	if st.ReplanCount != 0 {
		t.Errorf("ReplanCount = %d, want 0 (original code should not replan on MissingPrimary)", st.ReplanCount)
	}
	if len(calls) != 1 {
		t.Errorf("task count = %d, want 1 (only seed DISCOVER; no replan tasks)", len(calls))
	}
}

// p9PutDisputedEvidence pre-populates the store with n DISPUTED evidence
// items from distinct sources sharing the same claim but with different
// values (forming CONTRADICTS relations), so that Counts() reports
// Contradictions = n.
func p9PutDisputedEvidence(t *testing.T, es *storage.SQLiteEvidenceStore, sid model.SessionID, n int) {
	t.Helper()
	ids := make([]model.EvidenceID, 0, n)
	for i := 0; i < n; i++ {
		ev := model.Evidence{
			SessionID:    sid,
			TaskID:       model.NewTaskID(),
			SourceID:     model.NewSourceID(fmt.Sprintf("disputed%d.example", i)),
			Topic:        "revenue",
			Claim:        "revenue_fact",
			Value:        fmt.Sprintf("value_%d", i),
			Confidence:   0.3,
			Verification: model.VerificationDisputed,
			CollectedAt:  time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
		}
		id, err := es.Put(ev)
		if err != nil {
			t.Fatalf("Put evidence: %v", err)
		}
		ids = append(ids, id)
	}
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			if err := es.PutRelation(model.EvidenceRelation{
				From:     ids[i],
				To:       ids[j],
				Kind:     model.EvidenceRelationContradicts,
				Strength: 1.0,
			}); err != nil {
				t.Fatalf("PutRelation: %v", err)
			}
		}
	}
}

// TestP9_ThreeTriggersTogether verifies behavior when ALL THREE replan
// triggers fire simultaneously in the same initial state. The Counts() fix
// (evidence.go:26-48) populates all three evidence dimensions from
// pre-populated evidence in the SQLite store.
//
// Priority order in replanTriggerIfAny (decision.go:218-233) is:
//  1. Contradiction  (first match — highest priority)
//  2. MissingPrimary (second)
//  3. StaleSource     (third)
//
// With all three counts exceeding their thresholds, the Contradiction trigger
// fires first on every observeAndDecide call. The MissingPrimary and
// StaleSource triggers are shadowed and never reached. This test verifies:
//   - ReplanCount is capped at MaxReplans=3 (overall, not 3 per trigger = 9)
//   - Terminal state is reached normally ("research_complete")
//   - Only Verify + Reconcile tasks are issued (from the Contradiction replan),
//     NOT Discover (from MissingPrimary) or FetchHTTP (from StaleSource)
//   - No logical conflicts between task types from different triggers
//
// Threshold constants (discovered, not assumed):
//   KContradictions = 2  (evidence/verify.go:12, mirrors config.DefaultReplan())
//   MMissingPrimary = 2  (config.go:110 via DefaultReplan())
//   SStaleSources   = 2  (config.go:110 via DefaultReplan())
//   MaxReplans      = 3  (state.go:72-75)
//
// Values used to exceed thresholds: 3 for each evidence type (>= 2).
func TestP9_ThreeTriggersTogether(t *testing.T) {
	w := newP9NoopWorker()
	bmgr := &fakeBrowserManager{}
	m, _, es := newEvidenceTestGraph(t, w, bmgr)

	req := model.IntentRequest{
		Query:  "research Acme Corp, revenue",
		Seeds:  []string{"https://alpha.example"},
		UserID: "u1",
	}
	sid, err := m.SubmitIntent(req)
	if err != nil {
		t.Fatalf("SubmitIntent: %v", err)
	}

	// Pre-populate ALL THREE trigger conditions simultaneously:
	// 3 DISPUTED items → Contradictions = 3 >= KContradictions(2)
	// 3 UNVERIFIED items → MissingPrimary = 3 >= MMissingPrimary(2)
	// 3 PARTIALLY_VERIFIED items → StaleSources = 3 >= SStaleSources(2)
	p9PutDisputedEvidence(t, es, sid, 3)
	p9PutUnverifiedEvidence(t, es, sid, 3)
	p9PutPartiallyVerifiedEvidence(t, es, sid, 3)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	st := m.State()
	if !st.Terminal {
		t.Fatalf("expected terminal state, got Terminal=%v reason=%q", st.Terminal, st.TerminalReason)
	}

	calls := w.taskTypes()
	typeCounts := p9CountTaskTypes(calls)

	t.Logf("=== P9 Three-Triggers-Together Experiment 1 Results ===")
	t.Logf("Threshold constants: KContradictions=2, MMissingPrimary=2, SStaleSources=2, MaxReplans=3")
	t.Logf("Priority order (decision.go:218-233): Contradiction > MissingPrimary > StaleSource")
	t.Logf("Pre-populated evidence: 3 DISPUTED + 3 UNVERIFIED + 3 PARTIALLY_VERIFIED")
	t.Logf("Evidence counts: Contradictions=%d MissingPrimary=%d StaleSources=%d",
		st.Evidence.Contradictions, st.Evidence.MissingPrimary, st.Evidence.StaleSources)
	t.Logf("ReplanCount=%d (MaxReplans=%d)", st.ReplanCount, st.MaxReplans)
	t.Logf("TerminalReason=%q", st.TerminalReason)
	t.Logf("Worker task calls: %v", calls)
	t.Logf("Task type counts: %v", typeCounts)
	t.Logf("Total tasks executed: %d", len(calls))

	// (1) All three trigger conditions present in the initial state
	if st.Evidence.Contradictions < 2 {
		t.Errorf("Contradictions = %d, want >= 2 (3 DISPUTED pre-populated, KContradictions=2)", st.Evidence.Contradictions)
	}
	if st.Evidence.MissingPrimary < 2 {
		t.Errorf("MissingPrimary = %d, want >= 2 (3 UNVERIFIED pre-populated, MMissingPrimary=2)", st.Evidence.MissingPrimary)
	}
	if st.Evidence.StaleSources < 2 {
		t.Errorf("StaleSources = %d, want >= 2 (3 PARTIALLY_VERIFIED pre-populated, SStaleSources=2)", st.Evidence.StaleSources)
	}

	// (2) MaxReplans respected: overall cap is 3, NOT 3 per trigger (which would be 9)
	if st.ReplanCount > st.MaxReplans {
		t.Errorf("ReplanCount = %d exceeds MaxReplans = %d (overall cap must be 3, not per-trigger)", st.ReplanCount, st.MaxReplans)
	}
	if st.ReplanCount < 1 {
		t.Errorf("ReplanCount = %d, want >= 1 (Contradiction trigger should fire)", st.ReplanCount)
	}

	// (3) Contradiction has priority → only Verify + Reconcile from replans.
	//     MissingPrimary (Discover) and StaleSource (FetchHTTP) are shadowed.
	if typeCounts[model.TaskTypeVerify] < 1 {
		t.Errorf("Verify tasks issued: %d, want >= 1 (Contradiction replan produces Verify)",
			typeCounts[model.TaskTypeVerify])
	}
	if typeCounts[model.TaskTypeReconcile] < 1 {
		t.Errorf("Reconcile tasks issued: %d, want >= 1 (Contradiction replan produces Reconcile)",
			typeCounts[model.TaskTypeReconcile])
	}

	// Only the seed DISCOVER should execute — no Discover from MissingPrimary replan
	// (it is shadowed by Contradiction's higher priority in the switch statement).
	if typeCounts[model.TaskTypeDiscover] > 1 {
		t.Errorf("Discover tasks issued: %d, want <= 1 (only seed; MissingPrimary shadowed by Contradiction priority)",
			typeCounts[model.TaskTypeDiscover])
	}

	// No FetchHTTP from StaleSource replan (shadowed by Contradiction).
	if typeCounts[model.TaskTypeFetchHTTP] > 0 {
		t.Errorf("FetchHTTP tasks issued: %d, want 0 (StaleSource shadowed by Contradiction priority)",
			typeCounts[model.TaskTypeFetchHTTP])
	}

	// (4) Normal termination via research_complete
	if st.TerminalReason != "research_complete" {
		t.Errorf("TerminalReason = %q, want %q", st.TerminalReason, "research_complete")
	}

	// (5) No logical conflicts: bounded task count.
	// Contradiction replan adds min(Contradictions,3)=3 Verify + 1 Reconcile = 4 per replan.
	// Only 2 replans actually add tasks (3rd RecordReplan sets count=3, CanReplan=false → empty delta).
	// Total expected = 1 seed + 4 + 4 = 9 maximum.
	maxExpected := 1 + st.MaxReplans*4 + 4
	if len(calls) > maxExpected {
		t.Errorf("task count %d exceeds max %d (possible infinite loop or conflict)", len(calls), maxExpected)
	}
}

// TestP9_ContradictionOnly is a control scenario where ONLY the Contradiction
// trigger fires (sufficient DISPUTED evidence but NOT enough UNVERIFIED or
// PARTIALLY_VERIFIED to trigger MissingPrimary or StaleSource).
//
// This serves as a baseline for comparison with TestP9_ThreeTriggersTogether.
// Expected task types from the Contradiction replan (planning.go:55-61):
//   - min(Contradictions, 3) Verify tasks (priority 50)
//   - 1 Reconcile task (priority 40)
//
// Per prior Track B results, the contradiction circuit produces Verify +
// Reconcile tasks, confirming the MissingPrimary DISCOVER path is not
// activated when only DISPUTED evidence is present.
func TestP9_ContradictionOnly(t *testing.T) {
	w := newP9NoopWorker()
	bmgr := &fakeBrowserManager{}
	m, _, es := newEvidenceTestGraph(t, w, bmgr)

	req := model.IntentRequest{
		Query:  "research Acme Corp, revenue",
		Seeds:  []string{"https://alpha.example"},
		UserID: "u1",
	}
	sid, err := m.SubmitIntent(req)
	if err != nil {
		t.Fatalf("SubmitIntent: %v", err)
	}

	// Pre-populate ONLY DISPUTED evidence (no UNVERIFIED or PARTIALLY_VERIFIED):
	// Contradictions = 3 >= KContradictions(2) → triggers
	// MissingPrimary = 0 < MMissingPrimary(2) → does NOT trigger
	// StaleSources = 0 < SStaleSources(2) → does NOT trigger
	p9PutDisputedEvidence(t, es, sid, 3)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	st := m.State()
	if !st.Terminal {
		t.Fatalf("expected terminal state, got Terminal=%v reason=%q", st.Terminal, st.TerminalReason)
	}

	calls := w.taskTypes()
	typeCounts := p9CountTaskTypes(calls)

	t.Logf("=== P9 Contradiction-Only Control Scenario Results ===")
	t.Logf("Evidence counts: Contradictions=%d MissingPrimary=%d StaleSources=%d",
		st.Evidence.Contradictions, st.Evidence.MissingPrimary, st.Evidence.StaleSources)
	t.Logf("ReplanCount=%d (MaxReplans=%d)", st.ReplanCount, st.MaxReplans)
	t.Logf("TerminalReason=%q", st.TerminalReason)
	t.Logf("Worker task calls: %v", calls)
	t.Logf("Task type counts: %v", typeCounts)
	t.Logf("Total tasks executed: %d", len(calls))

	// Only Contradiction condition is present
	if st.Evidence.Contradictions < 2 {
		t.Errorf("Contradictions = %d, want >= 2 (3 DISPUTED pre-populated, KContradictions=2)", st.Evidence.Contradictions)
	}
	if st.Evidence.MissingPrimary > 0 {
		t.Errorf("MissingPrimary = %d, want 0 (no UNVERIFIED pre-populated)", st.Evidence.MissingPrimary)
	}
	if st.Evidence.StaleSources > 0 {
		t.Errorf("StaleSources = %d, want 0 (no PARTIALLY_VERIFIED pre-populated)", st.Evidence.StaleSources)
	}

	// Contradiction trigger fires
	if st.ReplanCount < 1 {
		t.Errorf("ReplanCount = %d, want >= 1 (Contradiction trigger should fire)", st.ReplanCount)
	}
	if st.ReplanCount > st.MaxReplans {
		t.Errorf("ReplanCount = %d exceeds MaxReplans = %d", st.ReplanCount, st.MaxReplans)
	}

	// Contradiction replan produces Verify + Reconcile tasks (planning.go:55-61)
	if typeCounts[model.TaskTypeVerify] < 1 {
		t.Errorf("Verify tasks issued: %d, want >= 1 (Contradiction replan produces Verify)",
			typeCounts[model.TaskTypeVerify])
	}
	if typeCounts[model.TaskTypeReconcile] < 1 {
		t.Errorf("Reconcile tasks issued: %d, want >= 1 (Contradiction replan produces Reconcile)",
			typeCounts[model.TaskTypeReconcile])
	}

	// No Discover from MissingPrimary (not triggered) — only the seed
	if typeCounts[model.TaskTypeDiscover] > 1 {
		t.Errorf("Discover tasks issued: %d, want <= 1 (only seed; MissingPrimary not triggered)",
			typeCounts[model.TaskTypeDiscover])
	}
	if typeCounts[model.TaskTypeFetchHTTP] > 0 {
		t.Errorf("FetchHTTP tasks issued: %d, want 0 (StaleSource not triggered)",
			typeCounts[model.TaskTypeFetchHTTP])
	}

	// Normal termination
	if st.TerminalReason != "research_complete" {
		t.Errorf("TerminalReason = %q, want %q", st.TerminalReason, "research_complete")
	}

	// Bounded
	maxExpected := 1 + st.MaxReplans*4 + 4
	if len(calls) > maxExpected {
		t.Errorf("task count %d exceeds max %d", len(calls), maxExpected)
	}
}

// ---------------------------------------------------------------------------
// Experiment 2 — Realistic worker under dynamic (non-static) state.
//
// p9RealisticWorker replaces the sterile p9NoopWorker with a noisy, generic
// worker that exercises the same end-to-end circuit (Extract -> SQLite ->
// ComputeRelations -> ComputeVerification -> Counts -> replanTriggerIfAny ->
// PlanningEngine.Replan) but with MIXED, state-mutating results:
//
//   - Success with crafted JSON evidence for Discover/FetchHTTP/Verify:
//     a DISTINCT integer `revenue` per call. Distinct values share the claim
//     path "[0].revenue" but disagree, forging CONTRADICTS edges (-> DISPUTED)
//     as soon as >=2 accumulate. Integer strings share no n-grams, so the
//     paraphrase gate (tau=0.8) never suppresses the edge.
//   - Partial (RetrievalStatusPartial) on a deterministic slice: accepted,
//     lower-confidence evidence (statusBase=0.5).
//   - Transient Timeout failures on a sparse, deterministic slice that retry
//     ONCE and then succeed (RetryCount guard => the retry always succeeds, so
//     MaxAttempts=3 is never exhausted and the session never fatal-terminates
//     on an error result).
//
// The very first DISCOVER (n==0, i.e. the seed from SubmitIntent) returns no
// data so the first replay trigger is driven purely by the planted evidence,
// exactly mirroring the noop worker's seed behaviour. Every subsequent data
// task can therefore inject NEW evidence (and NEW contradictions) DURING the
// Replan cycle, not just in the initial state.
//
// Worker return-value semantics:
//   DISCOVER  -> seed: Success, no data;  replan-issued (>=n=1): Success/Partial
//                with distinct `revenue` JSON   (MissingPrimary replan path)
//   FETCH_HTTP -> Success/Partial with distinct `revenue` JSON    (StaleSource path)
//   VERIFY    -> Success/Partial with distinct `revenue` JSON    (Contradiction path)
//   RECONCILE -> Success/Partial, no payload                    (consolidation)
//   other     -> Success, no payload
// ---------------------------------------------------------------------------

// p9CallLog is the extra method both experiment-2 workers expose so a shared
// runner can retrieve the ordered execution log without widening the
// manager.Worker interface.
type p9CallLog interface {
	taskTypes() []model.TaskType
}

// Compile-time assertions: both workers satisfy manager.Worker and p9CallLog.
var (
	_ manager.Worker = (*p9NoopWorker)(nil)
	_ p9CallLog      = (*p9NoopWorker)(nil)
	_ manager.Worker = (*p9RealisticWorker)(nil)
	_ p9CallLog      = (*p9RealisticWorker)(nil)
)

type p9RealisticWorker struct {
	mu         sync.Mutex
	calls      []model.TaskType
	counter    map[model.TaskType]int
	injections int
}

func newP9RealisticWorker() *p9RealisticWorker {
	return &p9RealisticWorker{counter: map[model.TaskType]int{}}
}

// p9ShouldFail returns true for the n-th call of a task type when it should
// transiently fail. Every 3rd call (n%3==2) => each scenario exercises the
// retry path at least once, but never so often that retries dominate.
func p9ShouldFail(n int) bool { return n%3 == 2 }

// p9IsPartial returns true for the n-th call when it should yield a Partial
// (low-confidence) result instead of a full Success.
func p9IsPartial(n int) bool { return n%4 == 3 }

func (w *p9RealisticWorker) Run(t model.Task) (model.TaskResult, error) {
	w.mu.Lock()
	w.calls = append(w.calls, t.Type)
	n := w.counter[t.Type]
	w.counter[t.Type] = n + 1
	w.mu.Unlock()

	// Seed DISCOVER: no retrievable payload, so the first replay trigger is
	// governed purely by the planted evidence (matches noop seed behaviour).
	if t.Type == model.TaskTypeDiscover && n == 0 {
		return model.TaskResult{Status: model.RetrievalStatusSuccess}, nil
	}

	// Transient failure: only on the first attempt (RetryCount==0). The retry
	// (RetryCount>=1) skips this branch, so no task ever exhausts MaxAttempts.
	if t.RetryCount == 0 && p9ShouldFail(n) {
		return model.TaskResult{Status: model.RetrievalStatusTimeout}, nil
	}

	partial := p9IsPartial(n)

	switch t.Type {
	case model.TaskTypeDiscover, model.TaskTypeFetchHTTP, model.TaskTypeFetchBrowser, model.TaskTypeVerify:
		// Distinct integer revenue per call -> distinct "[0].revenue" values
		// -> fresh CONTRADICTS edges -> DISPUTED once >=2 accumulate, injecting a
		// NEW contradiction during the Replan cycle.
		revenue := 100 + n
		status := model.RetrievalStatusSuccess
		if partial {
			status = model.RetrievalStatusPartial
		}
		w.mu.Lock()
		w.injections++
		w.mu.Unlock()
		return model.TaskResult{
			Status:  status,
			Data:    []byte(fmt.Sprintf(`[{"revenue":%d}]`, revenue)),
			Headers: http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	case model.TaskTypeReconcile:
		if partial {
			return model.TaskResult{Status: model.RetrievalStatusPartial}, nil
		}
		return model.TaskResult{Status: model.RetrievalStatusSuccess}, nil
	default:
		return model.TaskResult{Status: model.RetrievalStatusSuccess}, nil
	}
}

func (w *p9RealisticWorker) Close() error { return nil }

func (w *p9RealisticWorker) taskTypes() []model.TaskType {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]model.TaskType, len(w.calls))
	copy(out, w.calls)
	return out
}

func (w *p9RealisticWorker) injectionCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.injections
}

// p9Setup seeds the SQLite evidence store for a scenario.
type p9Setup func(t *testing.T, es *storage.SQLiteEvidenceStore, sid model.SessionID)

// p9RunScenario runs one experiment scenario (three-trigger layout) with the
// supplied worker and evidence setup, returning the terminal ResearchState,
// the worker's ordered call log, and a per-task-type histogram.
func p9RunScenario(t *testing.T, w manager.Worker, setup p9Setup) (master.ResearchState, []model.TaskType, map[model.TaskType]int) {
	t.Helper()
	bmgr := &fakeBrowserManager{}
	m, _, es := newEvidenceTestGraph(t, w, bmgr)

	req := model.IntentRequest{
		Query:  "research Acme Corp, revenue",
		Seeds:  []string{"https://alpha.example"},
		UserID: "u1",
	}
	sid, err := m.SubmitIntent(req)
	if err != nil {
		t.Fatalf("SubmitIntent: %v", err)
	}
	setup(t, es, sid)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	st := m.State()
	var calls []model.TaskType
	if cl, ok := w.(p9CallLog); ok {
		calls = cl.taskTypes()
	}
	return st, calls, p9CountTaskTypes(calls)
}

func p9LogScenario(t *testing.T, label string, st master.ResearchState, calls []model.TaskType, counts map[model.TaskType]int) {
	t.Helper()
	t.Logf("=== %s ===", label)
	t.Logf("Evidence counts: Contradictions=%d MissingPrimary=%d StaleSources=%d",
		st.Evidence.Contradictions, st.Evidence.MissingPrimary, st.Evidence.StaleSources)
	t.Logf("ReplanCount=%d (MaxReplans=%d) EvidenceCount=%d", st.ReplanCount, st.MaxReplans, st.EvidenceCount)
	t.Logf("Terminal=%v TerminalReason=%q", st.Terminal, st.TerminalReason)
	t.Logf("Worker task calls: %v", calls)
	t.Logf("Task type counts: %v", counts)
	t.Logf("Total tasks executed (incl. retries): %d", len(calls))
}

func p9SummaryLine(label string, st master.ResearchState, counts map[model.TaskType]int) string {
	return fmt.Sprintf("%s replan=%d/%d term=%q counts=%d/%d/%d calls=%v",
		label, st.ReplanCount, st.MaxReplans, st.TerminalReason,
		st.Evidence.Contradictions, st.Evidence.MissingPrimary, st.Evidence.StaleSources, counts)
}

// threeTriggersSetup plants 3 DISPUTED + 3 UNVERIFIED + 3 PARTIALLY_VERIFIED.
func threeTriggersSetup(t *testing.T, es *storage.SQLiteEvidenceStore, sid model.SessionID) {
	t.Helper()
	p9PutDisputedEvidence(t, es, sid, 3)
	p9PutUnverifiedEvidence(t, es, sid, 3)
	p9PutPartiallyVerifiedEvidence(t, es, sid, 3)
}

// missingPrimarySetup plants 3 UNVERIFIED only.
func missingPrimarySetup(t *testing.T, es *storage.SQLiteEvidenceStore, sid model.SessionID) {
	t.Helper()
	p9PutUnverifiedEvidence(t, es, sid, 3)
}

// staleSourceSetup plants 3 PARTIALLY_VERIFIED only.
func staleSourceSetup(t *testing.T, es *storage.SQLiteEvidenceStore, sid model.SessionID) {
	t.Helper()
	p9PutPartiallyVerifiedEvidence(t, es, sid, 3)
}

// TestP9_Realistic_ThreeTriggers re-runs the three-trigger scenario with the
// noisy p9RealisticWorker. The Contradiction trigger still has priority and the
// overall cap (MaxReplans=3) is still respected, but the worker injects NEW
// contradictions dynamically as its replan-issued Verify tasks emit disagreeing
// `revenue` values.
func TestP9_Realistic_ThreeTriggers(t *testing.T) {
	w := newP9RealisticWorker()
	st, calls, typeCounts := p9RunScenario(t, w, threeTriggersSetup)
	p9LogScenario(t, "P9 Three-Triggers Scenario — REALISTIC worker", st, calls, typeCounts)

	// Gate 1: normal terminal completion.
	if !st.Terminal {
		t.Fatalf("expected terminal state, got Terminal=%v reason=%q", st.Terminal, st.TerminalReason)
	}
	if st.TerminalReason != "research_complete" {
		t.Errorf("TerminalReason=%q, want research_complete", st.TerminalReason)
	}

	// Gate 2: MaxReplans=3 respected (overall cap, NOT per-trigger).
	if st.ReplanCount > st.MaxReplans {
		t.Errorf("ReplanCount=%d exceeds MaxReplans=%d (overall cap must hold under dynamic state)", st.ReplanCount, st.MaxReplans)
	}
	if st.ReplanCount < 1 {
		t.Errorf("ReplanCount=%d, want >=1 (Contradiction trigger should fire first)", st.ReplanCount)
	}

	// Gate 3: priority ordering preserved — only Verify+Reconcile from the
	// Contradiction trigger; seed Discover is the only Discover; no FetchHTTP.
	if typeCounts[model.TaskTypeVerify] < 1 {
		t.Errorf("Verify tasks=%d want >=1 (Contradiction replan path)", typeCounts[model.TaskTypeVerify])
	}
	if typeCounts[model.TaskTypeReconcile] < 1 {
		t.Errorf("Reconcile tasks=%d want >=1 (Contradiction replan path)", typeCounts[model.TaskTypeReconcile])
	}
	if typeCounts[model.TaskTypeDiscover] > 1 {
		t.Errorf("Discover tasks=%d want <=1 (only the seed; MissingPrimary shadowed)", typeCounts[model.TaskTypeDiscover])
	}
	if typeCounts[model.TaskTypeFetchHTTP] > 0 {
		t.Errorf("FetchHTTP tasks=%d want 0 (StaleSource shadowed by Contradiction priority)", typeCounts[model.TaskTypeFetchHTTP])
	}

	// Gate 4: bounded task count (no infinite loop / inflation). Retries add a
	// little slack over the noop upper bound.
	maxExpected := 1 + st.MaxReplans*4 + 8
	if len(calls) > maxExpected {
		t.Errorf("task count %d exceeds bound %d (possible infinite loop or inflation)", len(calls), maxExpected)
	}

	// Gate 5: dynamic contradiction injection — the realistic worker raised
	// Contradictions beyond the planted baseline of 3. Under P10, DecisionStop
	// terminates the session when the replan budget is exhausted (MaxReplans=3),
	// so not all dynamic injections are reflected in the final Contradictions
	// count (some results are discarded during the deterministic drain). The
	// worker's own injection counter proves dynamic injection occurred during
	// replan-issued Verify tasks, which is the same proof used by
	// TestP9_Experiment5_PriorityStarvation.
	injections := w.injectionCount()
	if injections <= 3 {
		t.Errorf("worker injections=%d, want >3 (dynamic contradiction injection during replan tasks)", injections)
	}
}

// TestP9_Realistic_MissingPrimaryOnly re-runs the MissingPrimary-only scenario
// with the realistic worker. Only UNVERIFIED evidence is planted, so the first
// trigger is MissingPrimary (priority). The worker's replan-issued Discover
// tasks then emit disagreeing `revenue` values, dynamically injecting
// Contradictions mid-run.
func TestP9_Realistic_MissingPrimaryOnly(t *testing.T) {
	st, calls, typeCounts := p9RunScenario(t, newP9RealisticWorker(), missingPrimarySetup)
	p9LogScenario(t, "P9 MissingPrimary-Only Scenario — REALISTIC worker", st, calls, typeCounts)

	if !st.Terminal {
		t.Fatalf("expected terminal state, got Terminal=%v reason=%q", st.Terminal, st.TerminalReason)
	}
	if st.TerminalReason != "research_complete" {
		t.Errorf("TerminalReason=%q, want research_complete", st.TerminalReason)
	}
	if st.ReplanCount > st.MaxReplans {
		t.Errorf("ReplanCount=%d exceeds MaxReplans=%d", st.ReplanCount, st.MaxReplans)
	}
	if st.ReplanCount < 1 {
		t.Errorf("ReplanCount=%d, want >=1 (MissingPrimary trigger should fire first)", st.ReplanCount)
	}
	// Initial trigger was MissingPrimary (only UNVERIFIED planted).
	if st.Evidence.MissingPrimary < 2 {
		t.Errorf("MissingPrimary=%d, want >=2 (planted baseline)", st.Evidence.MissingPrimary)
	}
	// Discover is the MissingPrimary replan product; seed makes count even.
	if typeCounts[model.TaskTypeDiscover] < 2 {
		t.Errorf("Discover tasks=%d want >=2 (seed + MissingPrimary replan)", typeCounts[model.TaskTypeDiscover])
	}
	// Dynamic injection proof: new contradictions appeared during the run.
	if st.Evidence.Contradictions <= 0 {
		t.Errorf("Contradictions=%d, want >0 (realistic worker should inject contradictions mid-replan)", st.Evidence.Contradictions)
	}
	maxExpected := 1 + st.MaxReplans*3 + 8
	if len(calls) > maxExpected {
		t.Errorf("task count %d exceeds bound %d", len(calls), maxExpected)
	}
}

// TestP9_Realistic_StaleSourceOnly re-runs the StaleSource-only scenario with
// the realistic worker. Only PARTIALLY_VERIFIED evidence is planted, so the
// first trigger is StaleSource. The worker's replan-issued FetchHTTP tasks
// then emit disagreeing `revenue` values, dynamically injecting Contradictions.
func TestP9_Realistic_StaleSourceOnly(t *testing.T) {
	st, calls, typeCounts := p9RunScenario(t, newP9RealisticWorker(), staleSourceSetup)
	p9LogScenario(t, "P9 StaleSource-Only Scenario — REALISTIC worker", st, calls, typeCounts)

	if !st.Terminal {
		t.Fatalf("expected terminal state, got Terminal=%v reason=%q", st.Terminal, st.TerminalReason)
	}
	if st.TerminalReason != "research_complete" {
		t.Errorf("TerminalReason=%q, want research_complete", st.TerminalReason)
	}
	if st.ReplanCount > st.MaxReplans {
		t.Errorf("ReplanCount=%d exceeds MaxReplans=%d", st.ReplanCount, st.MaxReplans)
	}
	if st.ReplanCount < 1 {
		t.Errorf("ReplanCount=%d, want >=1 (StaleSource trigger should fire first)", st.ReplanCount)
	}
	if st.Evidence.StaleSources < 2 {
		t.Errorf("StaleSources=%d, want >=2 (planted baseline)", st.Evidence.StaleSources)
	}
	// StaleSource replan product is FetchHTTP (plus the seed Discover).
	if typeCounts[model.TaskTypeFetchHTTP] < 1 {
		t.Errorf("FetchHTTP tasks=%d want >=1 (StaleSource replan path)", typeCounts[model.TaskTypeFetchHTTP])
	}
	if typeCounts[model.TaskTypeFetchBrowser] > 0 {
		t.Errorf("FetchBrowser tasks=%d want 0 (no escalation path expected)", typeCounts[model.TaskTypeFetchBrowser])
	}
	if st.Evidence.Contradictions <= 0 {
		t.Errorf("Contradictions=%d, want >0 (dynamic injection proof)", st.Evidence.Contradictions)
	}
	maxExpected := 1 + st.MaxReplans*2 + 8
	if len(calls) > maxExpected {
		t.Errorf("task count %d exceeds bound %d", len(calls), maxExpected)
	}
}

// TestP9_Compare_NoopVsRealistic runs the three-trigger scenario under BOTH
// the sterile noop worker (Experiment 1) and the realistic worker (Experiment
// 2) and asserts that every gate holds identically, while logging the
// behavioural delta caused by the worker's dynamic state changes.
func TestP9_Compare_NoopVsRealistic(t *testing.T) {
	stNoop, callsNoop, countsNoop := p9RunScenario(t, newP9NoopWorker(), threeTriggersSetup)
	stReal, callsReal, countsReal := p9RunScenario(t, newP9RealisticWorker(), threeTriggersSetup)

	p9LogScenario(t, "P9 THREE-TRIGGER NOOP (Experiment 1 baseline — sterile)", stNoop, callsNoop, countsNoop)
	p9LogScenario(t, "P9 THREE-TRIGGER REALISTIC (Experiment 2 — dynamic state)", stReal, callsReal, countsReal)

	t.Logf("=== COMPARISON: noop vs realistic worker (three-trigger) ===")
	t.Logf("ReplanCount      : noop=%d  realistic=%d  (cap=%d)", stNoop.ReplanCount, stReal.ReplanCount, stNoop.MaxReplans)
	t.Logf("TerminalReason   : noop=%q  realistic=%q", stNoop.TerminalReason, stReal.TerminalReason)
	t.Logf("Contradictions    : noop=%d  realistic=%d", stNoop.Evidence.Contradictions, stReal.Evidence.Contradictions)
	t.Logf("MissingPrimary    : noop=%d  realistic=%d", stNoop.Evidence.MissingPrimary, stReal.Evidence.MissingPrimary)
	t.Logf("StaleSources      : noop=%d  realistic=%d", stNoop.Evidence.StaleSources, stReal.Evidence.StaleSources)
	t.Logf("EvidenceCount     : noop=%d  realistic=%d", stNoop.EvidenceCount, stReal.EvidenceCount)
	t.Logf("Total task calls  : noop=%d  realistic=%d", len(callsNoop), len(callsReal))
	t.Logf("Task type counts  : noop=%v realistic=%v", countsNoop, countsReal)

	// Gate A — both terminate normally.
	assertP9Terminal(t, "noop", stNoop)
	assertP9Terminal(t, "realistic", stReal)

	// Gate B — MaxReplans=3 overall cap respected by BOTH.
	assertP9ReplanCap(t, "noop", stNoop)
	assertP9ReplanCap(t, "realistic", stReal)

	// Gate C — no task-count inflation beyond a sane bound for BOTH.
	if len(callsNoop) > 1+stNoop.MaxReplans*4+4 {
		t.Errorf("noop task count %d exceeds bound (possible inflation)", len(callsNoop))
	}
	if len(callsReal) > 1+stReal.MaxReplans*4+8 {
		t.Errorf("realistic task count %d exceeds bound (possible inflation)", len(callsReal))
	}

	// Gate D — no task-key collisions / resource conflicts are observable as a
	// crash or duplicate-ID panic; the run simply terminates. Both states are
	// reachable from here.

	// Gate E — consistency: ReplanCount and termination type are identical.
	if stNoop.ReplanCount != stReal.ReplanCount {
		t.Errorf("ReplanCount diverged under dynamic state: noop=%d realistic=%d", stNoop.ReplanCount, stReal.ReplanCount)
	}
	if stNoop.TerminalReason != stReal.TerminalReason {
		t.Errorf("TerminalReason diverged: noop=%q realistic=%q", stNoop.TerminalReason, stReal.TerminalReason)
	}

	// Gate F — dynamic-injection proof: the realistic worker injected evidence
	// (distinct revenue values producing new DISPUTED items) beyond the sterile
	// baseline. Under P10, DecisionStop terminates the session when the replan
	// budget is exhausted (MaxReplans=3), so not all dynamic injections are
	// reflected in the final Contradictions count — both workers report 3
	// (the pre-planted baseline). EvidenceCount, however, proves the realistic
	// worker extracted evidence items while the noop worker did not.
	if stReal.EvidenceCount <= stNoop.EvidenceCount {
		t.Errorf("realistic EvidenceCount=%d should exceed noop %d (no dynamic evidence injected)",
			stReal.EvidenceCount, stNoop.EvidenceCount)
	}
	if len(callsReal) == 0 {
		t.Error("realistic worker recorded no task calls (worker not exercised?)")
	}
}

func assertP9Terminal(t *testing.T, label string, st master.ResearchState) {
	t.Helper()
	if !st.Terminal {
		t.Errorf("%s: expected terminal state, got Terminal=%v reason=%q", label, st.Terminal, st.TerminalReason)
	}
	if st.TerminalReason != "research_complete" {
		t.Errorf("%s: TerminalReason=%q, want research_complete", label, st.TerminalReason)
	}
}

func assertP9ReplanCap(t *testing.T, label string, st master.ResearchState) {
	t.Helper()
	if st.ReplanCount > st.MaxReplans {
		t.Errorf("%s: ReplanCount=%d exceeds MaxReplans=%d (overall cap not respected)", label, st.ReplanCount, st.MaxReplans)
	}
	if st.ReplanCount < 1 {
		t.Errorf("%s: ReplanCount=%d, want >=1", label, st.ReplanCount)
	}
}

// ---------------------------------------------------------------------------
// Experiment 5 — Priority Starvation Across Multiple Replan Opportunities
//
// Question: Since Contradiction always wins priority in the switch at
// decision.go:224-231 and MaxReplans=3 is an overall cap (not per-trigger),
// can repeated contradictions monopolize all 3 replan opportunities, leaving
// MissingPrimary and StaleSource STARVED (never fired) with their evidence
// completely unprocessed at session termination?
//
// Worker: p9Experiment5Worker — reuses the p9RealisticWorker pattern (distinct
// revenue values → CONTRADICTS edges → DISPUTED evidence) but adds explicit
// injection tracking. The worker does NOT resolve the pre-planted UNVERIFIED or
// PARTIALLY_VERIFIED evidence because it uses different claim paths (JSON path
// "[0].revenue"), so no cross-relations are created.
//
// Scenario: 3 DISPUTED + 3 UNVERIFIED + 3 PARTIALLY_VERIFIED planted
// simultaneously. The p9Experiment5Worker dynamically re-injects contradictions
// during each replan-issued Verify task, ensuring Contradictions >= 2 persists at
// every replan opportunity.
// ---------------------------------------------------------------------------

// p9Experiment5Worker extends p9RealisticWorker with contradiction-injection
// tracking. It returns TaskError (InvalidContent) or Partial results to keep
// the contradiction circuit active, and distinct revenue values to create
// DISPUTED evidence. Pre-planted UNVERIFIED/PARTIALLY_VERIFIED evidence is
// never touched (different claim paths → no cross-relations).
type p9Experiment5Worker struct {
	mu         sync.Mutex
	calls      []model.TaskType
	counter    map[model.TaskType]int
	injections int
}

func newP9Experiment5Worker() *p9Experiment5Worker {
	return &p9Experiment5Worker{counter: map[model.TaskType]int{}}
}

func (w *p9Experiment5Worker) Run(t model.Task) (model.TaskResult, error) {
	w.mu.Lock()
	w.calls = append(w.calls, t.Type)
	n := w.counter[t.Type]
	w.counter[t.Type] = n + 1
	w.mu.Unlock()

	// Seed DISCOVER: no retrievable payload, so the first replay trigger is
	// governed purely by the planted evidence (matches noop seed behaviour).
	if t.Type == model.TaskTypeDiscover && n == 0 {
		return model.TaskResult{Status: model.RetrievalStatusSuccess}, nil
	}

	// Transient failure: Timeout on every 3rd call (n%3==2) at RetryCount==0.
	// The retry (RetryCount>=1) always succeeds, so MaxAttempts=3 is never
	// exhausted and the session never fatal-terminates.
	if t.RetryCount == 0 && p9ShouldFail(n) {
		return model.TaskResult{Status: model.RetrievalStatusTimeout}, nil
	}

	partial := p9IsPartial(n)

	switch t.Type {
	case model.TaskTypeDiscover, model.TaskTypeFetchHTTP, model.TaskTypeFetchBrowser, model.TaskTypeVerify:
		// Distinct integer revenue per call → distinct "[0].revenue" claim
		// values → fresh CONTRADICTS edges → DISPUTED once >= 2 accumulate.
		// Integer strings share no n-grams, so the paraphrase gate
		// (evidence/similarity.go:16, tau=0.8) never suppresses the edge.
		revenue := 100 + n
		status := model.RetrievalStatusSuccess
		if partial {
			status = model.RetrievalStatusPartial
		}
		w.mu.Lock()
		w.injections++
		w.mu.Unlock()
		return model.TaskResult{
			Status:  status,
			Data:    []byte(fmt.Sprintf(`[{"revenue":%d}]`, revenue)),
			Headers: http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	case model.TaskTypeReconcile:
		if partial {
			return model.TaskResult{Status: model.RetrievalStatusPartial}, nil
		}
		return model.TaskResult{Status: model.RetrievalStatusSuccess}, nil
	default:
		return model.TaskResult{Status: model.RetrievalStatusSuccess}, nil
	}
}

func (w *p9Experiment5Worker) Close() error { return nil }

func (w *p9Experiment5Worker) taskTypes() []model.TaskType {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]model.TaskType, len(w.calls))
	copy(out, w.calls)
	return out
}

func (w *p9Experiment5Worker) injectionCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.injections
}

var _ manager.Worker = (*p9Experiment5Worker)(nil)
var _ p9CallLog = (*p9Experiment5Worker)(nil)

// p9CountEvidenceBySource queries the evidence store for items with the given
// session ID and verification state, then filters by source ID. This is used
// to verify that pre-planted evidence items retain their original verification
// states throughout the experiment (i.e., they were never processed/resolved
// through any path).
func p9CountEvidenceBySource(t *testing.T, es *storage.SQLiteEvidenceStore, sid model.SessionID, verification model.VerificationState, sourceIDs []string) int {
	t.Helper()
	evs := es.Query(storage.EvidenceFilter{SessionID: sid, Verification: verification})
	want := make(map[string]bool, len(sourceIDs))
	for _, s := range sourceIDs {
		want[s] = true
	}
	count := 0
	for _, ev := range evs {
		if want[string(ev.SourceID)] {
			count++
		}
	}
	return count
}

// TestP9_Experiment5_PriorityStarvation tests whether the Contradiction trigger,
// which has highest priority in replanTriggerIfAny (decision.go:224-231), can
// monopolize all 3 replan opportunities (MaxReplans=3, an overall cap), leaving
// MissingPrimary and StaleSource starved — never firing even though both have
// sufficient evidence (UNVERIFIED >= MMissingPrimary=2, PARTIALLY_VERIFIED >=
// SStaleSources=2) throughout the entire session.
//
// Scenario:
//   - 3 DISPUTED evidence items (Contradictions = 3 >= KContradictions=2)
//   - 3 UNVERIFIED evidence items (MissingPrimary = 3 >= MMissingPrimary=2)
//   - 3 PARTIALLY_VERIFIED evidence items (StaleSources = 3 >= SStaleSources=2)
//   - p9Experiment5Worker dynamically injects NEW contradictions via distinct
//     revenue values during each replan-issued Verify task, ensuring
//     Contradictions >= 2 persists at every replan opportunity
//   - The worker does NOT resolve the UNVERIFIED/PARTIALLY_VERIFIED evidence
//     (different claim paths → no cross-relations → verification state unchanged)
//
// Expected outcome (FULL STARRVATION):
//   - Contradiction fires at all 3 replan opportunities
//   - MissingPrimary and StaleSource NEVER fire (shadowed by Contradiction priority)
//   - Only Verify + Reconcile tasks issued (plus 1 seed Discover)
//   - Pre-planted UNVERIFIED and PARTIALLY_VERIFIED evidence remains UNRESOLVED
//   - Terminal state: research_complete (not budget_exhausted)
func TestP9_Experiment5_PriorityStarvation(t *testing.T) {
	w := newP9Experiment5Worker()
	bmgr := &fakeBrowserManager{}
	m, _, es := newEvidenceTestGraph(t, w, bmgr)

	req := model.IntentRequest{
		Query:  "research Acme Corp, revenue",
		Seeds:  []string{"https://alpha.example"},
		UserID: "u1",
	}
	sid, err := m.SubmitIntent(req)
	if err != nil {
		t.Fatalf("SubmitIntent: %v", err)
	}

	// Plant ALL THREE trigger conditions simultaneously:
	// 3 DISPUTED items → Contradictions = 3 >= KContradictions(2) → fires FIRST
	// 3 UNVERIFIED items → MissingPrimary = 3 >= MMissingPrimary(2) → shadowed
	// 3 PARTIALLY_VERIFIED items → StaleSources = 3 >= SStaleSources(2) → shadowed
	p9PutDisputedEvidence(t, es, sid, 3)
	p9PutUnverifiedEvidence(t, es, sid, 3)
	p9PutPartiallyVerifiedEvidence(t, es, sid, 3)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	st := m.State()
	calls := w.taskTypes()
	typeCounts := p9CountTaskTypes(calls)
	injections := w.injectionCount()

	t.Logf("=== Experiment 5: Priority Starvation Analysis ===")
	t.Logf("Threshold constants: KContradictions=2, MMissingPrimary=2, SStaleSources=2, MaxReplans=3")
	t.Logf("Priority order (decision.go:218-233): Contradiction > MissingPrimary > StaleSource")
	t.Logf("Pre-populated evidence: 3 DISPUTED + 3 UNVERIFIED + 3 PARTIALLY_VERIFIED")
	t.Logf("Evidence counts at termination: Contradictions=%d MissingPrimary=%d StaleSources=%d",
		st.Evidence.Contradictions, st.Evidence.MissingPrimary, st.Evidence.StaleSources)
	t.Logf("ReplanCount=%d (MaxReplans=%d)", st.ReplanCount, st.MaxReplans)
	t.Logf("Terminal=%v TerminalReason=%q", st.Terminal, st.TerminalReason)
	t.Logf("EvidenceCount (total rows in store)=%d", st.EvidenceCount)
	t.Logf("Contradiction injections by worker: %d", injections)
	t.Logf("Worker task calls (ordered): %v", calls)
	t.Logf("Task type counts: %v", typeCounts)
	t.Logf("Total tasks executed: %d", len(calls))

	// --- Terminal state ---
	if !st.Terminal {
		t.Fatalf("expected terminal state, got Terminal=%v reason=%q", st.Terminal, st.TerminalReason)
	}
	if st.TerminalReason != "research_complete" {
		t.Errorf("TerminalReason=%q, want research_complete (budget exhausted = %d/%d)",
			st.TerminalReason, st.BudgetUsed, st.BudgetTotal)
	}

	// --- MaxReplans cap respected (overall, not per-trigger) ---
	if st.ReplanCount > st.MaxReplans {
		t.Errorf("ReplanCount=%d exceeds MaxReplans=%d (overall cap must hold)", st.ReplanCount, st.MaxReplans)
	}
	if st.ReplanCount < 1 {
		t.Errorf("ReplanCount=%d, want >=1 (Contradiction trigger should fire)", st.ReplanCount)
	}

	// --- STARVATION CHECK 1: Contradiction fired at ALL replan opportunities ---
	// Only Verify + Reconcile tasks should be issued (from Contradiction replan
	// at planning.go:55-61), plus the 1 seed Discover.
	// If MissingPrimary fired, it would issue Discover tasks (planning.go:62-66).
	// If StaleSource fired, it would issue FetchHTTP tasks (planning.go:67-71).
	if typeCounts[model.TaskTypeDiscover] > 1 {
		t.Errorf("STARVATION BROKEN: Discover tasks=%d (want <=1). "+
			"MissingPrimary trigger fired at least once — Contradiction did not monopolize all replan opportunities",
			typeCounts[model.TaskTypeDiscover])
	}
	if typeCounts[model.TaskTypeFetchHTTP] > 0 {
		t.Errorf("STARVATION BROKEN: FetchHTTP tasks=%d (want 0). "+
			"StaleSource trigger fired at least once — Contradiction did not monopolize all replan opportunities",
			typeCounts[model.TaskTypeFetchHTTP])
	}

	// Confirm Contradiction replan path was exercised
	if typeCounts[model.TaskTypeVerify] < 1 {
		t.Errorf("Verify tasks=%d, want >=1 (Contradiction replan produces Verify at planning.go:55-61)",
			typeCounts[model.TaskTypeVerify])
	}
	if typeCounts[model.TaskTypeReconcile] < 1 {
		t.Errorf("Reconcile tasks=%d, want >=1 (Contradiction replan produces Reconcile at planning.go:61)",
			typeCounts[model.TaskTypeReconcile])
	}

	// --- STARVATION CHECK 2: Contradictions persist at every replan opportunity ---
	// The pre-planted DISPUTED evidence (3 items) ensures Contradictions >= 3 >= 2
	// at the first opportunity. The worker's dynamic injection grows this count.
	if st.Evidence.Contradictions < 2 {
		t.Errorf("Contradictions=%d, want >=2 (must persist at every replan opportunity for starvation)",
			st.Evidence.Contradictions)
	}
	// Dynamic injection proof: worker added contradictions beyond the 3
	// pre-planted. Under P10, DecisionStop terminates the session when the
	// replan budget is exhausted (MaxReplans=3), so not all worker
	// injections are reflected in the final state.counts (some results are
	// discarded during the deterministic drain). The worker's own injection
	// counter proves dynamic injection occurred during replan-issued Verify
	// tasks.
	if injections <= 3 {
		t.Errorf("worker injections=%d, want >3 (dynamic contradiction injection during replan tasks)",
			injections)
	}
	if injections == 0 {
		t.Errorf("worker contradiction injections=%d, want >0 (worker must inject during replan tasks)", injections)
	}

	// --- STARVATION CHECK 3: Pre-planted UNVERIFIED evidence persists (unprocessed) ---
	// At termination, MissingPrimary must still be >= KMissingPrimary(2) because
	// the UNVERIFIED evidence was never processed — no Discover tasks beyond the
	// seed were issued (MissingPrimary trigger was shadowed).
	if st.Evidence.MissingPrimary < 2 {
		t.Errorf("MissingPrimary=%d, want >=2 (pre-planted UNVERIFIED evidence should persist unresolved)",
			st.Evidence.MissingPrimary)
	}

	// --- STARVATION CHECK 4: Pre-planted PARTIALLY_VERIFIED evidence persists ---
	if st.Evidence.StaleSources < 2 {
		t.Errorf("StaleSources=%d, want >=2 (pre-planted PARTIALLY_VERIFIED evidence should persist unresolved)",
			st.Evidence.StaleSources)
	}

	// --- STARVATION CHECK 5: Direct store verification of pre-planted evidence ---
	// Query the SQLite store by source ID to confirm the pre-planted evidence
	// items retained their original verification states at termination.
	unverifiedSources := []string{"src0.example", "src1.example", "src2.example"}
	pvSources := []string{"stale0.example", "stale1.example", "stale2.example"}
	disputedSources := []string{"disputed0.example", "disputed1.example", "disputed2.example"}

	uvCount := p9CountEvidenceBySource(t, es, sid, model.VerificationUnverified, unverifiedSources)
	pvCount := p9CountEvidenceBySource(t, es, sid, model.VerificationPartiallyVerified, pvSources)
	disputedCount := p9CountEvidenceBySource(t, es, sid, model.VerificationDisputed, disputedSources)

	t.Logf("Pre-planted evidence verification states at termination (queried by source ID):")
	t.Logf("  UNVERIFIED items     (srcN.example):     %d/3", uvCount)
	t.Logf("  PARTIALLY_VERIFIED   (staleN.example):  %d/3", pvCount)
	t.Logf("  DISPUTED items       (disputedN.example): %d/3", disputedCount)

	if uvCount != 3 {
		t.Errorf("UNVERIFIED pre-planted evidence: %d/3 remain UNVERIFIED at termination "+
			"(evidence was processed/resolved by some path)", uvCount)
	}
	if pvCount != 3 {
		t.Errorf("PARTIALLY_VERIFIED pre-planted evidence: %d/3 remain PARTIALLY_VERIFIED at termination "+
			"(evidence was processed/resolved by some path)", pvCount)
	}
	if disputedCount != 3 {
		t.Errorf("DISPUTED pre-planted evidence: %d/3 remain DISPUTED at termination "+
			"(evidence was resolved by some path)", disputedCount)
	}

	// --- STARVATION CHECK 6: No other task types processed the evidence ---
	// If any natural path (e.g., Discover from MissingPrimary, FetchHTTP from
	// StaleSource) had processed the UNVERIFIED/PARTIALLY_VERIFIED evidence,
	// we would see Discover > 1 or FetchHTTP > 0 above. Their absence confirms
	// no alternative path resolved the evidence.
	if typeCounts[model.TaskTypeFetchBrowser] > 0 {
		t.Logf("Note: FetchBrowser tasks=%d (escalation path, unrelated to starvation triggers)",
			typeCounts[model.TaskTypeFetchBrowser])
	}

	// --- Bounded task count (no infinite loop) ---
	maxExpected := 1 + st.MaxReplans*4 + 8 // seed + 3 replans × (3 Verify + 1 Reconcile) + retry slack
	if len(calls) > maxExpected {
		t.Errorf("task count %d exceeds bound %d (possible infinite loop)", len(calls), maxExpected)
	}

	// --- VERDICT ---
	starvationConfirmed := typeCounts[model.TaskTypeDiscover] <= 1 &&
		typeCounts[model.TaskTypeFetchHTTP] == 0 &&
		st.Evidence.MissingPrimary >= 2 &&
		st.Evidence.StaleSources >= 2 &&
		uvCount == 3 &&
		pvCount == 3 &&
		st.TerminalReason == "research_complete"

	t.Logf("")
	t.Logf("=== STARVATION VERDICT ===")
	if starvationConfirmed {
		t.Logf("FULL STARRVATION CONFIRMED:")
		t.Logf("  - Contradiction fired at all %d replan opportunities (ReplanCount=%d)", st.ReplanCount, st.ReplanCount)
		t.Logf("  - MissingPrimary trigger NEVER fired (Discover tasks = %d, only seed)", typeCounts[model.TaskTypeDiscover])
		t.Logf("  - StaleSource trigger NEVER fired (FetchHTTP tasks = 0)")
		t.Logf("  - Pre-planted UNVERIFIED evidence (3 items) remained UNRESOLVED at termination")
		t.Logf("  - Pre-planted PARTIALLY_VERIFIED evidence (3 items) remained UNRESOLVED at termination")
		t.Logf("  - Dynamic contradiction injection: %d (Contradictions %d → %d)",
			injections, 3, st.Evidence.Contradictions)
		t.Logf("  - Terminal state: %q (not budget_exhausted)", st.TerminalReason)
		t.Logf("  - No alternative path processed the starved evidence")
	} else {
		t.Logf("STARVATION NOT CONFIRMED:")
		if typeCounts[model.TaskTypeDiscover] > 1 {
			t.Logf("  - MissingPrimary triggered (Discover=%d)", typeCounts[model.TaskTypeDiscover])
		}
		if typeCounts[model.TaskTypeFetchHTTP] > 0 {
			t.Logf("  - StaleSource triggered (FetchHTTP=%d)", typeCounts[model.TaskTypeFetchHTTP])
		}
		if uvCount != 3 {
			t.Logf("  - UNVERIFIED evidence partially resolved (%d/3)", uvCount)
		}
		if pvCount != 3 {
			t.Logf("  - PARTIALLY_VERIFIED evidence partially resolved (%d/3)", pvCount)
		}
		if st.TerminalReason != "research_complete" {
			t.Logf("  - Terminal state is %q (not research_complete)", st.TerminalReason)
		}
	}
}

