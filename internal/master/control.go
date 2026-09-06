package master

import (
	"time"

	"draw/internal/config"
	"draw/internal/model"
)

type ReplanTriggerKind string

const (
	ReplanTriggerContradiction  ReplanTriggerKind = "CONTRADICTION"
	ReplanTriggerMissingPrimary ReplanTriggerKind = "MISSING_PRIMARY"
	ReplanTriggerStaleSource    ReplanTriggerKind = "STALE_SOURCE"
	ReplanTriggerManual         ReplanTriggerKind = "MANUAL"
	ReplanTriggerCoverageGap    ReplanTriggerKind = "COVERAGE_GAP"
)

type ReplanTrigger struct {
	Kind         ReplanTriggerKind
	EvidenceIDs  []model.EvidenceID
	TaskIDs      []model.TaskID
	MissingTopic string
}

type DecisionAction string

const (
	DecisionExecute         DecisionAction = "EXECUTE"
	DecisionDelay           DecisionAction = "DELAY"
	DecisionRetry           DecisionAction = "RETRY"
	DecisionUpgrade         DecisionAction = "UPGRADE"
	DecisionRediscover      DecisionAction = "REDISCOVER"
	DecisionAddVerification DecisionAction = "ADD_VERIFICATION"
	DecisionTerminate       DecisionAction = "TERMINATE"
	DecisionStop            DecisionAction = "STOP"
)

type Decision struct {
	Action      DecisionAction
	Reason      string
	UpgradeTo   *model.TaskType
	Backoff     time.Duration
	Replan      *ReplanTrigger
	DropTaskIDs []model.TaskID
}

func (d Decision) HasReplan() bool { return d.Replan != nil }

type SchedulerStats struct {
	Active             int
	Queued             int
	GlobalCap          int
	BrowserActive      int
	BrowserCap         int
	CPULoad            float64
	RAMLoad            float64
	BrowserCapConfigured int
	HeavyActive        int
	HeavyCapEffective  int
	HeavyCapConfigured int
	DomainActive       map[string]int
}

type DecisionInput struct {
	Task    model.Task
	Result  model.TaskResult
	State   ResearchState
	Retry   config.RetryPolicy
	Upgrade config.UpgradePolicy
	Replan  config.ReplanTriggers
	Stats   SchedulerStats
}
