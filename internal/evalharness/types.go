package evalharness

import (
	"database/sql"

	"draw/internal/model"
	"draw/internal/storage"
)

type SemanticStatus string

const (
	StatusObserved SemanticStatus = "Observed"
	StatusDerived  SemanticStatus = "Derived"
	StatusInferred SemanticStatus = "Inferred"
	StatusUnknown  SemanticStatus = "Unknown"
)

type MetricResult struct {
	Name   string
	Value  string
	Status SemanticStatus
}

type ScenarioID string

type SourceSpec struct {
	Domain     string
	Claim      string
	Value      string
	Confidence float64
	Quality    float64
	TaskID     model.TaskID
}

type GroundTruthEdge struct {
	A           string
	B           string
	Relation    string
	Independent bool
}

type Expectation struct {
	IndependentGroups int
	Contradiction     bool
	SaturationWindow  int
	ParaphrasePairs   int
}

type Scenario struct {
	ID       ScenarioID
	Name     string
	Topic    string
	Sources  []SourceSpec
	Topology []GroundTruthEdge
	Quality  map[string]float64
	Expect   Expectation
}

type ObservedState struct {
	Evidence      []model.Evidence
	Edges         []model.EvidenceRelation
	Events        []storage.Event
	Verifications map[model.EvidenceID]model.VerificationState
}

type ScenarioResult struct {
	ScenarioID ScenarioID
	Metrics    []MetricResult
	// Perf holds per-scenario performance/resource measurements. It lives
	// outside the M1-M7 metric set so the semantic report section is unchanged.
	Perf PerfMetrics
}

type testStore struct {
	db         *sql.DB
	ES         *storage.SQLiteEvidenceStore
	EVS        *storage.SQLiteEventStore
	sid        model.SessionID
	tid        model.TaskID
	queryCount *int64 // live DB query counter from the counting driver (nil if not counting)
	perf       PerfMetrics
}
