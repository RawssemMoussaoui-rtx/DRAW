package api

import (
	"context"

	"draw/internal/auth"
	"draw/internal/master"
	"draw/internal/model"
	"draw/internal/result"
	"draw/internal/storage"
)

// MasterAPI is the narrow Master surface the API layer depends on.
// *master.Master satisfies it (SubmitIntent/Run/State/Stop).
type MasterAPI interface {
	SubmitIntent(model.IntentRequest) (model.SessionID, error)
	Run(context.Context) error
	State() master.ResearchState
	Stop() error
}

// Deps wires the API server to the engine seams.
type Deps struct {
	Master         MasterAPI
	EvidenceStore  storage.EvidenceStore
	SourceRegistry storage.SourceRegistry
	EventStore     storage.EventStore
	SessionStore   storage.SessionStore
	ReportBuilder  *result.ReportBuilder
	Auth           auth.Config
}
