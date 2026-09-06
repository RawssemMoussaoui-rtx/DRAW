package master

import (
	"time"

	"draw/internal/config"
	"draw/internal/model"
)

const (
	PhaseDiscovery              = "discovery"
	PhasePrimaryRetrieval       = "primary_retrieval"
	PhaseSecondaryRetrieval     = "secondary_retrieval"
	PhaseCrossSourceComparison  = "cross_source_comparison"
	PhaseContradictionDetection = "contradiction_detection"
	PhaseAdditionalVerification = "additional_verification"
	PhaseEvidenceConsolidation  = "evidence_consolidation"
	PhaseFinalization           = "finalization"
)

var PlanPhaseOrder = []string{
	PhaseDiscovery,
	PhasePrimaryRetrieval,
	PhaseSecondaryRetrieval,
	PhaseCrossSourceComparison,
	PhaseContradictionDetection,
	PhaseAdditionalVerification,
	PhaseEvidenceConsolidation,
	PhaseFinalization,
}

type EvidenceCounts struct {
	Contradictions int
	MissingPrimary int
	StaleSources   int
}

type EvidenceReader interface {
	Counts(model.SessionID) EvidenceCounts
}

type noopEvidenceReader struct{}

func (noopEvidenceReader) Counts(model.SessionID) EvidenceCounts { return EvidenceCounts{} }

func NoopEvidenceReader() EvidenceReader { return noopEvidenceReader{} }

type ResearchState struct {
	Session          *model.Session
	Plan             *model.Plan
	BudgetTotal      int
	BudgetUsed       int
	EvidenceCount    int
	ReplanCount      int
	MaxReplans       int
	PhaseCompleted   map[string]bool
	PhaseIndex       int
	Terminal         bool
	TerminalReason   string
	Evidence         EvidenceCounts
	UpdatedAt        time.Time
	setTerminalCount int
}

func NewResearchState(session *model.Session, plan *model.Plan, cfg config.SchedulerConfig) *ResearchState {
	if session == nil || plan == nil {
		panic("master: ResearchState requires non-nil session and plan")
	}
	total := session.Intent.EffortBudget
	if total <= 0 {
		total = 500
	}
	maxReplans := session.Intent.MaxReplans
	if maxReplans <= 0 {
		maxReplans = 3
	}
	return &ResearchState{
		Session:        session,
		Plan:           plan,
		BudgetTotal:    total,
		MaxReplans:     maxReplans,
		PhaseCompleted: make(map[string]bool),
	}
}

func (s *ResearchState) Clone() ResearchState {
	cp := ResearchState{
		Session:          s.Session,
		Plan:             s.Plan,
		BudgetTotal:      s.BudgetTotal,
		BudgetUsed:       s.BudgetUsed,
		EvidenceCount:    s.EvidenceCount,
		ReplanCount:      s.ReplanCount,
		MaxReplans:       s.MaxReplans,
		PhaseCompleted:   make(map[string]bool, len(s.PhaseCompleted)),
		PhaseIndex:       s.PhaseIndex,
		Terminal:         s.Terminal,
		TerminalReason:   s.TerminalReason,
		Evidence:         s.Evidence,
		UpdatedAt:        s.UpdatedAt,
		setTerminalCount: s.setTerminalCount,
	}
	for k, v := range s.PhaseCompleted {
		cp.PhaseCompleted[k] = v
	}
	if s.Plan != nil {
		p := clonePlan(s.Plan)
		cp.Plan = &p
	}
	return cp
}

func clonePlan(p *model.Plan) model.Plan {
	cp := model.Plan{Phases: make([]model.Phase, len(p.Phases))}
	for i, ph := range p.Phases {
		cp.Phases[i] = model.Phase{
			Name:      ph.Name,
			Tasks:     append([]model.TaskID(nil), ph.Tasks...),
			Completed: ph.Completed,
		}
	}
	return cp
}

func (s *ResearchState) Charge(cost int) bool {
	if s.BudgetUsed+cost > s.BudgetTotal {
		return false
	}
	s.BudgetUsed += cost
	s.UpdatedAt = time.Now().UTC()
	return true
}

func (s *ResearchState) AddEvidence(n int) {
	s.EvidenceCount += n
	s.UpdatedAt = time.Now().UTC()
}

func (s *ResearchState) RefreshEvidenceCounts(rid EvidenceReader) {
	if s.Session == nil {
		return
	}
	s.Evidence = rid.Counts(s.Session.ID)
	s.UpdatedAt = time.Now().UTC()
}

func (s *ResearchState) SetTerminal(reason string) {
	if !s.Terminal || reason != s.TerminalReason {
		s.setTerminalCount++
	}
	s.Terminal = true
	s.TerminalReason = reason
	s.UpdatedAt = time.Now().UTC()
}

func (s *ResearchState) MarkPhaseCompleted(name string) {
	s.PhaseCompleted[name] = true
	if idx := indexOfPhase(name); idx >= 0 {
		if idx >= s.PhaseIndex {
			s.PhaseIndex = idx + 1
		}
	}
	s.UpdatedAt = time.Now().UTC()
}

func (s *ResearchState) CanReplan() bool {
	return s.ReplanCount < s.MaxReplans
}

func (s *ResearchState) RecordReplan() {
	s.ReplanCount++
	s.UpdatedAt = time.Now().UTC()
}

func indexOfPhase(name string) int {
	for i, p := range PlanPhaseOrder {
		if p == name {
			return i
		}
	}
	return -1
}

func BudgetExhausted(s ResearchState) bool {
	return s.BudgetUsed >= s.BudgetTotal
}

func PhaseForTaskType(tt model.TaskType) string {
	switch tt {
	case model.TaskTypeDiscover:
		return PhaseDiscovery
	case model.TaskTypeFetchHTTP, model.TaskTypeFetchBrowser:
		return PhasePrimaryRetrieval
	case model.TaskTypeVerify:
		return PhaseAdditionalVerification
	case model.TaskTypeReconcile:
		return PhaseEvidenceConsolidation
	default:
		return PhaseDiscovery
	}
}
