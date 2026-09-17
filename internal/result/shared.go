package result

import (
	"sort"
	"time"

	"draw/internal/master"
	"draw/internal/model"
	"draw/internal/storage"
)

type findingKey struct{ topic, claim string }

// groupedEvidenceByFindingKey groups evidence by (topic, claim) and returns
// the group keys sorted by topic then claim for deterministic iteration.
func groupedEvidenceByFindingKey(evs []model.Evidence) ([]findingKey, map[findingKey][]model.Evidence) {
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
	return keys, groups
}

// ConsensusWinner determines the consensus Value and winning Evidence for a
// group of evidence sharing the same (topic, claim). It uses the EXACT same
// algorithm as the historical buildFindings:
//
//  1. For each distinct Value, select the best evidence (highest Confidence,
//     then lowest EvidenceID as tiebreak).
//  2. Among those per-Value representatives, select the overall winner
//     (highest Confidence, then lowest EvidenceID).
//
// Returns the consensus Value and the winning Evidence. If items is empty,
// returns an empty consensus value and a zero Evidence.
func ConsensusWinner(items []model.Evidence) (consensusValue string, winner model.Evidence) {
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

	first := true
	for _, v := range values {
		cand := best[v]
		if first {
			consensusValue = v
			winner = cand
			first = false
			continue
		}
		if cand.Confidence > winner.Confidence {
			consensusValue = v
			winner = cand
		} else if cand.Confidence == winner.Confidence && cand.ID < winner.ID {
			consensusValue = v
			winner = cand
		}
	}
	return consensusValue, winner
}

// ComputeExclusionReasons maps each EvidenceID to its exclusion reason.
// Evidence whose Value wins consensus for its (topic, claim) group is mapped
// to nil (not excluded). Evidence whose Value loses consensus is mapped to
// a pointer to ExclusionReasonNonConsensusValue ("NON_CONSENSUS_VALUE").
func ComputeExclusionReasons(evs []model.Evidence) map[model.EvidenceID]*model.ExclusionReason {
	_, groups := groupedEvidenceByFindingKey(evs)

	result := make(map[model.EvidenceID]*model.ExclusionReason, len(evs))
	for _, items := range groups {
		consensusValue, _ := ConsensusWinner(items)
		for _, ev := range items {
			if ev.Value == consensusValue {
				result[ev.ID] = nil
			} else {
				reason := model.ExclusionReasonNonConsensusValue
				result[ev.ID] = &reason
			}
		}
	}
	return result
}

// WithExclusionReasons returns a copy of evs with each Evidence's ExclusionReason
// field populated based on consensus selection within its (topic, claim) group.
func WithExclusionReasons(evs []model.Evidence) []model.Evidence {
	reasons := ComputeExclusionReasons(evs)
	out := make([]model.Evidence, len(evs))
	for i, ev := range evs {
		out[i] = ev
		out[i].ExclusionReason = reasons[ev.ID]
	}
	return out
}

// distinctExclusionReasons returns the sorted list of distinct non-nil
// exclusion reason values present in evs.
func distinctExclusionReasons(evs []model.Evidence) []string {
	seen := map[string]bool{}
	for _, ev := range evs {
		if ev.ExclusionReason != nil {
			seen[string(*ev.ExclusionReason)] = true
		}
	}
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// QueryEvidence retrieves all evidence for the session in rs from es.
func QueryEvidence(rs master.ResearchState, es storage.EvidenceStore) []model.Evidence {
	if rs.Session == nil || es == nil {
		return nil
	}
	return es.Query(storage.EvidenceFilter{SessionID: rs.Session.ID})
}

// BuildSources assembles the deduplicated, sorted source list for the given
// evidence, enriching with profile information when available.
func BuildSources(evs []model.Evidence, src storage.SourceRegistry) []model.ReportSource {
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
		if src != nil {
			if profile, ok := src.Lookup(d); ok && profile != nil {
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

// BuildContradictions extracts contradiction relations from the evidence store
// and returns them as ContradictionView entries, sorted deterministically.
func BuildContradictions(evs []model.Evidence, es storage.EvidenceStore) []model.ContradictionView {
	if es == nil {
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
		rels := es.FindRelations(topic)
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

// RelationSummary is a single relation between two evidence items.
type RelationSummary struct {
	From     model.EvidenceID           `json:"from"`
	To       model.EvidenceID           `json:"to"`
	Kind     model.EvidenceRelationKind `json:"kind"`
	Strength float64                    `json:"strength"`
}

// collectRelations gathers all relations across all topics in evs, deduplicating
// by (from, to, kind) and sorting deterministically.
func collectRelations(evs []model.Evidence, es storage.EvidenceStore) []RelationSummary {
	if es == nil {
		return nil
	}

	topics := map[string]bool{}
	for _, ev := range evs {
		if ev.Topic != "" {
			topics[ev.Topic] = true
		}
	}

	seen := map[string]bool{}
	var out []RelationSummary
	for topic := range topics {
		rels := es.FindRelations(topic)
		for _, rel := range rels {
			key := string(rel.From) + "|" + string(rel.To) + "|" + string(rel.Kind)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, RelationSummary{
				From:     rel.From,
				To:       rel.To,
				Kind:     rel.Kind,
				Strength: rel.Strength,
			})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		if out[i].To != out[j].To {
			return out[i].To < out[j].To
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// EvidenceState is the assembled state returned to the v2agent for GET /state.
type EvidenceState struct {
	SessionID        model.SessionID           `json:"session_id"`
	Status           string                    `json:"status"`
	BudgetUsed       int                       `json:"budget_used"`
	BudgetTotal      int                       `json:"budget_total"`
	Progress         float64                   `json:"progress"`
	UpdatedAt        time.Time                 `json:"updated_at"`
	Evidence         []model.Evidence          `json:"evidence"`
	ExclusionReasons []string                  `json:"exclusion_reasons"`
	Sources          []model.ReportSource      `json:"sources"`
	Relations        []RelationSummary         `json:"relations"`
	Contradictions   []model.ContradictionView `json:"contradictions"`
}

// sessionStatus returns the terminal reason if the research is terminal,
// otherwise "active".
func sessionStatus(rs master.ResearchState) string {
	if rs.Terminal {
		return rs.TerminalReason
	}
	return "active"
}

// AssembleState assembles the EvidenceState for GET /state from the Master's
// ResearchState. It queries evidence, computes exclusion reasons, builds
// sources, collects relations, and computes contradictions.
func AssembleState(rs master.ResearchState, es storage.EvidenceStore, src storage.SourceRegistry) EvidenceState {
	state := EvidenceState{
		SessionID:   sessionID(rs),
		Status:      sessionStatus(rs),
		BudgetUsed:  rs.BudgetUsed,
		BudgetTotal: rs.BudgetTotal,
		Progress:    buildCompleteness(rs),
		UpdatedAt:   rs.UpdatedAt,
	}

	evs := QueryEvidence(rs, es)
	state.Evidence = WithExclusionReasons(evs)
	state.ExclusionReasons = distinctExclusionReasons(state.Evidence)
	state.Sources = BuildSources(evs, src)
	state.Relations = collectRelations(evs, es)
	state.Contradictions = BuildContradictions(evs, es)

	return state
}

// sessionID returns the session ID from rs, or empty string if nil.
func sessionID(rs master.ResearchState) model.SessionID {
	if rs.Session == nil {
		return ""
	}
	return rs.Session.ID
}
