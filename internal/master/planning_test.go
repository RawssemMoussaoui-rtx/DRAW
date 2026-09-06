package master

import (
	"testing"

	"draw/internal/config"
	"draw/internal/model"
)

func testPlanMaker() PlanMaker {
	return NewPlanMaker(config.Defaults())
}

func testIntent() model.Intent {
	return model.Intent{
		ID:         model.NewIntentID(),
		Type:       model.IntentTypeResearch,
		Entity:     "Company X",
		CrawlDepth: 2,
		TaskDepth:  3,
		EffortBudget: 500,
		MaxReplans: 3,
	}
}

func TestBuildCreatesEightPhases(t *testing.T) {
	pm := testPlanMaker()
	plan := pm.Build(testIntent())
	if len(plan.Phases) != 8 {
		t.Fatalf("phase count: got %d want 8", len(plan.Phases))
	}
	for i, want := range PlanPhaseOrder {
		if plan.Phases[i].Name != want {
			t.Errorf("phase %d: got %q want %q", i, plan.Phases[i].Name, want)
		}
		if plan.Phases[i].Completed {
			t.Errorf("phase %q should start incomplete", want)
		}
	}
}

func TestBuildPhaseOneEmptyForMasterToSeed(t *testing.T) {
	plan := testPlanMaker().Build(testIntent())
	if len(plan.Phases[0].Tasks) != 0 {
		t.Errorf("phase 0 tasks: got %d want 0 (master seeds them)", len(plan.Phases[0].Tasks))
	}
}

func TestReplanContradictionProducesVerifyAndReconcile(t *testing.T) {
	pm := testPlanMaker()
	st := NewResearchState(activeSession(), basePlan(), config.Defaults())
	st.Evidence = EvidenceCounts{Contradictions: 3, MissingPrimary: 0, StaleSources: 0}
	trigger := ReplanTrigger{Kind: ReplanTriggerContradiction, EvidenceIDs: []model.EvidenceID{"e1", "e2"}}
	delta := pm.Replan(*st, trigger)
	if len(delta.Add) == 0 {
		t.Fatal("expected replan tasks for contradiction")
	}
	kindCounts := map[model.TaskType]int{}
	for _, tk := range delta.Add {
		kindCounts[tk.Type]++
		if tk.Type != model.TaskTypeVerify && tk.Type != model.TaskTypeReconcile {
			t.Errorf("unexpected task type in contradiction replan: %s", tk.Type)
		}
		if tk.SessionID != st.Session.ID {
			t.Error("task session mismatch")
		}
		if tk.Type == model.TaskTypeVerify {
			cost := config.DefaultCostModel()[model.TaskTypeVerify]
			if tk.EstimatedCost != cost {
				t.Errorf("verify cost: got %d want %d", tk.EstimatedCost, cost)
			}
		}
		if tk.TaskKey == "" {
			t.Error("verify tasks must have deterministic task_key")
		}
	}
	if kindCounts[model.TaskTypeReconcile] != 1 {
		t.Error("expected exactly one reconcile task")
	}
}

func TestReplanMissingPrimaryProducesDiscover(t *testing.T) {
	pm := testPlanMaker()
	st := NewResearchState(activeSession(), basePlan(), config.Defaults())
	st.Evidence = EvidenceCounts{MissingPrimary: 4}
	delta := pm.Replan(*st, ReplanTrigger{Kind: ReplanTriggerMissingPrimary, MissingTopic: "revenue"})
	n := 0
	for _, tk := range delta.Add {
		if tk.Type == model.TaskTypeDiscover {
			n++
		}
	}
	if n != 3 {
		t.Errorf("expected 3 discover tasks for missing primary (capped at 3), got %d", n)
	}
}

func TestReplanStaleSourceProducesFetchHTTP(t *testing.T) {
	pm := testPlanMaker()
	st := NewResearchState(activeSession(), basePlan(), config.Defaults())
	st.Evidence = EvidenceCounts{StaleSources: 5}
	delta := pm.Replan(*st, ReplanTrigger{Kind: ReplanTriggerStaleSource})
	n := 0
	for _, tk := range delta.Add {
		if tk.Type == model.TaskTypeFetchHTTP {
			n++
		}
	}
	if n != 2 {
		t.Errorf("expected 2 fetch-http tasks for stale source (capped at 2), got %d", n)
	}
}

func TestReplanCoverageGapAndManual(t *testing.T) {
	pm := testPlanMaker()
	st := NewResearchState(activeSession(), basePlan(), config.Defaults())
	d1 := pm.Replan(*st, ReplanTrigger{Kind: ReplanTriggerCoverageGap})
	if len(d1.Add) != 1 || d1.Add[0].Type != model.TaskTypeDiscover {
		t.Errorf("coverage gap should add one discover, got %+v", d1.Add)
	}
	d2 := pm.Replan(*st, ReplanTrigger{Kind: ReplanTriggerManual})
	if len(d2.Add) != 1 || d2.Add[0].Type != model.TaskTypeDiscover {
		t.Errorf("manual should add one discover, got %+v", d2.Add)
	}
}

func TestReplanDeterministicAcrossRuns(t *testing.T) {
	pm := testPlanMaker()
	st := NewResearchState(activeSession(), basePlan(), config.Defaults())
	st.Evidence = EvidenceCounts{Contradictions: 2}
	trigger := ReplanTrigger{Kind: ReplanTriggerContradiction}
	d0 := pm.Replan(*st, trigger)
	for i := 0; i < 3; i++ {
		d := pm.Replan(*st, trigger)
		if len(d.Add) != len(d0.Add) {
			t.Fatalf("run %d: add count differs", i)
		}
		for j := range d.Add {
			if d.Add[j].TaskKey != d0.Add[j].TaskKey {
				t.Errorf("run %d task %d task_key differs: %q vs %q", i, j, d.Add[j].TaskKey, d0.Add[j].TaskKey)
			}
			if d.Add[j].Priority != d0.Add[j].Priority {
				t.Errorf("run %d task %d priority differs", i, j)
			}
		}
	}
}

func TestReplanExhaustedMaxReplansIsEmpty(t *testing.T) {
	pm := testPlanMaker()
	st := NewResearchState(activeSession(), basePlan(), config.Defaults())
	for i := 0; i < st.MaxReplans; i++ {
		st.RecordReplan()
	}
	delta := pm.Replan(*st, ReplanTrigger{Kind: ReplanTriggerContradiction})
	if len(delta.Add) != 0 {
		t.Errorf("replan at max should add nothing, got %d", len(delta.Add))
	}
}

func TestMergePlanAddAndDrop(t *testing.T) {
	plan := basePlan()
	t2 := model.Task{ID: "t2", Type: model.TaskTypeVerify}
	t3 := model.Task{ID: "t3", Type: model.TaskTypeDiscover}
	plan.Phases[0].Tasks = []model.TaskID{"seed1"}
	plan.Phases[1].Tasks = []model.TaskID{"t1"}

	merged := MergePlan(*plan, model.PlanDelta{
		Add: []model.Task{t2, t3},
		Drop: []model.TaskID{"seed1"},
	})

	p := findPhase(merged, PhaseAdditionalVerification)
	if p == nil {
		t.Fatal("expected verification phase")
	}
	hasT2 := false
	for _, tid := range p.Tasks {
		if tid == "t2" {
			hasT2 = true
		}
	}
	if !hasT2 {
		t.Error("t2 should be added to verification phase")
	}

	disc := findPhase(merged, PhaseDiscovery)
	if disc == nil {
		t.Fatal("expected discovery phase")
	}
	for _, tid := range disc.Tasks {
		if tid == "t3" {
			break
		}
		if tid == "seed1" {
			t.Error("dropped seed1 should be removed from discovery")
		}
		if tid == "t3" {
			// ok
		}
	}
	foundT3 := false
	for _, tid := range disc.Tasks {
		if tid == "t3" {
			foundT3 = true
		}
	}
	if !foundT3 {
		t.Error("t3 should be added to discovery phase")
	}
	for _, tid := range disc.Tasks {
		if tid == "seed1" {
			t.Error("seed1 should have been dropped")
		}
	}
	for _, tid := range findPhase(merged, PhasePrimaryRetrieval).Tasks {
		if tid == "t1" {
			// t1 retained (not dropped)
		}
	}
}

func TestMergePlanDoesNotMutateInput(t *testing.T) {
	plan := basePlan()
	plan.Phases[0].Tasks = []model.TaskID{"a"}
	orig := plan.Phases[0].Tasks
	_ = MergePlan(*plan, model.PlanDelta{
		Add:  []model.Task{{ID: "b", Type: model.TaskTypeDiscover}},
		Drop: []model.TaskID{"a"},
	})
	if len(plan.Phases[0].Tasks) != len(orig) {
		t.Error("merge must not mutate input plan")
	}
}

func findPhase(plan model.Plan, name string) *model.Phase {
	for i := range plan.Phases {
		if plan.Phases[i].Name == name {
			return &plan.Phases[i]
		}
	}
	return nil
}

func activeSession() *model.Session {
	return baseSession()
}
