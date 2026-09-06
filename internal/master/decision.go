package master

import (
	"fmt"
	"time"

	"draw/internal/config"
	"draw/internal/model"
)

type DecisionEngine interface {
	Decide(DecisionInput) Decision
}

type Decider struct {
	Costs       map[model.TaskType]int
	BrowserAuth BrowserAuthProvider
}

func NewDecider(cfg config.SchedulerConfig) Decider {
	cm := cfg.CostModel
	if cm == nil {
		cm = model.DefaultCostModel()
	}
	return Decider{
		Costs:       cm,
		BrowserAuth: disabledBrowserAuth{},
	}
}

func (d Decider) Decide(in DecisionInput) Decision {
	t := in.Task
	r := in.Result
	st := in.State

	if t.IsTerminal() {
		return Decision{Action: DecisionExecute, Reason: fmt.Sprintf("task %s already terminal; observe complete", t.ID)}
	}

	if BudgetExhausted(st.Clone()) {
		return Decision{
			Action: DecisionTerminate,
			Reason: fmt.Sprintf("effort budget exhausted (%d/%d)", st.BudgetUsed, st.BudgetTotal),
		}
	}

	if t.ErrorWeight == model.ErrorWeightBlocking && isErrorResult(r.Status) {
		return Decision{
			Action: DecisionTerminate,
			Reason: fmt.Sprintf("blocking error (%s); terminal, no retry or escalation", r.Status),
		}
	}

	if tr := replanTriggerIfAny(in); tr != nil && !st.CanReplan() {
		return Decision{
			Action: DecisionStop,
			Reason: fmt.Sprintf("replan trigger (%s) required but replan budget exhausted (%d/%d); deterministic stop",
				tr.Kind, st.ReplanCount, st.MaxReplans),
		}
	}

	switch r.Status {

	case model.RetrievalStatusSuccess:
		if tr := replanTriggerIfAny(in); tr != nil {
			return Decision{
				Action: DecisionExecute,
				Reason: "success: evidence accepted; replan scheduled by trigger",
				Replan: tr,
			}
		}
		return Decision{
			Action: DecisionExecute,
			Reason: "success: evidence accepted; plan coverage sufficient",
		}

	case model.RetrievalStatusPartial:
		if tr := replanTriggerIfAny(in); tr != nil {
			return Decision{
				Action: DecisionExecute,
				Reason: "partial: kept; replan triggered by missing/contradictory evidence",
				Replan: tr,
			}
		}
		return Decision{
			Action: DecisionExecute,
			Reason: "partial: kept; required information not yet determined missing",
		}

	case model.RetrievalStatusEmpty:
		return Decision{
			Action: DecisionRediscover,
			Reason: "empty result: no evidence returned; re-discover alternate source",
		}

	case model.RetrievalStatusJavascriptRequired:
		if d.canUpgradeToBrowser(st, in) {
			bt := model.TaskTypeFetchBrowser
			return Decision{
				Action:    DecisionUpgrade,
				Reason:    "javascript-required: escalating to browser retrieval once",
				UpgradeTo: &bt,
			}
		}
		return Decision{
			Action: DecisionTerminate,
			Reason: "javascript-required: no browser capacity/budget for escalation",
		}

	case model.RetrievalStatusAuthRequired:
		if d.canAuthorizeBrowser(st, in) {
			bt := model.TaskTypeFetchBrowser
			return Decision{
				Action:    DecisionUpgrade,
				Reason:    "auth-required: user authorized browser session available",
				UpgradeTo: &bt,
			}
		}
		return Decision{
			Action: DecisionTerminate,
			Reason: "auth-required: terminal without explicit user authorization (no bypass)",
		}

	case model.RetrievalStatusTimeout:
		if retryAllowed(t, in.Retry) {
			return Decision{
				Action:  DecisionRetry,
				Reason:  "timeout: retry HTTP per retry policy",
				Backoff: backoffFor(t, in.Retry),
			}
		}
		if d.canUpgradeToBrowser(st, in) {
			bt := model.TaskTypeFetchBrowser
			return Decision{
				Action:    DecisionUpgrade,
				Reason:    "timeout: HTTP retries exhausted; escalate to browser",
				UpgradeTo: &bt,
			}
		}
		return Decision{
			Action: DecisionTerminate,
			Reason: "timeout: retries exhausted and no browser capacity/budget",
		}

	case model.RetrievalStatusBlocked:
		return Decision{
			Action: DecisionTerminate,
			Reason: "blocked: terminal; never bypass access controls (Cor.2)",
		}

	case model.RetrievalStatusInvalidContent:
		if retryAllowed(t, in.Retry) {
			return Decision{
				Action:  DecisionRetry,
				Reason:  "invalid content: retry HTTP with alternate UA/headers",
				Backoff: backoffFor(t, in.Retry),
			}
		}
		return Decision{
			Action: DecisionTerminate,
			Reason: "invalid content: retries exhausted",
		}
	}

	return Decision{
		Action: DecisionTerminate,
		Reason: fmt.Sprintf("unexpected retrieval status %q", r.Status),
	}
}

func (d Decider) canUpgradeToBrowser(st ResearchState, in DecisionInput) bool {
	if st.BudgetUsed+browserCost(d.Costs) > st.BudgetTotal {
		return false
	}
	if in.Stats.BrowserCap <= 0 {
		return false
	}
	return in.Stats.BrowserActive < in.Stats.BrowserCap
}

func (d Decider) canAuthorizeBrowser(st ResearchState, in DecisionInput) bool {
	// Authorized (authenticated) browser sessions (Cor.1) are a Phase D capability.
	// Fail closed: AUTH_REQUIRED stays terminal unless an explicit BrowserAuthProvider
	// authorizes the session's user.
	if d.BrowserAuth == nil {
		return false
	}
	if st.Session == nil {
		return false
	}
	return d.BrowserAuth.Authorized(st.Session.UserID)
}

func browserCost(costs map[model.TaskType]int) int {
	if c, ok := costs[model.TaskTypeFetchBrowser]; ok {
		return c
	}
	return 10
}

func retryAllowed(t model.Task, rp config.RetryPolicy) bool {
	if t.ErrorWeight == model.ErrorWeightBlocking {
		return false
	}
	return t.RetryCount < rp.MaxAttempts
}

func backoffFor(t model.Task, rp config.RetryPolicy) time.Duration {
	if rp.BackoffBase <= 0 {
		rp.BackoffBase = time.Second
	}
	if rp.BackoffMax <= 0 {
		rp.BackoffMax = time.Minute
	}
	b := rp.BackoffBase
	for i := 0; i < t.RetryCount; i++ {
		b *= 2
		if b > rp.BackoffMax {
			b = rp.BackoffMax
			break
		}
	}
	return b
}

func replanTriggerIfAny(in DecisionInput) *ReplanTrigger {
	st := in.State
	rp := in.Replan
	if rp.KContradictions <= 0 && rp.MMissingPrimary <= 0 && rp.SStaleSources <= 0 {
		return nil
	}
	switch {
	case st.Evidence.Contradictions >= rp.KContradictions:
		return &ReplanTrigger{Kind: ReplanTriggerContradiction}
	case st.Evidence.MissingPrimary >= rp.MMissingPrimary:
		return &ReplanTrigger{Kind: ReplanTriggerMissingPrimary, MissingTopic: "unknown"}
	case st.Evidence.StaleSources >= rp.SStaleSources:
		return &ReplanTrigger{Kind: ReplanTriggerStaleSource}
	}
	return nil
}

func isErrorResult(s model.RetrievalStatus) bool {
	return s != model.RetrievalStatusSuccess && s != model.RetrievalStatusPartial
}
