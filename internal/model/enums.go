package model

import "time"

type IntentType string

const IntentTypeResearch IntentType = "RESEARCH"

type SourceClass string

const (
	SourceClassOfficial    SourceClass = "OFFICIAL"
	SourceClassNews        SourceClass = "NEWS"
	SourceClassSocial      SourceClass = "SOCIAL"
	SourceClassSpecialized SourceClass = "SPECIALIZED"
	SourceClassUnknown     SourceClass = "UNKNOWN"
)

type Operation string

const (
	OperationDiscover          Operation = "DISCOVER"
	OperationRetrieve          Operation = "RETRIEVE"
	OperationVerify            Operation = "VERIFY"
	OperationCompare           Operation = "COMPARE"
	OperationContradictionCheck Operation = "CONTRADICTION_CHECK"
)

type OutputType string

const OutputTypeReport OutputType = "REPORT"

type AuthType string

const (
	AuthTypeNone           AuthType = "NONE"
	AuthTypeUserAuthorized AuthType = "USER_AUTHORIZED"
)

type TaskType string

const (
	TaskTypeDiscover     TaskType = "DISCOVER"
	TaskTypeFetchHTTP    TaskType = "FETCH_HTTP"
	TaskTypeFetchBrowser TaskType = "FETCH_BROWSER"
	TaskTypeVerify       TaskType = "VERIFY"
	TaskTypeReconcile    TaskType = "RECONCILE"
)

var TaskTypes = map[string]TaskType{
	string(TaskTypeDiscover):     TaskTypeDiscover,
	string(TaskTypeFetchHTTP):    TaskTypeFetchHTTP,
	string(TaskTypeFetchBrowser): TaskTypeFetchBrowser,
	string(TaskTypeVerify):       TaskTypeVerify,
	string(TaskTypeReconcile):    TaskTypeReconcile,
}

type TaskState string

const (
	TaskStateReady       TaskState = "READY"
	TaskStateRunning     TaskState = "RUNNING"
	TaskStateRetryWait   TaskState = "RETRY_WAIT"
	TaskStateCompleted   TaskState = "COMPLETED"
	TaskStateFailed      TaskState = "FAILED"
	TaskStateBlocked     TaskState = "BLOCKED"
	TaskStateCancelled   TaskState = "CANCELLED"
	TaskStateExpired     TaskState = "EXPIRED"
)

type RetrievalStatus string

const (
	RetrievalStatusSuccess           RetrievalStatus = "SUCCESS"
	RetrievalStatusPartial           RetrievalStatus = "PARTIAL"
	RetrievalStatusEmpty             RetrievalStatus = "EMPTY"
	RetrievalStatusJavascriptRequired RetrievalStatus = "JAVASCRIPT_REQUIRED"
	RetrievalStatusAuthRequired      RetrievalStatus = "AUTH_REQUIRED"
	RetrievalStatusTimeout           RetrievalStatus = "TIMEOUT"
	RetrievalStatusBlocked           RetrievalStatus = "BLOCKED"
	RetrievalStatusInvalidContent    RetrievalStatus = "INVALID_CONTENT"
)

type ErrorWeight string

const (
	ErrorWeightLow      ErrorWeight = "LOW"
	ErrorWeightModerate ErrorWeight = "MODERATE"
	ErrorWeightHigh     ErrorWeight = "HIGH"
	ErrorWeightBlocking ErrorWeight = "BLOCKING"
)

type EvidenceRelationKind string

const (
	EvidenceRelationSupports   EvidenceRelationKind = "SUPPORTS"
	EvidenceRelationContradicts EvidenceRelationKind = "CONTRADICTS"
	EvidenceRelationDuplicates  EvidenceRelationKind = "DUPLICATES"
	EvidenceRelationDerivedFrom EvidenceRelationKind = "DERIVED_FROM"
)

type VerificationState string

const (
	VerificationUnverified        VerificationState = "UNVERIFIED"
	VerificationPartiallyVerified VerificationState = "PARTIALLY_VERIFIED"
	VerificationVerified          VerificationState = "VERIFIED"
	VerificationDisputed          VerificationState = "DISPUTED"
	VerificationUnresolved        VerificationState = "UNRESOLVED"
)

type SessionState string

const (
	SessionStateActive   SessionState = "ACTIVE"
	SessionStateArchived SessionState = "ARCHIVED"
	SessionStateDeleted  SessionState = "DELETED"
	SessionStateExpired  SessionState = "EXPIRED"
)

type TimeWindow struct {
	From time.Time
	To   time.Time
}

func DefaultCostModel() map[TaskType]int {
	return map[TaskType]int{
		TaskTypeDiscover:     1,
		TaskTypeFetchHTTP:    1,
		TaskTypeFetchBrowser: 10,
		TaskTypeVerify:       1,
		TaskTypeReconcile:    3,
	}
}
