package master

import (
	"testing"
	"time"

	"draw/internal/config"
	"draw/internal/model"
)

func baseSession() *model.Session {
	return &model.Session{
		ID:     model.NewSessionID(),
		UserID: "u1",
		State:  model.SessionStateActive,
		Intent: model.Intent{
			ID:         model.NewIntentID(),
			Entity:     "Company X",
			EffortBudget: 100,
			MaxReplans: 2,
		},
		CreatedAt: time.Now().UTC(),
	}
}

func basePlan() *model.Plan {
	p := &model.Plan{}
	for _, n := range PlanPhaseOrder {
		p.Phases = append(p.Phases, model.Phase{Name: n})
	}
	return p
}

func TestNewResearchStateDefaults(t *testing.T) {
	s := baseSession()
	st := NewResearchState(s, basePlan(), config.Defaults())
	if st.BudgetTotal != 100 {
		t.Errorf("budget total: got %d want 100", st.BudgetTotal)
	}
	if st.MaxReplans != 2 {
		t.Errorf("max replans: got %d want 2", st.MaxReplans)
	}
	if len(st.PhaseCompleted) != 0 {
		t.Error("new state should have no completed phases")
	}
	if st.PhaseIndex != 0 {
		t.Errorf("phase index: got %d want 0", st.PhaseIndex)
	}
}

func TestChargeBudgetRespectsCap(t *testing.T) {
	st := NewResearchState(baseSession(), basePlan(), config.Defaults())
	if !st.Charge(30) {
		t.Fatal("charge 30 should succeed")
	}
	if !st.Charge(70) {
		t.Fatal("charge 70 should succeed (total 100)")
	}
	if st.Charge(1) {
		t.Error("charge over cap should fail")
	}
	cp := st.Clone()
	if cp.BudgetUsed != 100 {
		t.Errorf("clone budget used: got %d want 100", cp.BudgetUsed)
	}
}

func TestBudgetExhaustedFlag(t *testing.T) {
	st := NewResearchState(baseSession(), basePlan(), config.Defaults())
	st.Charge(100)
	if !BudgetExhausted(st.Clone()) {
		t.Error("expected exhausted")
	}
	st2 := NewResearchState(baseSession(), basePlan(), config.Defaults())
	if BudgetExhausted(st2.Clone()) {
		t.Error("fresh state should not be exhausted")
	}
}

func TestCloneDeepCopiesPlan(t *testing.T) {
	st := NewResearchState(baseSession(), basePlan(), config.Defaults())
	cp := st.Clone()
	cp.Plan.Phases[0].Tasks = append(cp.Plan.Phases[0].Tasks, "x")
	if len(st.Plan.Phases[0].Tasks) != 0 {
		t.Error("clone must not share plan slice")
	}
}

func TestReplanAccounting(t *testing.T) {
	st := NewResearchState(baseSession(), basePlan(), config.Defaults())
	if !st.CanReplan() {
		t.Fatal("should be able to replan initially")
	}
	st.RecordReplan()
	st.RecordReplan()
	if st.ReplanCount != 2 {
		t.Errorf("replan count: got %d want 2", st.ReplanCount)
	}
	if st.CanReplan() {
		t.Error("should not be able to replan beyond MaxReplans")
	}
}

func TestMarkPhaseCompletedAdvancesIndex(t *testing.T) {
	st := NewResearchState(baseSession(), basePlan(), config.Defaults())
	st.MarkPhaseCompleted(PhaseDiscovery)
	cp := st.Clone()
	if !cp.PhaseCompleted[PhaseDiscovery] {
		t.Error("discovery phase should be marked completed")
	}
	if cp.PhaseIndex != 1 {
		t.Errorf("phase index after P1 complete: got %d want 1", cp.PhaseIndex)
	}
}

func TestSetTerminal(t *testing.T) {
	st := NewResearchState(baseSession(), basePlan(), config.Defaults())
	st.SetTerminal("budget_exhausted")
	cp := st.Clone()
	if !cp.Terminal || cp.TerminalReason != "budget_exhausted" {
		t.Error("terminal state not set")
	}
}

func TestRefreshEvidenceCounts(t *testing.T) {
	st := NewResearchState(baseSession(), basePlan(), config.Defaults())
	st.RefreshEvidenceCounts(fakeEvidence{3, 1, 2})
	cp := st.Clone()
	if cp.Evidence.Contradictions != 3 || cp.Evidence.MissingPrimary != 1 || cp.Evidence.StaleSources != 2 {
		t.Errorf("evidence counts wrong: %+v", cp.Evidence)
	}
}

type fakeEvidence struct{ c, m, s int }

func (f fakeEvidence) Counts(model.SessionID) EvidenceCounts {
	return EvidenceCounts{Contradictions: f.c, MissingPrimary: f.m, StaleSources: f.s}
}

func TestMemorySessionStoreRoundTrip(t *testing.T) {
	store := NewMemorySessionStore()
	s := baseSession()
	if err := store.Save(s); err != nil {
		t.Fatal(err)
	}
	got, ok := store.Current("u1")
	if !ok || got.ID != s.ID {
		t.Fatalf("current: ok=%v got=%v want %s", ok, got, s.ID)
	}
	if err := store.Archive(s.ID); err != nil {
		t.Fatal(err)
	}
	got2, _ := store.Current("u1")
	if got2.State != model.SessionStateArchived {
		t.Errorf("state: got %s want %s", got2.State, model.SessionStateArchived)
	}
}

func TestNoopEvidenceReader(t *testing.T) {
	if c := (NoopEvidenceReader()).Counts("x"); c != (EvidenceCounts{}) {
		t.Errorf("noop counts: got %+v", c)
	}
}

func TestPhaseForTaskType(t *testing.T) {
	cases := map[model.TaskType]string{
		model.TaskTypeDiscover:    PhaseDiscovery,
		model.TaskTypeFetchHTTP:   PhasePrimaryRetrieval,
		model.TaskTypeFetchBrowser: PhasePrimaryRetrieval,
		model.TaskTypeVerify:      PhaseAdditionalVerification,
		model.TaskTypeReconcile:   PhaseEvidenceConsolidation,
	}
	for tt, want := range cases {
		if got := PhaseForTaskType(tt); got != want {
			t.Errorf("%s -> %s want %s", tt, got, want)
		}
	}
}
