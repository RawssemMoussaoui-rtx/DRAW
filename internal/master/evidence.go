package master

import (
	"context"

	"draw/internal/evidence"
	"draw/internal/model"
	"draw/internal/storage"
)

func WithEvidenceStore(es storage.EvidenceStore) MasterOption {
	return func(m *Master) {
		m.es = es
		m.evidence = &storeEvidenceReader{es: es}
	}
}

func WithSourceRegistry(reg storage.SourceRegistry) MasterOption {
	return func(m *Master) { m.reg = reg }
}

type storeEvidenceReader struct {
	es storage.EvidenceStore
}

func (r *storeEvidenceReader) Counts(sid model.SessionID) EvidenceCounts {
	c := EvidenceCounts{}
	queries := []model.VerificationState{
		model.VerificationDisputed,
		model.VerificationUnverified,
		model.VerificationPartiallyVerified,
	}
	for _, v := range queries {
		evs := r.es.Query(storage.EvidenceFilter{
			SessionID:    sid,
			Verification: v,
		})
		switch v {
		case model.VerificationDisputed:
			c.Contradictions = len(evs)
		case model.VerificationUnverified:
			c.MissingPrimary = len(evs)
		case model.VerificationPartiallyVerified:
			c.StaleSources = len(evs)
		}
	}
	return c
}

func (m *Master) extractAndStoreEvidence(ctx context.Context, t model.Task, r *model.TaskResult) {
	if m.es == nil {
		return
	}

	topics := m.resolvedTopics()
	items := evidence.Extract(t, *r, topics)
	if len(items) == 0 {
		return
	}

	m.upsertSourceProfiles(items)

	for i := range items {
		id, err := m.es.Put(items[i])
		if err != nil {
			continue
		}
		items[i].ID = id
		r.Evidence = append(r.Evidence, id)
	}

	existing := m.es.Query(storage.EvidenceFilter{SessionID: t.SessionID})

	newRels := evidence.ComputeRelations(items, existing)
	for _, rel := range newRels {
		_ = m.es.PutRelation(rel)
	}

	m.recomputeVerification(items, existing)
}

func (m *Master) resolvedTopics() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == nil || m.state.Session == nil {
		return []string{"research"}
	}
	intent := m.state.Session.Intent
	if len(intent.Topics) > 0 {
		return append([]string(nil), intent.Topics...)
	}
	if intent.Entity != "" {
		return []string{intent.Entity}
	}
	return []string{"research"}
}

func (m *Master) upsertSourceProfiles(items []model.Evidence) {
	if m.reg == nil {
		return
	}
	seen := map[string]bool{}
	for i := range items {
		domain := string(items[i].SourceID)
		if domain == "" || seen[domain] {
			continue
		}
		seen[domain] = true
		_ = m.reg.ProvisionalUpsert(model.SourceProfile{
			Domain:          domain,
			Class:           model.SourceClassUnknown,
			QualityScore:    0.3,
			CrawlDepthLimit: 3,
			PerDomainLimit:  5,
			RateLimitRPM:    60,
			AuthType:        model.AuthTypeNone,
			Provisional:     true,
		})
	}
}

func (m *Master) recomputeVerification(newItems, existing []model.Evidence) {
	allItems := append(append([]model.Evidence{}, newItems...), existing...)
	qualities := make(map[model.EvidenceID]float64, len(allItems))
	for _, ev := range allItems {
		q := 0.3
		if m.reg != nil {
			if p, ok := m.reg.Lookup(string(ev.SourceID)); ok && p != nil {
				q = p.QualityScore
			}
		}
		qualities[ev.ID] = q
	}

	topics := map[string]bool{}
	for _, ev := range newItems {
		if ev.Topic != "" {
			topics[ev.Topic] = true
		}
	}

	for topic := range topics {
		rels := m.es.FindRelations(topic)
		var affected []model.Evidence
		for _, ev := range allItems {
			if ev.Topic == topic {
				affected = append(affected, ev)
			}
		}
		for i := range affected {
			newState := evidence.ComputeVerification(
				affected[i], rels,
				evidence.HighQualityThreshold,
				evidence.KContradictions,
				qualities,
			)
			if newState != affected[i].Verification {
				affected[i].Verification = newState
				_, _ = m.es.Put(affected[i])
			}
		}
	}
}
