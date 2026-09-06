package master

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"draw/internal/config"
	"draw/internal/manager"
	"draw/internal/model"
)

// stuckManager simulates a manager whose Execute blocks indefinitely for a
// designated task until cancel() is invoked. This models a genuinely stuck
// worker — a hard hang inside Execute that never produces a result and never
// releases its slot — i.e. an actual task-manager failure rather than an
// ordinary retryable delay. For any other task it completes immediately.
type stuckManager struct {
	mu       sync.Mutex
	calls    int
	cancelCh chan struct{}
	stuckID  model.TaskID
}

func newStuckManager(stuckID model.TaskID) *stuckManager {
	return &stuckManager{
		cancelCh: make(chan struct{}),
		stuckID:  stuckID,
	}
}

func (m *stuckManager) Capabilities() []manager.Capability { return nil }

func (m *stuckManager) Execute(t model.Task) (model.TaskResult, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()

	if t.ID == m.stuckID {
		select {
		case <-m.cancelCh:
			return model.TaskResult{
				Status: model.RetrievalStatusInvalidContent,
				Error:  context.Canceled,
			}, context.Canceled
		}
	}
	return model.TaskResult{
		Status:   model.RetrievalStatusSuccess,
		Evidence: []model.EvidenceID{"e1"},
	}, nil
}

func (m *stuckManager) cancel() {
	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case <-m.cancelCh:
	default:
		close(m.cancelCh)
	}
}

func (m *stuckManager) getCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// cancelableScheduler wraps fakeScheduler with TaskCanceller support. When
// CancelTask is invoked it zeroes the scheduler's in-flight Active count and
// signals the stuck manager to abort its blocked Execute call — simulating a
// real task manager that, once told to cancel, releases its worker slot.
type cancelableScheduler struct {
	*fakeScheduler
	stuckMgr    *stuckManager
	cancelCount int32
}

func (s *cancelableScheduler) CancelTask(id model.TaskID) {
	atomic.AddInt32(&s.cancelCount, 1)
	s.fakeScheduler.mu.Lock()
	s.fakeScheduler.running = 0
	s.fakeScheduler.mu.Unlock()
	s.stuckMgr.cancel()
}

func (s *cancelableScheduler) getCancelCount() int32 {
	return atomic.LoadInt32(&s.cancelCount)
}

// stopDecider always returns DecisionStop, simulating a deterministic stop
// condition (e.g. a replan trigger whose budget is exhausted while a task is
// still in flight).
type stopDecider struct{}

func (stopDecider) Decide(DecisionInput) Decision {
	return Decision{Action: DecisionStop, Reason: "test: forced stop"}
}

// TestEdgeCase_DrainedTimeout_StuckTask verifies edge case 2: drained() cannot
// complete because a task is genuinely stuck — its manager's Execute blocks
// indefinitely, never releasing its slot (Active stays > 0, the task remains in
// m.pending). The drain timeout (~5s) must fire and Cancel() must be issued to
// the remaining workers so Master.Run returns without hanging; afterward state
// is purged and a new session starts from a clean, deterministic state.
func TestEdgeCase_DrainedTimeout_StuckTask(t *testing.T) {
	const stuckTaskID = "tsk_stuck"

	stuck := newStuckManager(model.TaskID(stuckTaskID))
	fs := newFakeScheduler(stuck, 2)
	sched := &cancelableScheduler{
		fakeScheduler: fs,
		stuckMgr:      stuck,
	}

	cfg := config.Defaults()
	m := NewMaster(cfg, sched)
	m.evidence = &countingEvidence{}
	m.decider = stopDecider{}
	m.drainTimeout = 5 * time.Second

	session := baseSession()
	plan := basePlan()

	taskA := model.Task{
		ID:            model.TaskID(stuckTaskID),
		SessionID:     session.ID,
		Type:          model.TaskTypeDiscover,
		State:         model.TaskStateReady,
		Priority:      1000,
		SourceTarget:  "stuck.example.com",
		URL:           mustURL("https://stuck.example.com"),
		EstimatedCost: 1,
		TaskKey:       "test:stuck",
		CreatedAt:     time.Now().UTC(),
	}
	taskB := model.Task{
		ID:            "tsk_complete",
		SessionID:     session.ID,
		Type:          model.TaskTypeDiscover,
		State:         model.TaskStateReady,
		Priority:      500,
		SourceTarget:  "ok.example.com",
		URL:           mustURL("https://ok.example.com"),
		EstimatedCost: 1,
		TaskKey:       "test:complete",
		CreatedAt:     time.Now().UTC(),
	}

	// Seed both tasks directly into the scheduler queue and the master's
	// bookkeeping (bypassing SubmitIntent so the scenario is fully controlled).
	_ = sched.Submit(taskA)
	_ = sched.Submit(taskB)

	m.mu.Lock()
	m.state = NewResearchState(session, plan, cfg)
	m.tasks[taskA.ID] = taskA
	m.pending[taskA.ID] = true
	m.tasks[taskB.ID] = taskB
	m.pending[taskB.ID] = true
	m.mu.Unlock()

	runStart := time.Now()
	runDone := make(chan error, 1)
	go func() { runDone <- m.Run(context.Background()) }()

	var runErr error
	select {
	case runErr = <-runDone:
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return within 20s; stuck task caused infinite hang")
	}
	elapsed := time.Since(runStart)

	if runErr != nil {
		t.Fatalf("Run returned error: %v", runErr)
	}

	// (a) Timeout fires at ~5s (not sooner, not much later) and Run returns.
	if elapsed < 4*time.Second || elapsed > 7*time.Second {
		t.Errorf("drain timeout elapsed: got %v, want ~5s (4-7s range)", elapsed)
	}

	// (b) Cancel() was issued to the remaining (stuck) worker via TaskCanceller.
	if sched.getCancelCount() == 0 {
		t.Error("expected CancelTask to be invoked on the scheduler (stuck worker cancellation)")
	}

	// (c) State terminated with the stuck-worker reason set by Cancel.
	st := m.State()
	if st.TerminalReason != "cancelled_stuck_workers" {
		t.Errorf("terminal reason: got %q, want %q", st.TerminalReason, "cancelled_stuck_workers")
	}

	// (d) m.pending and m.tasks are purged after the timeout.
	m.mu.Lock()
	pendingLen := len(m.pending)
	tasksLen := len(m.tasks)
	stopPending := m.StopPending
	m.mu.Unlock()
	if pendingLen != 0 {
		t.Errorf("m.pending should be purged after Cancel: got %d entries", pendingLen)
	}
	if tasksLen != 0 {
		t.Errorf("m.tasks should be purged after Cancel: got %d entries", tasksLen)
	}
	if stopPending {
		t.Error("StopPending should be cleared after Cancel")
	}

	// (e) The genuinely stuck manager was unblocked by Cancel's CancelTask.
	if got := stuck.getCalls(); got < 2 {
		t.Errorf("expected >=2 manager Execute calls (stuck + complete), got %d", got)
	}

	// Allow the unblocked stuck goroutine to fully settle before the next phase.
	time.Sleep(100 * time.Millisecond)

	// (f) Macro determinism: a new session starts from a clean state with no
	// leakage from the cancelled session — no residual pending/tasks, no
	// StopPending/cancelled flags, a fresh session id, and a non-terminal state.
	sid, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u2",
		Query:  "research Company X",
		Seeds:  []string{"https://seed2.example.com"},
	})
	if err != nil {
		t.Fatalf("SubmitIntent for new session: %v", err)
	}

	m.mu.Lock()
	newSessionID := m.state.Session.ID
	newPending := len(m.pending)
	newTasks := len(m.tasks)
	cancelledFlag := m.cancelled
	stopPendingFlag := m.StopPending
	terminal := m.state.Terminal
	budget := m.state.BudgetTotal
	m.mu.Unlock()

	if stopPendingFlag {
		t.Error("StopPending should be false for the new session")
	}
	if cancelledFlag {
		t.Error("cancelled should be false for the new session")
	}
	if terminal {
		t.Error("new session state should not be terminal")
	}
	if newSessionID != sid {
		t.Errorf("session id mismatch: got %s, want %s", newSessionID, sid)
	}
	if newPending != 1 {
		t.Errorf("expected 1 pending task for new session, got %d", newPending)
	}
	if newTasks != 1 {
		t.Errorf("expected 1 task for new session, got %d", newTasks)
	}
	if budget == 0 {
		t.Error("new session budget should be initialized")
	}

	t.Logf("PASS: elapsed=%v cancelTasks=%d managerCalls=%d newPending=%d newTasks=%d",
		elapsed, sched.getCancelCount(), stuck.getCalls(), newPending, newTasks)
}
