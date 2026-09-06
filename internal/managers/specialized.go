package managers

import (
	"draw/internal/manager"
	"draw/internal/model"
)

// SpecializedManager is a stub (V1). Specialized-source retrieval is not yet
// wired; it advertises CapHTTP so the scheduler knows the capability is
// claimed, but Execute is a no-op returning EMPTY.
type SpecializedManager struct{}

func NewSpecializedManager() *SpecializedManager {
	return &SpecializedManager{}
}

func (m *SpecializedManager) Capabilities() []manager.Capability {
	return []manager.Capability{manager.CapHTTP}
}

func (m *SpecializedManager) Execute(t model.Task) (model.TaskResult, error) {
	return model.TaskResult{Status: model.RetrievalStatusEmpty}, nil
}
