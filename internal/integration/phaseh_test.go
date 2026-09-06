package integration_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"draw/internal/api"
	"draw/internal/auth"
	"draw/internal/manager"
	"draw/internal/model"
	"draw/internal/result"
	"draw/internal/storage"
)

// authRequiredWorker is a denylistWorker-style test fake that simulates a source
// responding HTTP 401: every retrieval task it executes returns
// RetrievalStatusAuthRequired. It records the ordered list of task types it ran,
// so callers can assert no FetchBrowser escalation occurred.
//
// With the default decider (no BrowserAuth provider — see newAPITestGraph, which
// never calls master.WithBrowserAuth), canAuthorizeBrowser is fail-closed, so
// AUTH_REQUIRED is terminal and no browser task is ever admitted.
type authRequiredWorker struct {
	mu    sync.Mutex
	calls []model.TaskType
}

func newAuthRequiredWorker() *authRequiredWorker { return &authRequiredWorker{} }

func (w *authRequiredWorker) Run(t model.Task) (model.TaskResult, error) {
	w.mu.Lock()
	w.calls = append(w.calls, t.Type)
	w.mu.Unlock()
	// HTTP 401 from the source: the decider treats this as AUTH_REQUIRED.
	return model.TaskResult{Status: model.RetrievalStatusAuthRequired}, nil
}

func (w *authRequiredWorker) Close() error { return nil }

var _ manager.Worker = (*authRequiredWorker)(nil)

// calledTaskTypes returns a copy of the worker's executed task-type log.
func (w *authRequiredWorker) taskTypes() []model.TaskType {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]model.TaskType, len(w.calls))
	copy(out, w.calls)
	return out
}

// phaseHDeterminismSignature captures the ID/time-independent structural
// invariants of a ResultEnvelope (reqding §0.12). Evidence IDs and
// collected/completed timestamps differ across runs, so they are deliberately
// excluded; the report's Sources/Findings/Contradictions slices are already
// deterministically ordered by ReportBuilder (sources by domain, findings by
// claim, contradictions by claim pair), so their ordering + scores are stable.
func phaseHDeterminismSignature(env model.ResultEnvelope) string {
	srcs := make([]string, len(env.Payload.Sources))
	for i, s := range env.Payload.Sources {
		srcs[i] = fmt.Sprintf("%s|%.6g|%s", s.Name, s.Quality, s.Class)
	}
	finds := make([]string, len(env.Payload.Findings))
	for i, f := range env.Payload.Findings {
		finds[i] = f.Claim
	}
	strs := make([]float64, len(env.Payload.Contradictions))
	for i, c := range env.Payload.Contradictions {
		strs[i] = c.Strength
	}
	sort.Float64s(strs)
	return fmt.Sprintf("status=%q;sources=%d|%s;findings=%d|%s;contradictions=%d|%v;completeness=%v;confidence=%v",
		env.Status,
		len(env.Payload.Sources), strings.Join(srcs, ","),
		len(env.Payload.Findings), strings.Join(finds, ";"),
		len(env.Payload.Contradictions), strs,
		env.Payload.Completeness, env.Payload.Confidence)
}

// TestPhaseH_DeterminismAcrossRuns closes the reqding §0.12 gap: identical input
// must yield identical task ordering + scores across >=3 runs. Three independent
// contradiction circuits (fresh in-memory SQLite + fresh contradictionWorker each
// time, so fetchCount restarts at 0 → revenue 100/200/300 every run) are run and
// their ResultEnvelopes compared on every deterministic invariant.
//
// The scoring functions (buildCompleteness/buildConfidence) contain no time.Now
// call, so completeness/confidence are stable; evidence IDs and timestamps are
// excluded from the signature exactly because they are intentionally run-specific.
func TestPhaseH_DeterminismAcrossRuns(t *testing.T) {
	r1 := runContradictionCircuit(t)
	r2 := runContradictionCircuit(t)
	r3 := runContradictionCircuit(t)

	s1 := phaseHDeterminismSignature(r1.Env)
	s2 := phaseHDeterminismSignature(r2.Env)
	s3 := phaseHDeterminismSignature(r3.Env)

	if s1 != s2 {
		t.Errorf("determinism: run1 != run2\n run1: %s\n run2: %s", s1, s2)
	}
	if s1 != s3 {
		t.Errorf("determinism: run1 != run3\n run1: %s\n run3: %s", s1, s3)
	}

	d := 0
	if s1 != s2 || s1 != s3 {
		d = 1
	}
	t.Logf("determinism 3 runs: sources=%d/%d/%d contradictions=%d/%d/%d completeness=%v/%v/%v confidence=%v/%v/%v delta=%d",
		len(r1.Env.Payload.Sources), len(r2.Env.Payload.Sources), len(r3.Env.Payload.Sources),
		len(r1.Env.Payload.Contradictions), len(r2.Env.Payload.Contradictions), len(r3.Env.Payload.Contradictions),
		r1.Env.Payload.Completeness, r2.Env.Payload.Completeness, r3.Env.Payload.Completeness,
		r1.Env.Payload.Confidence, r2.Env.Payload.Confidence, r3.Env.Payload.Confidence,
		d)
}

// TestPhaseH_AuthRequiredTerminalWithoutAuth covers Q3 / H29: when no
// BrowserAuth provider authorizes the session's user, an AUTH_REQUIRED result
// (HTTP 401 from the source) must be terminal with no browser escalation.
//
//   - newAPITestGraph wires the default Decider (BrowserAuth = disabledBrowserAuth,
//     fail-closed): canAuthorizeBrowser is always false.
//   - authRequiredWorker returns AuthRequired for the seed DISCOVER task.
//   - SSE emits a terminal report; the worker log proves no FetchBrowser ran.
func TestPhaseH_AuthRequiredTerminalWithoutAuth(t *testing.T) {
	w := newAuthRequiredWorker()
	bmgr := &fakeBrowserManager{}
	m, _, reg, es, ss, evs := newAPITestGraph(t, w, bmgr)

	deps := &api.Deps{
		Master:         m,
		EvidenceStore:  es,
		SourceRegistry: reg,
		EventStore:     evs,
		SessionStore:   ss,
		ReportBuilder:  result.NewReportBuilder(es, reg),
		Auth:           auth.Config{},
	}
	serverCtx, serverCancel := context.WithCancel(context.Background())
	t.Cleanup(func() { serverCancel() })
	server := httptest.NewServer(api.NewServer(deps, serverCtx))
	t.Cleanup(func() { server.Close() })

	// POST /sessions queues the seed DISCOVER (alpha.example) then starts
	// go master.Run via sessionsManager.Submit — exactly the path the API
	// exposes. No DRAW_AUTH_USERS / BrowserAuth is configured, so the user
	// "u1" is explicitly unauthorized.
	sid := createSessionViaAPI(t, server)
	env := sseReport(t, server, sid)

	low := strings.ToLower(env.Status)
	if !strings.Contains(low, "auth-required") {
		t.Errorf("expected terminal AUTH_REQUIRED status, got %q", env.Status)
	}

	// AUTH_REQUIRED must be terminal at the master level too.
	st := m.State()
	if !st.Terminal {
		t.Errorf("expected master terminal state, got Terminal=%v reason=%q", st.Terminal, st.TerminalReason)
	}

	// No browser escalation: no FetchBrowser task was admitted to a worker.
	for _, tt := range w.taskTypes() {
		if tt == model.TaskTypeFetchBrowser {
			t.Error("FetchBrowser task executed; AUTH_REQUIRED escalated when it must be terminal")
		}
	}

	// Contradiction-free terminal state: 401 yields no evidence, hence no
	// contradictions/relations and no sources/findings in the report.
	if len(env.Payload.Contradictions) != 0 {
		t.Errorf("expected contradiction-free report, got %d contradictions", len(env.Payload.Contradictions))
	}
	if len(env.Payload.Sources) != 0 || len(env.Payload.Findings) != 0 {
		t.Errorf("expected empty sources/findings for a 401-only source, got sources=%d findings=%d",
			len(env.Payload.Sources), len(env.Payload.Findings))
	}
	t.Logf("auth-required terminal: status=%q, worker tasks=%v, contradictions=%d",
		env.Status, w.taskTypes(), len(env.Payload.Contradictions))
}

// TestPhaseH_FullRunNoGoroutineLeak closes the reqding §0.10 #6 integration gap
// (Module E §VIII-13): after a full contradiction circuit reaches terminal,
// master.Run's goroutine and any scheduler poller must exit. The httptest
// server (owned by runContradictionCircuit via t.Cleanup) is closed first so the
// delta reflects the Run-loop footprint, not the server's Accept goroutine.
func TestPhaseH_FullRunNoGoroutineLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	r := runContradictionCircuit(t)

	// SSE terminal report already returned inside runContradictionCircuit, so
	// master.Run has reached terminal. Close the harness HTTP server to shed
	// its persistent serve/accept goroutine before measuring.
	r.Server.Close()

	// Give the master.Run goroutine (and the runContradictionCircuit wrapper
	// goroutine that calls ss.Archive) a bounded window to exit.
	deadline := time.Now().Add(2 * time.Second)
	after := runtime.NumGoroutine()
	for time.Now().Before(deadline) && after-before > 2 {
		time.Sleep(25 * time.Millisecond)
		after = runtime.NumGoroutine()
	}

	if delta := after - before; delta > 2 {
		t.Errorf("goroutine leak: delta=%d (before=%d, after=%d), want <= 2", delta, before, after)
	}
	t.Logf("goroutine delta=%d (before=%d, after=%d)", after-before, before, after)
}

// TestPhaseH_ContradictionToDisputedReplan (reqding §0.8.2/§0.8.3) proves the
// integration-level contradiction chain end-to-end across SQLite:
//
//  1. 3 sources return contradictory revenue values (alpha=100, beta=200,
//     gamma=300) → evidence.Extract → SQLite evidence rows.
//  2. evidence.ComputeRelations emits CONTRADICTS edges (SQLite) → >= 2.
//  3. evidence.ComputeVerification marks items VerificationState=DISPUTED.
//  4. storeEvidenceReader feeds DISPUTED count into ResearchState.Evidence;
//     DecisionEngine.replanTriggerIfAny fires → PlanningEngine.Replan →
//     ReplanCount >= 1.
//  5. Terminal report's Contradictions view (built from SQLite relations) is
//     non-empty.
//
// This is the master in-process path (mirrors TestPhaseF) but adds the report
// assertion and explicit SQLite relation counting demanded by Phase H.
func TestPhaseH_ContradictionToDisputedReplan(t *testing.T) {
	w := newContradictionWorker()
	bmgr := &fakeBrowserManager{}
	m, sched, reg, es, _, _ := newAPITestGraph(t, w, bmgr)

	req := model.IntentRequest{
		Query:  "research Acme Corp, revenue",
		Seeds:  []string{"https://alpha.example"},
		UserID: "u1",
	}
	// Q5 fix: SubmitIntent queues the seed DISCOVER but does NOT start Run, so
	// the three contradiction FetchHTTP tasks are injected before the async
	// drain begins (no race on the single seed task).
	sid, err := m.SubmitIntent(req)
	if err != nil {
		t.Fatalf("SubmitIntent: %v", err)
	}
	injectContradictionFetches(t, sched, sid)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	st := m.State()
	if !st.Terminal {
		t.Fatalf("expected terminal state, got %+v", st)
	}

	// (4) DecisionEngine triggered >= 1 replan; evidence reader bridged the
	//     DISPUTED count from SQLite into the research state.
	if st.ReplanCount < 1 {
		t.Errorf("ReplanCount = %d, want >= 1 (DecisionEngine replan on DISPUTED)", st.ReplanCount)
	}
	if st.Evidence.Contradictions < 2 {
		t.Errorf("st.Evidence.Contradictions = %d, want >= 2 (DISPUTED count via storeEvidenceReader)", st.Evidence.Contradictions)
	}

	// (2)+(3) SQLite: >= 2 EvidenceRelation CONTRADICTS edges stored for the
	//        revenue topic, and >= 1 item persisted with Verification=DISPUTED.
	rels := es.FindRelations("revenue")
	contradicts := 0
	for _, rel := range rels {
		if rel.Kind == model.EvidenceRelationContradicts {
			contradicts++
		}
	}
	if contradicts < 2 {
		t.Errorf("CONTRADICTS relations in SQLite = %d, want >= 2", contradicts)
	}

	disputed := es.Query(storage.EvidenceFilter{
		SessionID:    sid,
		Verification: model.VerificationDisputed,
	})
	if len(disputed) < 1 {
		t.Errorf("DISPUTED evidence items in SQLite = %d, want >= 1", len(disputed))
	}
	for _, ev := range disputed {
		if ev.Verification != model.VerificationDisputed {
			t.Errorf("evidence %s verification = %s, want DISPUTED", ev.ID, ev.Verification)
		}
	}

	// (5) Terminal report's Contradictions view is non-empty (integration-level:
	//     derived from the SQLite relations through ReportBuilder).
	rb := result.NewReportBuilder(es, reg)
	env := rb.Build(st)
	if len(env.Payload.Contradictions) < 1 {
		t.Errorf("terminal report Contradictions = %d, want >= 1", len(env.Payload.Contradictions))
	}

	t.Logf("contradiction->disputed->replan: relations=%d contradict=%d disputed=%d replan=%d report_contradictions=%d",
		len(rels), contradicts, len(disputed), st.ReplanCount, len(env.Payload.Contradictions))
}
