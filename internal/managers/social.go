package managers

import (
	"draw/internal/manager"
	"draw/internal/model"
)

// SocialManager is a stub (E19). RSS-backed social retrieval is not yet wired;
// it advertises CapRSS so the scheduler knows the capability is claimed, but
// Execute is a no-op returning EMPTY.
type SocialManager struct{}

func NewSocialManager() *SocialManager {
	return &SocialManager{}
}

func (m *SocialManager) Capabilities() []manager.Capability {
	return []manager.Capability{manager.CapRSS}
}

func (m *SocialManager) Execute(t model.Task) (model.TaskResult, error) {
	return model.TaskResult{Status: model.RetrievalStatusEmpty}, nil
}
