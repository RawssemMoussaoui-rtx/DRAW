package model

import "time"

type Evidence struct {
	ID           EvidenceID
	SessionID    SessionID
	TaskID       TaskID
	SourceID     SourceID
	Topic        string
	Claim        string
	Value        string
	Confidence   float64
	Verification VerificationState
	CollectedAt  time.Time
}

type EvidenceRelation struct {
	From     EvidenceID
	To       EvidenceID
	Kind     EvidenceRelationKind
	Strength float64
}

type SourceProfile struct {
	Domain          string
	Class           SourceClass
	QualityScore    float64
	CrawlDepthLimit int
	PerDomainLimit  int
	RateLimitRPM    int
	AuthType        AuthType
	LastObservedAt  time.Time
	Provisional     bool
	Denied          bool
}
