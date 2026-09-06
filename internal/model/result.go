package model

import "time"

type Session struct {
	ID            SessionID
	UserID        string
	State         SessionState
	Intent        Intent
	Plan          Plan
	CreatedAt     time.Time
	RecoveryToken string
}

type ResultEnvelope struct {
	Status      string
	SessionID   SessionID
	CompletedAt time.Time
	BudgetUsed  int
	PlanPhases  []Phase
	Payload     Report
	Errors      []error
}

type Report struct {
	Intent         Intent
	Entities       []string
	TimeRange      TimeWindow
	Sources        []ReportSource
	Findings       []Finding
	Contradictions []ContradictionView
	Completeness   float64
	Confidence     float64
}

type ReportSource struct {
	Name    string
	URL     string
	Class   SourceClass
	Quality float64
}

type Finding struct {
	Claim       string
	Value       string
	EvidenceIDs []EvidenceID
	Status      string
}

type ContradictionView struct {
	ClaimA      string
	ClaimB      string
	EvidenceIDA EvidenceID
	EvidenceIDB EvidenceID
	Strength    float64
}
