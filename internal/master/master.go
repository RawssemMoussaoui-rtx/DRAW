package master

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"draw/internal/config"
	"draw/internal/manager"
	"draw/internal/model"
	"draw/internal/storage"
)

type Orchestration interface {
	Submit(model.Task) error
	RegisterManager(manager.Manager, []manager.Capability) error
	Admit(time.Time) (model.Task, manager.Manager, manager.WorkerSlot, bool)
	Release(manager.WorkerSlot, model.TaskID, bool)
	Stats() SchedulerStats
}

// TaskCanceller is an optional interface for schedulers that support
// cancelling in-flight tasks. The Master invokes it during Cancel to
// forcibly terminate workers that would otherwise never release their
// slots (e.g. a manager whose Execute is blocked indefinitely).
type TaskCanceller interface {
	CancelTask(taskID model.TaskID)
}

type Scheduler interface {
	Orchestration
}

// ErrStopPending is returned by Master.mergeDelta when a task-injection
// (replan delta) is attempted while the session is StopPending. Callers may
// detect it via errors.Is(err, ErrStopPending). A direct m.orch.Submit bypasses
// this guard entirely (see the note on the pre-switch guard in applyDecision).
var ErrStopPending = errors.New("master: task injection rejected: session stop pending")

// Ingestion is the Phase-E seam: a producer that consumes a TaskResult's
// retrieved content and feeds discovered URLs into the FrontierSink. The
// Master invokes it from the result-observation path; ingestion is
// best-effort and non-fatal. Default is a no-op so all pre-existing
// behavior is preserved when ingestion is unconfigured. The concrete
// implementation (internal/ingestion.Processor) holds ONLY the
// frontier.FrontierSink capability; the Master itself never calls
// Frontier.Next and never becomes the frontier consumer.
type Ingestion interface {
	Process(ctx context.Context, t model.Task, r model.TaskResult) error
}

type noopIngestion struct{}

func (noopIngestion) Process(context.Context, model.Task, model.TaskResult) error { return nil }

func noopIngestionFactory() Ingestion { return noopIngestion{} }

type Master struct {
	cfg          config.SchedulerConfig
	sessions     storage.SessionStore
	evidence     EvidenceReader
	es           storage.EvidenceStore
	reg          storage.SourceRegistry
	orch         Orchestration
	intent       IntentEngine
	planner      PlanningEngine
	decider      DecisionEngine
	ingest       Ingestion
	observer     EventObserver
	tasksPersist TaskPersistence

	mu           sync.Mutex
	state        *ResearchState
	tasks        map[model.TaskID]model.Task
	pending      map[model.TaskID]bool
	redisc       map[model.TaskID]int
	dropped      map[model.TaskID]bool
	stop         chan struct{}
	runErr       error
	StopPending  bool
	cancelled    bool
	draining     bool
	drainTimeout time.Duration
	cancelFn     context.CancelFunc
}

type MasterOption func(*Master)

func WithMemorySessions() MasterOption {
	return func(m *Master) { m.sessions = NewMemorySessionStore() }
}

// WithIngestion installs a Phase-E ingestion seam onto the Master. Ingestion
// runs best-effort from the result-observation path (observeAndDecide); a
// failing Process must never alter task/ResearchState semantics. Omit to
// retain the default no-op ingestion, preserving prior behavior.
func WithIngestion(in Ingestion) MasterOption {
	return func(m *Master) { m.ingest = in }
}

func NewMaster(cfg config.SchedulerConfig, orch Orchestration, opts ...MasterOption) *Master {
	m := &Master{
		cfg:          cfg,
		orch:         orch,
		sessions:     NewMemorySessionStore(),
		evidence:     NoopEvidenceReader(),
		intent:       NewIntentParser(DefaultIntentConfig()),
		planner:      NewPlanMaker(cfg),
		decider:      NewDecider(cfg),
		ingest:       noopIngestionFactory(),
		stop:         make(chan struct{}),
		tasks:        map[model.TaskID]model.Task{},
		pending:      map[model.TaskID]bool{},
		redisc:       map[model.TaskID]int{},
		dropped:      map[model.TaskID]bool{},
		drainTimeout: 5 * time.Second,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func (m *Master) SubmitIntent(req model.IntentRequest) (model.SessionID, error) {
	m.mu.Lock()
	sp := m.StopPending
	m.mu.Unlock()
	if sp {
		m.waitDrainedWithTimeout()
	}
	m.mu.Lock()
	m.StopPending = false
	m.cancelled = false
	m.mu.Unlock()

	intent, err := m.intent.Parse(req)
	if err != nil {
		return "", err
	}
	if err := validateSeeds(req); err != nil {
		return "", err
	}
	plan := m.planner.Build(intent)
	session := &model.Session{
		ID:        model.NewSessionID(),
		UserID:    req.UserID,
		State:     model.SessionStateActive,
		Intent:    intent,
		Plan:      plan,
		CreatedAt: time.Now().UTC(),
	}
	if err := m.sessions.Save(session); err != nil {
		return "", err
	}

	seedTasks := m.seedTasks(session, intent)

	m.mu.Lock()
	defer m.mu.Unlock()
	st := NewResearchState(session, &session.Plan, m.cfg)
	m.state = st
	m.cancelled = false
	for i := range seedTasks {
		t := seedTasks[i]
		m.tasks[t.ID] = t
		m.pending[t.ID] = true
		session.Plan.Phases[0].Tasks = append(session.Plan.Phases[0].Tasks, t.ID)
		_ = m.orch.Submit(t)
		m.persistTask(t)
	}
	m.state.Plan = &session.Plan
	_ = m.sessions.Save(session)
	return session.ID, nil
}

func validateSeeds(req model.IntentRequest) error {
	if len(req.Seeds) == 0 {
		return errors.New("master: intent request must include at least one seed URL")
	}
	return nil
}

func (m *Master) seedTasks(session *model.Session, intent model.Intent) []model.Task {
	seen := map[string]bool{}
	var out []model.Task
	cost := m.cfg.CostModel[model.TaskTypeDiscover]
	if cost == 0 {
		cost = 1
	}
	for i, s := range intent.Seeds {
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			continue
		}
		if seen[u.Host] {
			continue
		}
		seen[u.Host] = true
		tk := fmt.Sprintf("seed:%s:%d:%s", session.ID, i, u.Host)
		out = append(out, model.Task{
			ID:            model.NewTaskID(),
			SessionID:     session.ID,
			Type:          model.TaskTypeDiscover,
			State:         model.TaskStateReady,
			Priority:      1000,
			SourceClass:   model.SourceClassUnknown,
			SourceTarget:  u.Host,
			URL:           u,
			CrawlDepth:    0,
			TaskDepth:     0,
			EstimatedCost: cost,
			TaskKey:       tk,
			CreatedAt:     time.Now().UTC(),
		})
	}
	return out
}

// taskResult carries a completed task's outcome from an executeAsync worker
// goroutine back to the Run loop. The Run goroutine is the sole owner of
// state mutation (observeAndDecide), so workers never touch m.state.
type taskResult struct {
	task   model.Task
	mgr    manager.Manager
	result model.TaskResult
	err    error
}

func (m *Master) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.cancelFn = cancel
	m.cancelled = false
	m.StopPending = false
	m.mu.Unlock()
	defer cancel()

	m.mu.Lock()
	if m.state == nil {
		m.mu.Unlock()
		return errors.New("master: no session submitted; call SubmitIntent first")
	}
	m.mu.Unlock()

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	// Buffered to the global concurrency cap so dispatch workers never block
	// on send (which would leak goroutines on early shutdown). The Scheduler
	// guarantees in-flight <= cap, so the buffer always has room.
	cap := m.cfg.MaxGlobalConcurrency
	if cap < 1 {
		cap = 1
	}
	resultsCh := make(chan taskResult, cap)

	// Tracks live dispatch workers so drain can wait for them to exit cleanly.
	var wg sync.WaitGroup

	for {
		select {
		case <-ctx.Done():
			m.safeDrain(resultsCh, &wg)
			return ctx.Err()
		case <-m.stop:
			m.safeDrain(resultsCh, &wg)
			return m.runErr
		default:
		}

		// A session already declared terminal (e.g. by an external Stop path)
		// just needs its in-flight work to settle.
		if m.terminal() {
			m.safeDrain(resultsCh, &wg)
			return nil
		}

		// Admission pass: admit-and-launch while the Scheduler has capacity
		// and ready work. admitOne releases m.mu before returning, so no
		// worker ever holds Master.mu across Execute.
		for {
			task, mgr, slot, ok := m.admitOne(ctx)
			if !ok {
				break
			}
			if !m.chargeOnAdmit(task) {
				m.orch.Release(slot, task.ID, false)
				m.mu.Lock()
				if m.state != nil && !m.state.Terminal {
					m.state.SetTerminal("budget_exhausted")
				}
				m.mu.Unlock()
				m.safeDrain(resultsCh, &wg)
				return nil
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				m.executeAsync(ctx, task, mgr, slot, resultsCh)
			}()
		}

		// Nothing queued and nothing in flight: research is complete — but
		// first flush any results still buffered in resultsCh. A fast worker
		// can release its slot (driving Active→0) while a result is still
		// buffered here; that result may trigger a replan, so observe+decide
		// it before declaring completion.
		if m.drained() {
		flushBuffer:
			for {
				select {
				case tr := <-resultsCh:
					if stop := m.observeAndDecide(ctx, tr.task, &tr.result); stop {
						m.handleStop(resultsCh, &wg)
						return nil
					}
				default:
					break flushBuffer
				}
			}
			if m.drained() {
				m.mu.Lock()
				if m.state != nil && !m.state.Terminal {
					m.state.SetTerminal("research_complete")
				}
				m.mu.Unlock()
				return nil
			}
			// New work surfaced from processing buffered results (e.g. a
			// replan); fall through to the result pass to re-admit it.
		}

		// Result pass: wait for a completion, a control signal, or a ticker
		// tick (to re-admit backoff-expired tasks). observeAndDecide runs
		// only here, in the Run goroutine, under m.mu where it mutates state.
		select {
		case <-ctx.Done():
			m.safeDrain(resultsCh, &wg)
			return ctx.Err()
		case <-m.stop:
			m.safeDrain(resultsCh, &wg)
			return m.runErr
		case tr := <-resultsCh:
			if stop := m.observeAndDecide(ctx, tr.task, &tr.result); stop {
				m.handleStop(resultsCh, &wg)
				return nil
			}
		case <-ticker.C:
		}
	}
}

// executeAsync runs a task on its manager and delivers the outcome to the
// results channel. It is invoked from a worker goroutine so the Master never
// holds m.mu (nor the Scheduler's lock) across Execute. Release is deferred
// first so the worker slot is always freed, even on panic. The ok flag is a
// no-op for the scheduler's accounting (it only frees the slot); the real
// success/failure semantics are decided by observeAndDecide's decision path.
func (m *Master) executeAsync(ctx context.Context, task model.Task, mgr manager.Manager, slot manager.WorkerSlot, results chan<- taskResult) {
	defer m.orch.Release(slot, task.ID, false)
	result, err := mgr.Execute(task)
	if err != nil {
		result = model.TaskResult{
			Status: model.RetrievalStatusInvalidContent,
			Error:  err,
		}
	}
	select {
	case results <- taskResult{task: task, mgr: mgr, result: result, err: err}:
	case <-ctx.Done():
	}
}

// drain waits for all in-flight dispatch workers to exit, then collects and
// discards any results still buffered in resultsCh. On a graceful exit the
// workers send their result (the buffered channel always has room) and exit;
// on cancel the workers abort via the ctx.Done() arm in executeAsync. Either
// way no goroutine is left blocked on send.
func (m *Master) drain(resultsCh chan taskResult, wg *sync.WaitGroup) {
	wg.Wait()
	for {
		select {
		case <-resultsCh:
		default:
			return
		}
	}
}

// safeDrain behaves like drain unless a Cancel has already been invoked.
// After Cancel the run context is cancelled so worker goroutines may still
// be blocked inside Execute(); waiting on wg would deadlock. safeDrain
// skips the wait in that case, allowing Run to return promptly.
func (m *Master) safeDrain(resultsCh chan taskResult, wg *sync.WaitGroup) {
	m.mu.Lock()
	cancelled := m.cancelled
	m.mu.Unlock()
	if !cancelled {
		m.drain(resultsCh, wg)
		return
	}
	for {
		select {
		case <-resultsCh:
		default:
			return
		}
	}
}

// handleStop is invoked when observeAndDecide returns a terminal stop decision.
// If StopPending is set (DecisionStop), it polls drained() while processing
// any buffered results, up to drainTimeout. Buffered results from completed
// workers may allow pending tasks to settle and drained() to return true
// without resorting to Cancel. If the timeout fires and the system is
// genuinely stuck (e.g. a worker blocked inside Execute), Cancel is invoked
// to purge state and unblock the Run loop.
func (m *Master) handleStop(resultsCh chan taskResult, wg *sync.WaitGroup) {
	m.mu.Lock()
	sp := m.StopPending
	m.mu.Unlock()
	if !sp {
		m.safeDrain(resultsCh, wg)
		return
	}

	deadline := time.Now().Add(m.drainTimeout)
	for time.Now().Before(deadline) {
		select {
		case tr := <-resultsCh:
			_ = m.observeAndDecide(context.Background(), tr.task, &tr.result)
		default:
		}
		if m.drained() {
			m.safeDrain(resultsCh, wg)
			m.mu.Lock()
			m.StopPending = false
			m.mu.Unlock()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	activeBeforeCancel := m.orch.Stats()
	workersStuck := activeBeforeCancel.Active > 0

	m.Cancel()
	m.mu.Lock()
	m.StopPending = false
	if m.state != nil && m.state.Terminal && workersStuck {
		m.state.SetTerminal("cancelled_stuck_workers")
	}
	m.mu.Unlock()
}

// waitDrainedWithTimeout polls drained() for up to drainTimeout (5s). If the
// system drains naturally within the window it returns immediately. Otherwise
// Cancel is invoked to forcibly purge remaining state and abort Run.
func (m *Master) waitDrainedWithTimeout() {
	if m.draining {
		return
	}
	m.mu.Lock()
	m.draining = true
	timeout := m.drainTimeout
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.draining = false
		m.mu.Unlock()
	}()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if m.drained() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	m.Cancel()
}

// Cancel forcibly terminates a stuck drain. It purges the pending/tasks maps,
// marks the session terminal, and cancels the Run context so Master.Run can
// return without waiting on worker goroutines that may never release their
// slots (e.g. a manager whose Execute is blocked indefinitely). If the
// Orchestration implements TaskCanceller, remaining task IDs are forwarded
// so the scheduler can signal stuck workers to abort.
func (m *Master) Cancel() {
	m.mu.Lock()
	if m.cancelled {
		m.mu.Unlock()
		return
	}
	taskIDs := make([]model.TaskID, 0, len(m.pending))
	for id := range m.pending {
		taskIDs = append(taskIDs, id)
	}
	m.pending = map[model.TaskID]bool{}
	m.tasks = map[model.TaskID]model.Task{}
	m.StopPending = false
	m.cancelled = true
	if m.state != nil && !m.state.Terminal {
		m.state.SetTerminal("cancelled_stuck_workers")
	}
	cancelFn := m.cancelFn
	m.mu.Unlock()

	if cancelFn != nil {
		cancelFn()
	}

	if tc, ok := m.orch.(TaskCanceller); ok {
		for _, id := range taskIDs {
			tc.CancelTask(id)
		}
	}
}

func (m *Master) admitOne(ctx context.Context) (model.Task, manager.Manager, manager.WorkerSlot, bool) {
	m.mu.Lock()
	if m.state == nil || m.state.Terminal {
		m.mu.Unlock()
		return model.Task{}, nil, manager.WorkerSlot{}, false
	}
	m.mu.Unlock()

	return m.orch.Admit(time.Now().UTC())
}

func (m *Master) chargeOnAdmit(t model.Task) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == nil {
		return false
	}
	return m.state.Charge(t.EstimatedCost)
}

func (m *Master) observeAndDecide(ctx context.Context, t model.Task, r *model.TaskResult) bool {
	m.extractAndStoreEvidence(ctx, t, r)

	m.updateProgress(t, *r)

	// Phase E: ingestion is best-effort and non-fatal...
	_ = m.ingest.Process(ctx, t, *r)

	m.mu.Lock()
	state := m.state.Clone()
	m.mu.Unlock()

	in := DecisionInput{
		Task:    t,
		Result:  *r,
		State:   state,
		Retry:   m.cfg.Retry,
		Upgrade: m.cfg.Upgrade,
		Replan:  m.cfg.Replan,
		Stats:   m.orch.Stats(),
	}
	decision := m.decider.Decide(in)
	m.emitEventsForDecision(t, decision)
	return m.applyDecision(ctx, t, *r, decision)
}

func (m *Master) applyDecision(ctx context.Context, t model.Task, r model.TaskResult, d Decision) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Phase E: reject task injection while StopPending is active. Only
	// terminal decisions (Terminate/Stop) are allowed through to the
	// idempotent handler below; all other decisions that would inject new
	// tasks (Execute-with-Replan, Upgrade, Rediscover, AddVerification,
	// Retry) are rejected with a warning event.
	if m.StopPending {
		switch d.Action {
		case DecisionTerminate, DecisionStop:
			// fall through to the idempotent terminal handler below
		default:
			m.emitEvent(Event{
				SessionID: t.SessionID,
				TaskID:    &t.ID,
				Kind:      "stop_pending_reject",
				Level:     "WARN",
				Message:   fmt.Sprintf("task injection via %s rejected: session stop pending", d.Action),
				TS:        time.Now().UTC(),
			})
			m.completeTask(t)
			m.persistTaskState(t.ID, t.State)
			return false
		}
	}

	switch d.Action {
	case DecisionRetry:
		t.RetryCount++
		t.BackoffUntil = time.Now().Add(d.Backoff)
		t.State = model.TaskStateRetryWait
		m.tasks[t.ID] = t
		m.pending[t.ID] = true
		_ = m.orch.Submit(t)
		m.persistTask(t)
	case DecisionUpgrade:
		if d.UpgradeTo == nil {
			return false
		}
		cost := m.cfg.CostModel[*d.UpgradeTo]
		t2 := t
		t2.ID = model.NewTaskID()
		t2.Type = *d.UpgradeTo
		t2.State = model.TaskStateReady
		t2.ParentTaskID = &t.ID
		t2.CreatedByTaskID = &t.ID
		t2.EstimatedCost = cost
		t2.TaskKey = fmt.Sprintf("upgrade:%s:%s", d.Action, t.TaskKey)
		t2.ErrorWeight = model.ErrorWeightLow
		t2.RetryCount = 0
		t2.CreatedAt = time.Now().UTC()
		delete(m.pending, t.ID)
		m.tasks[t2.ID] = t2
		m.pending[t2.ID] = true
		m.orch.Submit(t2)
		m.persistTask(t2)
	case DecisionRediscover:
		r2 := m.reseedTask(t, "rediscover")
		m.tasks[r2.ID] = r2
		m.pending[r2.ID] = true
		m.orch.Submit(r2)
		m.persistTask(r2)
	case DecisionAddVerification:
		r2 := m.reseedTask(t, "verify")
		m.tasks[r2.ID] = r2
		m.pending[r2.ID] = true
		m.orch.Submit(r2)
		m.persistTask(r2)
	case DecisionExecute:
		if d.HasReplan() && m.state != nil && m.state.CanReplan() {
			m.state.RecordReplan()
			delta := m.planner.Replan(m.state.Clone(), *d.Replan)
			_ = m.mergeDelta(delta)
		}
		m.completeTask(t)
		m.persistTaskState(t.ID, t.State)
	case DecisionTerminate, DecisionStop:
		m.completeTask(t)
		m.persistTaskState(t.ID, t.State)
		if d.Action == DecisionStop {
			if m.StopPending {
				return true
			}
			m.StopPending = true
			if m.state != nil {
				terminalRsn := reasonFor(d.Action, d.Reason)
				if m.state.Evidence.Contradictions >= m.cfg.Replan.KContradictions {
					terminalRsn = "research_complete"
				}
				m.state.SetTerminal(terminalRsn)
			}
			return true
		}
		if m.state != nil {
			m.state.SetTerminal(reasonFor(d.Action, d.Reason))
		}
		return true
	}
	return false
}

func (m *Master) reseedTask(orig model.Task, kind string) model.Task {
	m.redisc[orig.ID]++
	tt := model.TaskTypeDiscover
	if kind == "verify" {
		tt = model.TaskTypeVerify
	}
	cost := m.cfg.CostModel[tt]
	return model.Task{
		ID:              model.NewTaskID(),
		SessionID:       orig.SessionID,
		Type:            tt,
		State:           model.TaskStateReady,
		Priority:        50,
		SourceClass:     orig.SourceClass,
		SourceTarget:    orig.SourceTarget,
		URL:             orig.URL,
		CrawlDepth:      orig.CrawlDepth + 1,
		TaskDepth:       orig.TaskDepth + 1,
		EstimatedCost:   cost,
		TaskKey:         fmt.Sprintf("%s:%s:%d", kind, orig.TaskKey, m.redisc[orig.ID]),
		ParentTaskID:    &orig.ID,
		CreatedByTaskID: &orig.ID,
		CreatedAt:       time.Now().UTC(),
	}
}

func (m *Master) mergeDelta(delta model.PlanDelta) error {
	if m.state == nil || m.state.Plan == nil {
		return nil
	}
	// Defense-in-depth against task injection via a direct mergeDelta call.
	// The applyDecision pre-switch guard already blocks non-terminal decisions
	// (which is where the DecisionExecute/Replan path injects tasks), so this
	// only affects callers that invoke mergeDelta directly. Returning
	// ErrStopPending prevents out-of-band replan deltas from enqueuing tasks on
	// a stopped session. Since applyDecision calls m.orch.Submit while holding
	// m.mu, a lock-based guard on the orch.Submit path would deadlock; the
	// pre-switch guard above covers that case, making a scheduler-level Submit
	// guard redundant — no atomic.Bool mirror is needed.
	if m.StopPending {
		sid := model.SessionID("")
		if m.state.Session != nil {
			sid = m.state.Session.ID
		}
		m.emitEvent(Event{
			SessionID: sid,
			Kind:      "stop_pending_reject",
			Level:     "WARN",
			Message:   fmt.Sprintf("task injection via replan/mergeDelta rejected: session stop pending (add=%d drop=%d)", len(delta.Add), len(delta.Drop)),
			TS:        time.Now().UTC(),
		})
		return ErrStopPending
	}
	plan := MergePlan(*m.state.Plan, delta)
	m.state.Plan = &plan
	for i := range delta.Add {
		t := delta.Add[i]
		m.tasks[t.ID] = t
		m.pending[t.ID] = true
		_ = m.orch.Submit(t)
	}
	for _, id := range delta.Drop {
		delete(m.pending, id)
		m.dropped[id] = true
	}
	for id, p := range delta.AdjustPriority {
		if tk, ok := m.tasks[id]; ok {
			tk.Priority = p
			m.tasks[id] = tk
		}
	}
	return nil
}

func (m *Master) completeTask(t model.Task) {
	delete(m.pending, t.ID)
	if phaseName := PhaseForTaskType(t.Type); phaseName != "" {
		if allDone := m.phaseAllDone(phaseName); allDone {
			m.state.MarkPhaseCompleted(phaseName)
		}
	}
}

func (m *Master) phaseAllDone(name string) bool {
	plan := m.state.Plan
	for _, ph := range plan.Phases {
		if ph.Name == name {
			for _, tid := range ph.Tasks {
				if m.pending[tid] {
					return false
				}
			}
			return true
		}
	}
	return true
}

func (m *Master) updateProgress(t model.Task, r model.TaskResult) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == nil {
		return
	}
	if len(r.Evidence) > 0 {
		m.state.AddEvidence(len(r.Evidence))
	}
	m.state.RefreshEvidenceCounts(m.evidence)
}

func (m *Master) terminal() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state != nil && m.state.Terminal
}

func (m *Master) drained() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == nil {
		return true
	}
	if len(m.pending) > 0 {
		return false
	}
	stats := m.orch.Stats()
	return stats.Queued == 0 && stats.Active == 0
}

func (m *Master) State() ResearchState {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == nil {
		return ResearchState{}
	}
	return m.state.Clone()
}

func (m *Master) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runErr == nil {
		m.runErr = context.Canceled
	}
	if m.stop != nil {
		select {
		case <-m.stop:
		default:
			close(m.stop)
		}
	}
	return nil
}

func reasonFor(a DecisionAction, r string) string {
	if r != "" {
		return r
	}
	return string(a)
}
