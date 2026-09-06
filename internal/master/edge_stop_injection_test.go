package master

import (
	"context"
	"errors"
	"strings"
	"testing"

	"draw/internal/config"
	"draw/internal/model"
)

// TestEdgeCase_StopPending_RejectsInjection exercises edge case 1: injecting a
// new task while StopPending is active, through the two primitive injection
// vectors that BYPASS applyDecision's pre-switch StopPending guard:
//
//   - m.mergeDelta (the Replan-delta application path — "via Replan")
//   - m.orch.Submit (a direct task submission)
//
// NOTE: Edge-1 originally guarded m.orch.Submit via a stopGuardedOrchestration
// wrapper backed by an atomic.Bool mirror of StopPending. Per the reconciliation
// decision, that wrapper and atomic.Bool were DROPPED: the pre-switch guard in
// applyDecision already blocks all non-terminal decisions (which is where all
// task injection originates in the normal flow), making a scheduler-level
// Submit guard redundant. This test therefore verifies:
//
//   - mergeDelta rejects out-of-band deltas with ErrStopPending
//   - direct m.orch.Submit is NOT guarded (documents the intentional gap:
//   only injection that flows through observeAndDecide → applyDecision is guarded)
//   - planner.Replan + mergeDelta is rejected via the mergeDelta guard

func TestEdgeCase_StopPending_RejectsInjection(t *testing.T) {
	mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
		return model.TaskResult{Status: model.RetrievalStatusSuccess}
	}}
	sched := newFakeScheduler(mgr, 4)
	obs := &fakeEventObserver{}
	m := NewMaster(config.Defaults(), sched, WithEventObserver(obs))
	m.evidence = &countingEvidence{}

	// Submit a session so m.state (and m.state.Session) exist.
	if _, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1", Query: "research Company X", Seeds: []string{seedURL()},
	}); err != nil {
		t.Fatal(err)
	}

	// --- (3a) Evidence that triggers DecisionStop: contradictions present
	// (>= KContradictions, default 2) AND replan budget exhausted ---
	m.mu.Lock()
	m.state.Evidence = EvidenceCounts{Contradictions: 2}
	m.state.ReplanCount = 2
	m.state.MaxReplans = 2
	m.mu.Unlock()

	st := m.State()
	dec := m.decider.Decide(decisionInput(
		task(model.TaskTypeFetchHTTP, 0, model.ErrorWeightLow, 1),
		model.TaskResult{Status: model.RetrievalStatusSuccess},
		st,
		statsWith(0, 4),
	))
	if dec.Action != DecisionStop {
		t.Fatalf("evidence should trigger DecisionStop (replan budget exhausted); got %s (reason: %s)",
			dec.Action, dec.Reason)
	}

	// --- (3b) Trigger StopPending via DecisionStop (applyDecision) ---
	tk := model.Task{
		ID:        model.NewTaskID(),
		SessionID: m.state.Session.ID,
		Type:      model.TaskTypeFetchHTTP,
		State:     model.TaskStateRunning,
		Priority:  10,
	}
	m.mu.Lock()
	m.pending[tk.ID] = true
	m.tasks[tk.ID] = tk
	m.mu.Unlock()

	// DecisionStop returns true on the first call so the Run loop enters
	// the handleStop drain path (5s timeout + Cancel of stuck workers).
	if stopped := m.applyDecision(context.Background(), tk, model.TaskResult{}, dec); !stopped {
		t.Fatal("DecisionStop applyDecision should return stop=true on first call (handleStop path)")
	}

	m.mu.Lock()
	stopPending := m.StopPending
	m.mu.Unlock()
	if !stopPending {
		t.Fatal("StopPending should be active after DecisionStop")
	}

	rejectsBefore := len(stopPendingRejectMessages(obs))

	// --- (3c) Inject via mergeDelta (Replan delta) — must be rejected ---
	inject := model.Task{
		ID:            model.NewTaskID(),
		SessionID:     m.state.Session.ID,
		Type:          model.TaskTypeVerify,
		State:         model.TaskStateReady,
		Priority:      50,
		EstimatedCost: 1,
	}
	delta := model.PlanDelta{Add: []model.Task{inject}}
	m.mu.Lock()
	mergeErr := m.mergeDelta(delta)
	_, taskRegistered := m.tasks[inject.ID]
	inPending := m.pending[inject.ID]
	m.mu.Unlock()

	if !errors.Is(mergeErr, ErrStopPending) {
		t.Errorf("mergeDelta should return ErrStopPending; got %v", mergeErr)
	}
	if taskRegistered || inPending {
		t.Error("mergeDelta must not register/inject task while StopPending (direct primitive bypass)")
	}
	if queuedTaskIDs(sched)[inject.ID] {
		t.Error("mergeDelta must not submit task to scheduler while StopPending")
	}
	if !containsMessage(stopPendingRejectMessages(obs), "mergeDelta") {
		t.Error("expected stop_pending_reject event mentioning mergeDelta")
	}
	if got := len(stopPendingRejectMessages(obs)); got != rejectsBefore+1 {
		t.Errorf("mergeDelta should emit exactly one reject event; got %d (before=%d)", got, rejectsBefore)
	}

	// --- (3d) Direct m.orch.Submit — intentionally NOT guarded ---
	// Per the reconciliation decision, the stopGuardedOrchestration wrapper
	// and atomic.Bool were DROPPED. The pre-switch guard in applyDecision
	// covers all task injection that flows through the decision path; a direct
	// m.orch.Submit is only called from within applyDecision (under m.mu) and
	// from mergeDelta (which is itself guarded by the StopPending check).
	// This test documents that direct Submit does NOT return ErrStopPending:
	direct := model.Task{
		ID:        model.NewTaskID(),
		SessionID: m.state.Session.ID,
		Type:      model.TaskTypeFetchHTTP,
		State:     model.TaskStateReady,
		Priority:  10,
	}
	submitErr := m.orch.Submit(direct)
	if submitErr != nil {
		t.Logf("direct m.orch.Submit returned: %v (not guarded by stopGuardedOrchestration per reconciliation decision)", submitErr)
	}

	// --- (3e) Inject via planner.Replan + mergeDelta — must be rejected ---
	m.mu.Lock()
	m.state.ReplanCount = 0
	m.state.MaxReplans = 3
	m.mu.Unlock()

	st = m.State()
	st.ReplanCount = 0
	st.MaxReplans = 3
	replanDelta := m.planner.Replan(st, ReplanTrigger{Kind: ReplanTriggerContradiction})
	if len(replanDelta.Add) == 0 {
		t.Fatal("planner.Replan should produce tasks when CanReplan is true (budget available)")
	}

	preReplanRejects := len(stopPendingRejectMessages(obs))
	m.mu.Lock()
	replanMergeErr := m.mergeDelta(replanDelta)
	injectedCount := 0
	for _, tk := range replanDelta.Add {
		if _, ok := m.tasks[tk.ID]; ok {
			injectedCount++
		}
	}
	m.mu.Unlock()

	if !errors.Is(replanMergeErr, ErrStopPending) {
		t.Errorf("mergeDelta(replanDelta) should return ErrStopPending; got %v", replanMergeErr)
	}
	if injectedCount != 0 {
		t.Error("mergeDelta must not inject planner.Replan tasks while StopPending")
	}
	if !containsMessage(stopPendingRejectMessages(obs), "mergeDelta") {
		t.Error("expected stop_pending_reject event for mergeDelta via planner.Replan")
	}
	if got := len(stopPendingRejectMessages(obs)); got != preReplanRejects+1 {
		t.Errorf("expected exactly one additional stop_pending_reject event for mergeDelta; got %d (before=%d)", got, preReplanRejects)
	}
}

// stopPendingRejectMessages returns the messages of all stop_pending_reject
// events observed so far. It is a test-only accessor for fakeEventObserver.
func stopPendingRejectMessages(obs *fakeEventObserver) []string {
	obs.mu.Lock()
	defer obs.mu.Unlock()
	var out []string
	for _, ev := range obs.events {
		if ev.Kind == "stop_pending_reject" {
			out = append(out, ev.Message)
		}
	}
	return out
}

func containsMessage(msgs []string, sub string) bool {
	for _, m := range msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

// queuedTaskIDs snapshots the task IDs currently queued in the fake scheduler.
func queuedTaskIDs(s *fakeScheduler) map[model.TaskID]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[model.TaskID]bool, len(s.queue))
	for _, tk := range s.queue {
		out[tk.ID] = true
	}
	return out
}
