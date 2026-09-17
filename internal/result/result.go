package result

import (
	"sort"
	"time"

	"draw/internal/master"
	"draw/internal/model"
	"draw/internal/storage"
)

type ReportBuilder struct {
	es  storage.EvidenceStore
	src storage.SourceRegistry
}

func NewReportBuilder(es storage.EvidenceStore, src storage.SourceRegistry) *ReportBuilder {
	return &ReportBuilder{es: es, src: src}
}

func (b *ReportBuilder) Build(rs master.ResearchState) model.ResultEnvelope {
	env := model.ResultEnvelope{
		Status:      rs.TerminalReason,
		BudgetUsed:  rs.BudgetUsed,
		CompletedAt: time.Now(),
	}

	if rs.Session != nil {
		env.SessionID = rs.Session.ID
		env.Payload.Intent = rs.Session.Intent
		env.Payload.Entities = []string{rs.Session.Intent.Entity}
		if rs.Session.Intent.TimeRange != nil {
			env.Payload.TimeRange = *rs.Session.Intent.TimeRange
		}
		env.PlanPhases = buildPlanPhases(rs)
	}

	evs := b.queryEvidence(rs)

	env.Payload.Sources = BuildSources(evs, b.src)
	env.Payload.Findings = buildFindings(evs)
	env.Payload.Contradictions = BuildContradictions(evs, b.es)
	env.Payload.Completeness = buildCompleteness(rs)
	env.Payload.Confidence = buildConfidence(evs)

	return env
}

func (b *ReportBuilder) queryEvidence(rs master.ResearchState) []model.Evidence {
	return QueryEvidence(rs, b.es)
}

func buildPlanPhases(rs master.ResearchState) []model.Phase {
	if rs.Session == nil || rs.Session.Plan.Phases == nil {
		return nil
	}
	src := rs.Session.Plan.Phases
	out := make([]model.Phase, len(src))
	for i, ph := range src {
		out[i] = ph
		out[i].Completed = rs.PhaseCompleted[ph.Name]
	}
	return out
}

func (b *ReportBuilder) buildSources(evs []model.Evidence) []model.ReportSource {
	return BuildSources(evs, b.src)
}

func buildFindings(evs []model.Evidence) []model.Finding {
	keys, groups := groupedEvidenceByFindingKey(evs)

	findings := make([]model.Finding, 0, len(keys))
	for _, k := range keys {
		items := groups[k]

		consensusValue, consensus := ConsensusWinner(items)

		var ids []model.EvidenceID
		for _, ev := range items {
			if ev.Value == consensusValue {
				ids = append(ids, ev.ID)
			}
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

		findings = append(findings, model.Finding{
			Claim:       k.claim,
			Value:       consensusValue,
			EvidenceIDs: ids,
			Status:      string(consensus.Verification),
		})
	}
	return findings
}

func (b *ReportBuilder) buildContradictions(evs []model.Evidence) []model.ContradictionView {
	return BuildContradictions(evs, b.es)
}

func buildCompleteness(rs master.ResearchState) float64 {
	if rs.Session == nil || len(rs.Session.Plan.Phases) == 0 {
		return 0.0
	}
	completed := 0
	for _, ph := range rs.Session.Plan.Phases {
		if rs.PhaseCompleted[ph.Name] {
			completed++
		}
	}
	return float64(completed) / float64(len(rs.Session.Plan.Phases))
}

func buildConfidence(evs []model.Evidence) float64 {
	if len(evs) == 0 {
		return 0.0
	}
	verified := 0
	for _, ev := range evs {
		if ev.Verification == model.VerificationVerified {
			verified++
		}
	}
	return float64(verified) / float64(len(evs))
}
