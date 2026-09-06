package master

import (
	"testing"
	"time"

	"draw/internal/config"
	"draw/internal/model"
)

func testDecider() Decider {
	return NewDecider(config.Defaults())
}

func decisionInput(task model.Task, result model.TaskResult, st ResearchState, stats SchedulerStats) DecisionInput {
	return DecisionInput{
		Task:    task,
		Result:  result,
		State:   st,
		Retry:   config.DefaultRetry(),
		Upgrade: config.DefaultUpgrade(),
		Replan:  config.DefaultReplan(),
		Stats:   stats,
	}
}

func stateWithBudget(budget int, evidence EvidenceCounts) ResearchState {
	st := NewResearchState(baseSession(), basePlan(), config.Defaults())
	st.BudgetTotal = budget
	st.BudgetUsed = 0
	st.Evidence = evidence
	return *st
}

func task(tt model.TaskType, retry int, w model.ErrorWeight, cost int) model.Task {
	return model.Task{
		ID:            model.NewTaskID(),
		SessionID:     baseSession().ID,
		Type:          tt,
		State:         model.TaskStateRunning,
		Priority:      10,
		EstimatedCost: cost,
		RetryCount:    retry,
		ErrorWeight:   w,
	}
}

func statsWith(browserActive, browserCap int) SchedulerStats {
	return SchedulerStats{GlobalCap: 10, BrowserCap: browserCap, BrowserActive: browserActive}
}

func TestDecisionOnePerBranch(t *testing.T) {
	cases := []struct {
		name   string
		status model.RetrievalStatus
		retry  int
		stats  SchedulerStats
		want   DecisionAction
	}{
		{"success no replan", model.RetrievalStatusSuccess, 0, statsWith(0, 4), DecisionExecute},
		{"partial no replan", model.RetrievalStatusPartial, 0, statsWith(0, 4), DecisionExecute},
		{"empty", model.RetrievalStatusEmpty, 0, statsWith(0, 4), DecisionRediscover},
		{"js required upgrade", model.RetrievalStatusJavascriptRequired, 0, statsWith(0, 4), DecisionUpgrade},
		{"auth required terminal", model.RetrievalStatusAuthRequired, 0, statsWith(0, 4), DecisionTerminate},
		{"timeout retry", model.RetrievalStatusTimeout, 0, statsWith(0, 4), DecisionRetry},
		{"blocked terminal", model.RetrievalStatusBlocked, 0, statsWith(0, 4), DecisionTerminate},
		{"invalid content retry", model.RetrievalStatusInvalidContent, 0, statsWith(0, 4), DecisionRetry},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := stateWithBudget(500, EvidenceCounts{})
			in := decisionInput(task(model.TaskTypeFetchHTTP, tc.retry, model.ErrorWeightLow, 1),
				model.TaskResult{Status: tc.status}, st, tc.stats)
			d := testDecider().Decide(in)
			if d.Action != tc.want {
				t.Errorf("action: got %s want %s", d.Action, tc.want)
			}
			if d.Reason == "" {
				t.Error("decision must carry a non-empty reason")
			}
		})
	}
}

func TestDecisionTerminalOnBudgetExhausted(t *testing.T) {
	st := stateWithBudget(50, EvidenceCounts{})
	st.BudgetUsed = 50
	in := decisionInput(task(model.TaskTypeFetchHTTP, 0, model.ErrorWeightLow, 1),
		model.TaskResult{Status: model.RetrievalStatusSuccess}, st, statsWith(0, 4))
	d := testDecider().Decide(in)
	if d.Action != DecisionTerminate {
		t.Errorf("budget exhausted: got %s want TERMINATE", d.Action)
	}
	if d.Reason == "" {
		t.Error("reason required")
	}
}

func TestDecisionTimeoutExhaustedRetriesTerminalWhenNoBrowser(t *testing.T) {
	rp := config.DefaultRetry()
	st := stateWithBudget(500, EvidenceCounts{})
	in := decisionInput(task(model.TaskTypeFetchHTTP, rp.MaxAttempts, model.ErrorWeightModerate, 1),
		model.TaskResult{Status: model.RetrievalStatusTimeout}, st, statsWith(0, 0))
	in.Retry = rp
	d := testDecider().Decide(in)
	if d.Action != DecisionTerminate {
		t.Errorf("timeout retries exhausted, no capacity: got %s want TERMINATE", d.Action)
	}
}

func TestDecisionTimeoutExhaustedRetriesThenUpgrade(t *testing.T) {
	rp := config.DefaultRetry()
	rp.MaxAttempts = 10
	st := stateWithBudget(500, EvidenceCounts{})
	in := decisionInput(task(model.TaskTypeFetchHTTP, rp.MaxAttempts, model.ErrorWeightModerate, 1),
		model.TaskResult{Status: model.RetrievalStatusTimeout}, st, statsWith(1, 4))
	in.Retry = rp
	d := testDecider().Decide(in)
	if d.Action != DecisionUpgrade {
		t.Errorf("timeout retries exhausted, capacity available: got %s want UPGRADE", d.Action)
	}
	if d.UpgradeTo == nil || *d.UpgradeTo != model.TaskTypeFetchBrowser {
		t.Error("upgrade target should be FETCH_BROWSER")
	}
}

func TestDecisionBlockingErrorIsTerminal(t *testing.T) {
	st := stateWithBudget(500, EvidenceCounts{})
	in := decisionInput(task(model.TaskTypeFetchHTTP, 0, model.ErrorWeightBlocking, 1),
		model.TaskResult{Status: model.RetrievalStatusTimeout}, st, statsWith(0, 4))
	d := testDecider().Decide(in)
	if d.Action != DecisionTerminate {
		t.Errorf("blocking error: got %s want TERMINATE", d.Action)
	}
}

func TestDecisionBackoffExponentialCapped(t *testing.T) {
	rp := config.DefaultRetry()
	st := stateWithBudget(500, EvidenceCounts{})

	in := decisionInput(task(model.TaskTypeFetchHTTP, 0, model.ErrorWeightModerate, 1),
		model.TaskResult{Status: model.RetrievalStatusTimeout}, st, statsWith(0, 0))
	in.Retry = rp
	d := testDecider().Decide(in)
	if d.Backoff != rp.BackoffBase {
		t.Errorf("attempt 0 backoff: got %s want %s", d.Backoff, rp.BackoffBase)
	}

	rp.MaxAttempts = 10
	in = decisionInput(task(model.TaskTypeFetchHTTP, 6, model.ErrorWeightModerate, 1),
		model.TaskResult{Status: model.RetrievalStatusTimeout}, st, statsWith(0, 0))
	in.Retry = rp
	d = testDecider().Decide(in)
	if d.Backoff != rp.BackoffMax {
		t.Errorf("attempt 6 backoff capped: got %s want %s", d.Backoff, rp.BackoffMax)
	}
}

func TestDecisionUpgradeRespectsBudgetAndCapacity(t *testing.T) {
	st := stateWithBudget(5, EvidenceCounts{})
	in := decisionInput(task(model.TaskTypeFetchHTTP, 0, model.ErrorWeightLow, 1),
		model.TaskResult{Status: model.RetrievalStatusJavascriptRequired}, st, statsWith(0, 4))
	d := testDecider().Decide(in)
	if d.Action != DecisionTerminate {
		t.Errorf("browser cost 10 > remaining 5 -> terminal: got %s want TERMINATE", d.Action)
	}
}

func TestDecisionReplanOnExceededContradictions(t *testing.T) {
	st := stateWithBudget(500, EvidenceCounts{Contradictions: 2})
	in := decisionInput(task(model.TaskTypeFetchHTTP, 0, model.ErrorWeightLow, 1),
		model.TaskResult{Status: model.RetrievalStatusSuccess}, st, statsWith(0, 4))
	d := testDecider().Decide(in)
	if d.Action != DecisionExecute {
		t.Fatal("action should be execute")
	}
	if d.Replan == nil || d.Replan.Kind != ReplanTriggerContradiction {
		t.Errorf("expected contradiction replan trigger, got %+v", d.Replan)
	}
	if d.Reason == "" {
		t.Error("reason required even when replan scheduled")
	}
}

func TestDecisionNoReplanBelowThresholds(t *testing.T) {
	st := stateWithBudget(500, EvidenceCounts{})
	in := decisionInput(task(model.TaskTypeFetchHTTP, 0, model.ErrorWeightLow, 1),
		model.TaskResult{Status: model.RetrievalStatusSuccess}, st, statsWith(0, 4))
	d := testDecider().Decide(in)
	if d.Replan != nil {
		t.Error("should not replan when counts below thresholds")
	}
}

func TestDecisionReplanPriorityContradictionOverOthers(t *testing.T) {
	st := stateWithBudget(500, EvidenceCounts{Contradictions: 2, MissingPrimary: 3, StaleSources: 2})
	in := decisionInput(task(model.TaskTypeFetchHTTP, 0, model.ErrorWeightLow, 1),
		model.TaskResult{Status: model.RetrievalStatusSuccess}, st, statsWith(0, 4))
	d := testDecider().Decide(in)
	if d.Replan == nil || d.Replan.Kind != ReplanTriggerContradiction {
		t.Errorf("contradiction should win over missing/stale: got %+v", d.Replan)
	}
}

func TestDecisionReplanMissingPrimaryWhenNoContradiction(t *testing.T) {
	st := stateWithBudget(500, EvidenceCounts{MissingPrimary: 2})
	in := decisionInput(task(model.TaskTypeFetchHTTP, 0, model.ErrorWeightLow, 1),
		model.TaskResult{Status: model.RetrievalStatusPartial}, st, statsWith(0, 4))
	d := testDecider().Decide(in)
	if d.Replan == nil || d.Replan.Kind != ReplanTriggerMissingPrimary {
		t.Errorf("expected missing-primary replan: got %+v", d.Replan)
	}
}

func TestBackoffBaseCappedByMax(t *testing.T) {
	rp := config.RetryPolicy{MaxAttempts: 10, BackoffBase: 2 * time.Second, BackoffMax: 5 * time.Second}
	st := stateWithBudget(500, EvidenceCounts{})
	in := decisionInput(task(model.TaskTypeFetchHTTP, 1, model.ErrorWeightModerate, 1),
		model.TaskResult{Status: model.RetrievalStatusTimeout}, st, statsWith(0, 0))
	in.Retry = rp
	d := testDecider().Decide(in)
	want := 2 * time.Second * 2
	if d.Backoff != want {
		t.Errorf("attempt 1 backoff (under cap): got %s want %s", d.Backoff, want)
	}
}

type fakeBrowserAuth struct {
	authorized bool
}

func (f *fakeBrowserAuth) Authorized(string) bool {
	return f.authorized
}

func TestDecisionAuthorizeBrowserWhenAuthorized(t *testing.T) {
	d := testDecider()
	d.BrowserAuth = &fakeBrowserAuth{authorized: true}
	st := stateWithBudget(500, EvidenceCounts{})
	in := decisionInput(task(model.TaskTypeFetchHTTP, 0, model.ErrorWeightLow, 1),
		model.TaskResult{Status: model.RetrievalStatusAuthRequired}, st, statsWith(1, 4))
	dec := d.Decide(in)
	if dec.Action != DecisionUpgrade {
		t.Fatalf("authorized browser: got %s want UPGRADE", dec.Action)
	}
	if dec.UpgradeTo == nil || *dec.UpgradeTo != model.TaskTypeFetchBrowser {
		t.Error("upgrade target should be FETCH_BROWSER")
	}
}

func TestDecisionAuthRequiredTerminalWhenUnauthorized(t *testing.T) {
	d := testDecider()
	st := stateWithBudget(500, EvidenceCounts{})
	in := decisionInput(task(model.TaskTypeFetchHTTP, 0, model.ErrorWeightLow, 1),
		model.TaskResult{Status: model.RetrievalStatusAuthRequired}, st, statsWith(1, 4))
	dec := d.Decide(in)
	if dec.Action != DecisionTerminate {
		t.Errorf("unauthorized: got %s want TERMINATE", dec.Action)
	}
	if dec.Reason == "" {
		t.Error("decision must carry a non-empty reason")
	}
}

func TestDecisionStopOnReplanBudgetExhausted(t *testing.T) {
	st := stateWithBudget(500, EvidenceCounts{Contradictions: 2})
	st.ReplanCount = 2
	st.MaxReplans = 2
	in := decisionInput(task(model.TaskTypeFetchHTTP, 0, model.ErrorWeightLow, 1),
		model.TaskResult{Status: model.RetrievalStatusSuccess}, st, statsWith(0, 4))
	d := testDecider().Decide(in)
	if d.Action != DecisionStop {
		t.Errorf("replan budget exhausted + trigger present: got %s want STOP", d.Action)
	}
	if d.Reason == "" {
		t.Error("DecisionStop must carry a non-empty reason")
	}
}

func TestDecisionNoStopWhenReplanBudgetAvailable(t *testing.T) {
	st := stateWithBudget(500, EvidenceCounts{Contradictions: 2})
	// ReplanCount=0 < MaxReplans=2 → CanReplan() is true → DecisionExecute with replan
	in := decisionInput(task(model.TaskTypeFetchHTTP, 0, model.ErrorWeightLow, 1),
		model.TaskResult{Status: model.RetrievalStatusSuccess}, st, statsWith(0, 4))
	d := testDecider().Decide(in)
	if d.Action == DecisionStop {
		t.Error("should NOT produce DecisionStop when replan budget is available")
	}
	if d.Action != DecisionExecute {
		t.Errorf("expected EXECUTE (with replan), got %s", d.Action)
	}
	if d.Replan == nil || d.Replan.Kind != ReplanTriggerContradiction {
		t.Error("expected contradiction replan trigger to be scheduled")
	}
}

func TestDecisionStopOnMissingPrimaryExhaustion(t *testing.T) {
	st := stateWithBudget(500, EvidenceCounts{MissingPrimary: 2})
	st.ReplanCount = 2
	st.MaxReplans = 2
	in := decisionInput(task(model.TaskTypeFetchHTTP, 0, model.ErrorWeightLow, 1),
		model.TaskResult{Status: model.RetrievalStatusSuccess}, st, statsWith(0, 4))
	d := testDecider().Decide(in)
	if d.Action != DecisionStop {
		t.Errorf("missing-primary budget exhausted: got %s want STOP", d.Action)
	}
}
