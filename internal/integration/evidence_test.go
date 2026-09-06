package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"draw/internal/browser"
	"draw/internal/config"
	"draw/internal/frontier"
	"draw/internal/manager"
	"draw/internal/managers"
	"draw/internal/master"
	"draw/internal/model"
	"draw/internal/orchestrator"
	"draw/internal/storage"
)

// Compile-time interface assertion: contradictionWorker implements manager.Worker.
var _ manager.Worker = (*contradictionWorker)(nil)

// contradictionWorker implements manager.Worker. It returns Success with no
// data for DISCOVER / VERIFY / RECONCILE (the non-retrieval tasks that the
// Master emits as seeds and as replan artifacts), and returns JSON payloads
// with *different* revenue values on successive FetchHTTP calls so that the
// evidence layer extracts three contradictory claims about "revenue" from
// three distinct sources.
//
// FetchHTTP call #1 -> [{"revenue":100,"name":"Acme"}]  (source: alpha.example)
// FetchHTTP call #2 -> [{"revenue":200,"name":"Acme"}]  (source: beta.example)
// FetchHTTP call #3 -> [{"revenue":300,"name":"Acme"}]  (source: gamma.example)
//
// All three payloads share the JSON shape [{"revenue":N,"name":"Acme"}], so
// evidence.Extract emits the identical Claim path "[0].revenue" (plus "[0]"
// and "[0].name") for every source, guaranteeing that ComputeRelations
// produces CONTRADICTS edges for the revenue claim across all three sources.
type contradictionWorker struct {
	mu         sync.Mutex
	fetchCount int
	calls      []model.TaskType
}

func newContradictionWorker() *contradictionWorker {
	return &contradictionWorker{}
}

func (w *contradictionWorker) Run(t model.Task) (model.TaskResult, error) {
	w.mu.Lock()
	w.calls = append(w.calls, t.Type)
	n := 0
	if t.Type == model.TaskTypeFetchHTTP {
		w.fetchCount++
		n = w.fetchCount
	}
	w.mu.Unlock()

	switch t.Type {
	case model.TaskTypeFetchHTTP:
		// Three successive calls yield 100, 200, 300 — a genuine value
		// disagreement across three sources.
		revenue := 100 + (n-1)*100
		return model.TaskResult{
			Status:  model.RetrievalStatusSuccess,
			Data:    []byte(fmt.Sprintf(`[{"revenue":%d,"name":"Acme"}]`, revenue)),
			Headers: http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	default:
		// DISCOVER, VERIFY, RECONCILE: success but no retrievable data.
		return model.TaskResult{Status: model.RetrievalStatusSuccess}, nil
	}
}

func (w *contradictionWorker) Close() error { return nil }

// newEvidenceTestGraph builds the real execution graph (frontier + resource
// controller + scheduler + router) backed by a real in-memory SQLite
// EvidenceStore and SourceRegistry, mirroring newTestGraph but wiring the
// evidence/storage seam onto the Master. It returns the Master, the
// Scheduler (for direct task injection), and the EvidenceStore (for direct
// SQLite assertions).
func newEvidenceTestGraph(t *testing.T, w manager.Worker, bmgr browser.BrowserManager) (*master.Master, *orchestrator.Scheduler, *storage.SQLiteEvidenceStore) {
	t.Helper()
	cfg := config.Defaults()

	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("storage open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := storage.Apply(context.Background(), db); err != nil {
		t.Fatalf("storage Apply: %v", err)
	}

	es, err := storage.NewSQLiteEvidenceStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteEvidenceStore: %v", err)
	}
	reg, err := storage.NewSQLiteSourceRegistry(db, 0.3)
	if err != nil {
		t.Fatalf("NewSQLiteSourceRegistry: %v", err)
	}

	fr := frontier.NewMemoryFrontier(cfg, reg)
	rc := orchestrator.NewResourceController(cfg, orchestrator.NoopResourceSampler{})
	sched := orchestrator.NewScheduler(cfg, fr, rc, reg, orchestrator.DefaultTaskCapabilities())

	web := managers.NewWebManager(w)
	news := managers.NewNewsManager(w)
	social := managers.NewSocialManager()
	specialized := managers.NewSpecializedManager()
	brow := managers.NewBrowserAdapter(bmgr, 2*time.Minute)
	router := managers.NewRouterManager(web, news, brow, social, specialized)
	if err := sched.RegisterManager(router, router.Capabilities()); err != nil {
		t.Fatalf("register manager: %v", err)
	}

	m := master.NewMaster(cfg, sched,
		master.WithMemorySessions(),
		master.WithEvidenceStore(es),
		master.WithSourceRegistry(reg),
	)
	return m, sched, es
}

// TestPhaseF_EvidenceContradictionCircuit proves the end-to-end circuit:
//
//   TaskResult.Data (JSON)
//     -> evidence.Extract (rule-based, deterministic)
//     -> SQLite EvidenceStore.Put
//     -> evidence.ComputeRelations (CONTRADICTS edges)
//     -> evidence.ComputeVerification (DISPUTED when >= KContradictions=2)
//     -> storeEvidenceReader.Counts (DISPUTED count -> Contradictions)
//     -> DecisionEngine.replanTriggerIfAny (Contradictions >= K=2)
//     -> PlanningEngine.Replan (adds Verify + Reconcile tasks)
//
// Three FetchHTTP tasks from three distinct source hosts each return a JSON
// array reporting a different revenue figure for "Acme". Extract yields a
// "[0].revenue" claim per source with values 100, 200, 300; ComputeRelations
// marks them CONTRADICTS; ComputeVerification marks every such item DISPUTED
// (each incident to >= 2 contradiction edges, meeting KContradictions=2).
// The DecisionEngine observes the DISPUTED count via the real
// storeEvidenceReader, fires a ReplanTrigger, and the PlanningEngine emits
// Verify + Reconcile tasks that are then admitted and executed to completion.
func TestPhaseF_EvidenceContradictionCircuit(t *testing.T) {
	w := newContradictionWorker()
	bmgr := &fakeBrowserManager{}
	m, sched, es := newEvidenceTestGraph(t, w, bmgr)

	// Intent "research Acme Corp, revenue" -> Entity="Acme Corp",
	// Topics=["revenue"]. resolvedTopics() therefore returns ["revenue"],
	// which evidence.Extract stamps on every extracted item.
	sid, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1",
		Query:  "research Acme Corp, revenue",
		Seeds:  []string{"https://alpha.example"},
	})
	if err != nil {
		t.Fatalf("SubmitIntent: %v", err)
	}

	// Inject three FetchHTTP tasks from three distinct source hosts. The seed
	// DISCOVER task (alpha.example) was already queued by SubmitIntent; these
	// three are submitted directly to the scheduler (NOT the master's pending
	// map — they are tracked by the scheduler queue, which drained() inspects
	// via stats.Queued).
	hosts := []string{"alpha.example", "beta.example", "gamma.example"}
	base := time.Now().UTC()
	for i, host := range hosts {
		u, err := url.Parse("https://" + host + "/api")
		if err != nil {
			t.Fatalf("parse url: %v", err)
		}
		task := model.Task{
			ID:            model.NewTaskID(),
			SessionID:     sid,
			Type:          model.TaskTypeFetchHTTP,
			State:         model.TaskStateReady,
			Priority:      500,
			SourceClass:   model.SourceClassUnknown,
			SourceTarget:  host,
			URL:           u,
			EstimatedCost: 1,
			TaskKey:       fmt.Sprintf("evidence:fetch:%d", i),
			CreatedAt:     base.Add(time.Duration(i) * time.Millisecond),
		}
		if err := sched.Submit(task); err != nil {
			t.Fatalf("sched.Submit: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := m.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	st := m.State()

	// (1) storeEvidenceReader bridged the SQLite DISPUTED count into
	//     ResearchState.Evidence.Contradictions; DecisionEngine.replanTriggerIfAny
	//     fired because that count exceeded config KContradictions (=2).
	if st.Evidence.Contradictions < 2 {
		t.Errorf("st.Evidence.Contradictions = %d, want >= 2 (DISPUTED count read via storeEvidenceReader)", st.Evidence.Contradictions)
	}

	// (2) DecisionEngine triggered at least one Replan; PlanningEngine.Replan
	//     added Verify + Reconcile tasks that were subsequently executed.
	if st.ReplanCount < 1 {
		t.Errorf("st.ReplanCount = %d, want >= 1 (PlanningEngine.Replan fired)", st.ReplanCount)
	}

	// (3) Direct SQLite proof: at least 2 evidence items persisted with
	//     Verification = DISPUTED.
	disputed := es.Query(storage.EvidenceFilter{
		SessionID:    sid,
		Verification:   model.VerificationDisputed,
	})
	if len(disputed) < 2 {
		t.Errorf("DISPUTED evidence items in SQLite = %d, want >= 2", len(disputed))
	}

	// (4) Direct SQLite proof: at least 2 CONTRADICTS relations stored.
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
}
