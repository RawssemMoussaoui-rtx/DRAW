package managers

import (
	"draw/internal/manager"
	"draw/internal/model"
)

type NewsManager struct {
	worker manager.Worker
}

func NewNewsManager(w manager.Worker) *NewsManager {
	return &NewsManager{worker: w}
}

func (m *NewsManager) Capabilities() []manager.Capability {
	return []manager.Capability{manager.CapHTTP}
}

func (m *NewsManager) Execute(t model.Task) (model.TaskResult, error) {
	return runHTTP(m.worker, t, defaultWebTimeout)
}
