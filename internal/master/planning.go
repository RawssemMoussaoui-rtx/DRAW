package master

import (
	"fmt"
	"strings"

	"draw/internal/config"
	"draw/internal/model"
)

type PlanningEngine interface {
	Build(model.Intent) model.Plan
	Replan(ResearchState, ReplanTrigger) model.PlanDelta
}

type PlanMaker struct {
	Costs     map[model.TaskType]int
	ReplanMax int
}

func NewPlanMaker(cfg config.SchedulerConfig) PlanMaker {
	cm := cfg.CostModel
	if cm == nil {
		cm = model.DefaultCostModel()
	}
	return PlanMaker{Costs: cm, ReplanMax: cfg.Replan.KContradictions}
}

func (p PlanMaker) Build(intent model.Intent) model.Plan {
	plan := model.Plan{}
	for _, name := range PlanPhaseOrder {
		plan.Phases = append(plan.Phases, model.Phase{
			Name:      name,
			Tasks:     nil,
			Completed: false,
		})
	}
	return plan
}

func (p PlanMaker) Replan(state ResearchState, trigger ReplanTrigger) model.PlanDelta {
	delta := model.PlanDelta{
		Add:            []model.Task{},
		Drop:           []model.TaskID{},
		AdjustPriority: map[model.TaskID]int{},
	}
	if !state.CanReplan() {
		return delta
	}

	session := state.Session.ID
	intent := state.Session.Intent
	rk := replanSeedKey(state, trigger)

	switch trigger.Kind {
	case ReplanTriggerContradiction:
		n := minInt(state.Evidence.Contradictions, 3)
		for i := 0; i < n; i++ {
			delta.Add = append(delta.Add, p.newTask(model.TaskTypeVerify, session, intent, rk, i, 50))
		}
		delta.Add = append(delta.Add, p.newTask(model.TaskTypeReconcile, session, intent, rk, n, 40))
	case ReplanTriggerMissingPrimary:
		n := minInt(state.Evidence.MissingPrimary, 3)
		for i := 0; i < n; i++ {
			delta.Add = append(delta.Add, p.newTask(model.TaskTypeDiscover, session, intent, rk, i, 60))
		}
	case ReplanTriggerStaleSource:
		n := minInt(state.Evidence.StaleSources, 2)
		for i := 0; i < n; i++ {
			delta.Add = append(delta.Add, p.newTask(model.TaskTypeFetchHTTP, session, intent, rk, i, 55))
		}
	case ReplanTriggerCoverageGap:
		delta.Add = append(delta.Add, p.newTask(model.TaskTypeDiscover, session, intent, rk, 0, 50))
	case ReplanTriggerManual:
		delta.Add = append(delta.Add, p.newTask(model.TaskTypeDiscover, session, intent, rk, 0, 100))
	}

	if len(delta.Add) == 0 {
		delta.Add = nil
	}
	return delta
}

func (p PlanMaker) newTask(tt model.TaskType, session model.SessionID, intent model.Intent, rk string, idx, priority int) model.Task {
	return model.Task{
		ID:           model.NewTaskID(),
		SessionID:    session,
		Type:         tt,
		State:        model.TaskStateReady,
		Priority:     priority,
		SourceClass:  model.SourceClassUnknown,
		CrawlDepth:   intent.CrawlDepth,
		TaskDepth:    intent.TaskDepth,
		EstimatedCost: p.cost(tt),
		TaskKey:      fmt.Sprintf("replan:%s:%s:%d", strings.ToLower(string(tt)), rk, idx),
	}
}

func (p PlanMaker) cost(tt model.TaskType) int {
	if c, ok := p.Costs[tt]; ok {
		return c
	}
	return 1
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func replanSeedKey(state ResearchState, trigger ReplanTrigger) string {
	return fmt.Sprintf("%s:p%d:c%d:m%d:s%d", state.Session.ID, state.ReplanCount,
		state.Evidence.Contradictions, state.Evidence.MissingPrimary, state.Evidence.StaleSources)
}

func clonePlanValue(p model.Plan) model.Plan {
	out := model.Plan{Phases: make([]model.Phase, len(p.Phases))}
	for i, ph := range p.Phases {
		out.Phases[i] = model.Phase{
			Name:      ph.Name,
			Tasks:     append([]model.TaskID(nil), ph.Tasks...),
			Completed: ph.Completed,
		}
	}
	return out
}

func addToPhase(plan *model.Plan, name string, id model.TaskID) {
	for i := range plan.Phases {
		if plan.Phases[i].Name == name {
			for _, t := range plan.Phases[i].Tasks {
				if t == id {
					return
				}
			}
			plan.Phases[i].Tasks = append(plan.Phases[i].Tasks, id)
			return
		}
	}
}

func MergePlan(plan model.Plan, delta model.PlanDelta) model.Plan {
	cp := clonePlanValue(plan)
	for _, t := range delta.Add {
		addToPhase(&cp, PhaseForTaskType(t.Type), t.ID)
	}
	drop := make(map[model.TaskID]struct{}, len(delta.Drop))
	for _, id := range delta.Drop {
		drop[id] = struct{}{}
	}
	for i := range cp.Phases {
		ph := &cp.Phases[i]
		kept := ph.Tasks[:0]
		for _, tid := range ph.Tasks {
			if _, dropIt := drop[tid]; !dropIt {
				kept = append(kept, tid)
			}
		}
		ph.Tasks = kept
	}
	return cp
}
