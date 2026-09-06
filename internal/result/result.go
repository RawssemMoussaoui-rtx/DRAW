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

	env.Payload.Sources = b.buildSources(evs)
	env.Payload.Findings = buildFindings(evs)
	env.Payload.Contradictions = b.buildContradictions(evs)
	env.Payload.Completeness = buildCompleteness(rs)
	env.Payload.Confidence = buildConfidence(evs)

	return env
}

func (b *ReportBuilder) queryEvidence(rs master.ResearchState) []model.Evidence {
	if rs.Session == nil || b.es == nil {
		return nil
	}
	return b.es.Query(storage.EvidenceFilter{SessionID: rs.Session.ID})
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
	seen := map[string]bool{}
	var domains []string
	for _, ev := range evs {
		d := string(ev.SourceID)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		domains = append(domains, d)
	}
	sort.Strings(domains)

	out := make([]model.ReportSource, 0, len(domains))
	for _, d := range domains {
		if b.src != nil {
			if profile, ok := b.src.Lookup(d); ok && profile != nil {
				out = append(out, model.ReportSource{
					Name:    profile.Domain,
					URL:     "https://" + d,
					Class:   profile.Class,
					Quality: profile.QualityScore,
				})
				continue
			}
		}
		out = append(out, model.ReportSource{
			Name:    d,
			URL:     "https://" + d,
			Class:   model.SourceClassUnknown,
			Quality: 0.3,
		})
	}
	return out
}

func buildFindings(evs []model.Evidence) []model.Finding {
	type findingKey struct{ topic, claim string }
	groups := map[findingKey][]model.Evidence{}
	for _, ev := range evs {
		k := findingKey{topic: ev.Topic, claim: ev.Claim}
		groups[k] = append(groups[k], ev)
	}

	keys := make([]findingKey, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].topic != keys[j].topic {
			return keys[i].topic < keys[j].topic
		}
		return keys[i].claim < keys[j].claim
	})

	findings := make([]model.Finding, 0, len(keys))
	for _, k := range keys {
		items := groups[k]

		best := map[string]model.Evidence{}
		for _, ev := range items {
			cur, ok := best[ev.Value]
			if !ok {
				best[ev.Value] = ev
				continue
			}
			if ev.Confidence > cur.Confidence {
				best[ev.Value] = ev
			} else if ev.Confidence == cur.Confidence && ev.ID < cur.ID {
				best[ev.Value] = ev
			}
		}

		values := make([]string, 0, len(best))
		for v := range best {
			values = append(values, v)
		}
		sort.Strings(values)

		var consensusValue string
		var consensus model.Evidence
		first := true
		for _, v := range values {
			cand := best[v]
			if first {
				consensusValue = v
				consensus = cand
				first = false
				continue
			}
			if cand.Confidence > consensus.Confidence {
				consensusValue = v
				consensus = cand
			} else if cand.Confidence == consensus.Confidence && cand.ID < consensus.ID {
				consensusValue = v
				consensus = cand
			}
		}

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
	if b.es == nil {
		return nil
	}

	evMap := map[model.EvidenceID]model.Evidence{}
	topics := map[string]bool{}
	for _, ev := range evs {
		evMap[ev.ID] = ev
		if ev.Topic != "" {
			topics[ev.Topic] = true
		}
	}

	type view struct {
		claimA, claimB string
		from, to       model.EvidenceID
		strength       float64
	}
	var views []view
	seen := map[string]bool{}
	for topic := range topics {
		rels := b.es.FindRelations(topic)
		for _, rel := range rels {
			if rel.Kind != model.EvidenceRelationContradicts {
				continue
			}
			key := string(rel.From) + "|" + string(rel.To)
			if seen[key] {
				continue
			}
			seen[key] = true

			claimA, claimB := "", ""
			if ev, ok := evMap[rel.From]; ok {
				claimA = ev.Claim
			}
			if ev, ok := evMap[rel.To]; ok {
				claimB = ev.Claim
			}
			views = append(views, view{
				claimA:   claimA,
				claimB:   claimB,
				from:     rel.From,
				to:       rel.To,
				strength: rel.Strength,
			})
		}
	}

	sort.Slice(views, func(i, j int) bool {
		if views[i].claimA != views[j].claimA {
			return views[i].claimA < views[j].claimA
		}
		if views[i].claimB != views[j].claimB {
			return views[i].claimB < views[j].claimB
		}
		return views[i].from < views[j].from
	})

	out := make([]model.ContradictionView, 0, len(views))
	for _, v := range views {
		out = append(out, model.ContradictionView{
			ClaimA:      v.claimA,
			ClaimB:      v.claimB,
			EvidenceIDA: v.from,
			EvidenceIDB: v.to,
			Strength:    v.strength,
		})
	}
	return out
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
