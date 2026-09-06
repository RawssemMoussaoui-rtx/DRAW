package model

type Plan struct {
	Phases []Phase
}

type Phase struct {
	Name      string
	Tasks     []TaskID
	Completed bool
}

type PlanDelta struct {
	Add              []Task
	Drop             []TaskID
	AdjustPriority   map[TaskID]int
}
