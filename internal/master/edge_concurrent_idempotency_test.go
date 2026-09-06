package master

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"draw/internal/config"
	"draw/internal/model"
)

// TestDecisionStopFromDecider verifies the Decider returns DecisionStop
// when a replan trigger is detected but the replan budget is exhausted.
// This confirms the evidence setup in the concurrency test below would
// produce DecisionStop through the normal observeAndDecide -> Decide path.
func TestDecisionStopFromDecider(t *testing.T) {
	st := stateWithBudget(500, EvidenceCounts{Contradictions: 5}) // >= KContradictions (2)
	st.ReplanCount = st.MaxReplans                                // exhausted replan budget
	in := decisionInput(
		task(model.TaskTypeFetchHTTP, 0, model.ErrorWeightLow, 1),
		model.TaskResult{Status: model.RetrievalStatusSuccess},
		st,
		statsWith(0, 4),
	)
	d := testDecider().Decide(in)
	if d.Action != DecisionStop {
		t.Errorf("replan trigger + exhausted budget: got %s want STOP", d.Action)
	}
	if d.Reason == "" {
		t.Error("DecisionStop must carry a non-empty reason")
	}
}

// TestEdgeCase_ConcurrentDecisionStop verifies that when N goroutines
// simultaneously call applyDecision with DecisionStop at the exact same
// instant, the StopPending idempotency guard ensures:
//   - StopPending is set exactly ONCE (no double mutation)
//   - SetTerminal is called exactly ONCE
//   - no panic, no race on the StopPending field
//
// The test runs N>=5 iterations, each spawning 50 goroutines behind a
// start-line barrier so they race to enter applyDecision simultaneously.
func TestEdgeCase_ConcurrentDecisionStop(t *testing.T) {
	const N = 50
	const iterations = 5

	for iter := 0; iter < iterations; iter++ {
		t.Run(fmt.Sprintf("iter_%d", iter), func(t *testing.T) {
			// --- Set up a Master with a session and evidence that triggers DecisionStop ---
			sched := newFakeScheduler(&fakeManager{}, 4)
			m := NewMaster(config.Defaults(), sched)
			m.evidence = &countingEvidence{}

			session := baseSession()
			plan := basePlan()

			m.mu.Lock()
			st := NewResearchState(session, plan, config.Defaults())
			m.state = st
			// Evidence that triggers DecisionStop: replan trigger (contradictions)
			// + replan budget exhausted.
			st.ReplanCount = st.MaxReplans
			st.Evidence = EvidenceCounts{Contradictions: 5}
			// Seed a pending task that the handler will complete.
			tk := model.Task{
				ID:            model.NewTaskID(),
				SessionID:     session.ID,
				Type:          model.TaskTypeFetchHTTP,
				State:         model.TaskStateRunning,
				Priority:      10,
				EstimatedCost: 1,
				ErrorWeight:   model.ErrorWeightLow,
			}
			m.pending[tk.ID] = true
			m.tasks[tk.ID] = tk
			m.mu.Unlock()

			// --- Verify the Decider produces DecisionStop for this evidence ---
			m.mu.Lock()
			stateClone := m.state.Clone()
			m.mu.Unlock()
			in := DecisionInput{
				Task:    tk,
				Result:  model.TaskResult{Status: model.RetrievalStatusSuccess},
				State:   stateClone,
				Retry:   config.DefaultRetry(),
				Upgrade: config.DefaultUpgrade(),
				Replan:  config.DefaultReplan(),
				Stats:   statsWith(0, 4),
			}
			dec := m.decider.Decide(in)
			if dec.Action != DecisionStop {
				t.Fatalf("expected Decider to return DecisionStop for this evidence, got %s (reason: %s)",
					dec.Action, dec.Reason)
			}

			// --- Concurrency stress: 50 goroutines hit applyDecision simultaneously ---
			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(N)

			var panicCount int32
			var returnedTrue int32

			for i := 0; i < N; i++ {
				go func() {
					defer wg.Done()
					defer func() {
						if r := recover(); r != nil {
							atomic.AddInt32(&panicCount, 1)
						}
					}()
					<-start // start-line barrier: all goroutines wait here
					result := m.applyDecision(
						context.Background(),
						tk,
						model.TaskResult{Status: model.RetrievalStatusSuccess},
						dec,
					)
					if result {
						atomic.AddInt32(&returnedTrue, 1)
					}
				}()
			}

			close(start) // release all goroutines at the exact same instant
			wg.Wait()

			// --- Verify: no panic, no race ---
			if panicCount > 0 {
				t.Fatalf("panic occurred in %d goroutines", panicCount)
			}

			// --- Verify: all N goroutines returned true ---
			// The first goroutine to acquire the lock sets StopPending and
			// returns false (signalling "stop pending — let in-flight work
			// drain"). Subsequent goroutines observe StopPending=true and
			// return true immediately. All return true (idempotent).
			if got := atomic.LoadInt32(&returnedTrue); got != N {
				t.Errorf("expected %d goroutines to return true, got %d", N, got)
			}

			// --- Verify: StopPending is set (exactly once) ---
			m.mu.Lock()
			sp := m.StopPending
			terminal := m.state.Terminal
			terminalReason := m.state.TerminalReason
			terminalCount := m.state.setTerminalCount
			m.mu.Unlock()

			if !sp {
				t.Error("StopPending should be true after concurrent DecisionStop")
			}

			if !terminal {
				t.Error("state.Terminal should be true after concurrent DecisionStop")
			}

			if terminalReason == "" {
				t.Error("TerminalReason should not be empty")
			}

			// --- Verify: only ONE SetTerminal call occurred ---
			if terminalCount != 1 {
				t.Errorf("expected exactly 1 SetTerminal call, got %d "+
					"(StopPending idempotency guard failed: multiple goroutines passed the guard)",
					terminalCount)
			}
		})
	}
}

// TestEdgeCase_DecisionStopIdempotentSequential verifies the idempotency
// guard in a single-threaded scenario: calling applyDecision with
// DecisionStop twice should only invoke SetTerminal once.
func TestEdgeCase_DecisionStopIdempotentSequential(t *testing.T) {
	sched := newFakeScheduler(&fakeManager{}, 4)
	m := NewMaster(config.Defaults(), sched)
	m.evidence = &countingEvidence{}

	session := baseSession()
	plan := basePlan()

	m.mu.Lock()
	st := NewResearchState(session, plan, config.Defaults())
	m.state = st
	tk := model.Task{
		ID:            model.NewTaskID(),
		SessionID:     session.ID,
		Type:          model.TaskTypeFetchHTTP,
		State:         model.TaskStateRunning,
		Priority:      10,
		EstimatedCost: 1,
		ErrorWeight:   model.ErrorWeightLow,
	}
	m.pending[tk.ID] = true
	m.tasks[tk.ID] = tk
	m.mu.Unlock()

	decision := Decision{Action: DecisionStop, Reason: "sequential idempotency test"}

	for i := 0; i < 3; i++ {
		m.applyDecision(context.Background(), tk, model.TaskResult{}, decision)
	}

	m.mu.Lock()
	sp := m.StopPending
	terminalCount := m.state.setTerminalCount
	m.mu.Unlock()

	if !sp {
		t.Error("StopPending should be true after DecisionStop")
	}
	if terminalCount != 1 {
		t.Errorf("expected 1 SetTerminal call after 3 sequential DecisionStop calls, got %d", terminalCount)
	}
}

// TestEdgeCase_DecisionTerminateStillSetsTerminal verifies that
// DecisionTerminate is NOT blocked by the StopPending guard — it should
// always set terminal state even after a DecisionStop has occurred.
func TestEdgeCase_DecisionTerminateNotBlockedByStopPending(t *testing.T) {
	sched := newFakeScheduler(&fakeManager{}, 4)
	m := NewMaster(config.Defaults(), sched)
	m.evidence = &countingEvidence{}

	session := baseSession()
	plan := basePlan()

	m.mu.Lock()
	st := NewResearchState(session, plan, config.Defaults())
	m.state = st
	tk := model.Task{
		ID:            model.NewTaskID(),
		SessionID:     session.ID,
		Type:          model.TaskTypeFetchHTTP,
		State:         model.TaskStateRunning,
		Priority:      10,
		EstimatedCost: 1,
		ErrorWeight:   model.ErrorWeightLow,
	}
	m.pending[tk.ID] = true
	m.tasks[tk.ID] = tk
	m.mu.Unlock()

	// First: DecisionStop sets the guard
	stopDec := Decision{Action: DecisionStop, Reason: "stop"}
	m.applyDecision(context.Background(), tk, model.TaskResult{}, stopDec)

	m.mu.Lock()
	stopCount := m.state.setTerminalCount
	m.mu.Unlock()
	if stopCount != 1 {
		t.Fatalf("after DecisionStop: expected 1 SetTerminal call, got %d", stopCount)
	}

	// Second: DecisionTerminate should NOT be blocked by StopPending
	termDec := Decision{Action: DecisionTerminate, Reason: "terminate"}
	m.applyDecision(context.Background(), tk, model.TaskResult{}, termDec)

	m.mu.Lock()
	finalCount := m.state.setTerminalCount
	terminal := m.state.Terminal
	m.mu.Unlock()
	if finalCount != 2 {
		t.Errorf("after DecisionTerminate following DecisionStop: expected 2 SetTerminal calls, got %d "+
			"(DecisionTerminate must not be blocked by StopPending guard)", finalCount)
	}
	if !terminal {
		t.Error("state should remain terminal after DecisionTerminate")
	}
}
