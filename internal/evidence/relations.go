package evidence

import (
	"sort"

	"draw/internal/model"
)

// ComputeRelations compares newly-extracted evidence items against existing
// evidence items and produces deterministic EvidenceRelation edges.
//
// Rules (per pair n ∈ newEvidence, e ∈ existingEvidence):
//   - Same Topic required, else skip.
//   - Same Claim + same Value:
//   -   same SourceID  → DUPLICATES (Strength=1.0)
//   -   diff SourceID  → SUPPORTS (Strength = min(n.Confidence, e.Confidence), clamped [0,1])
//   - Same Claim + different Value → CONTRADICTS (Strength=1.0)
//   - Different Claim → no relation (skip)
//
// Each relation is emitted bidirectionally (n→e and e→n) so that both the
// new and existing evidence item can be verified in the same pass.
// Output is sorted by (From, To, Kind) and de-duplicated by From+To+Kind.
func ComputeRelations(newEvidence, existingEvidence []model.Evidence) []model.EvidenceRelation {
	seen := make(map[string]bool)
	var relations []model.EvidenceRelation

	for _, n := range newEvidence {
		for _, e := range existingEvidence {
			if n.ID == e.ID {
				continue
			}
			if n.Topic != e.Topic {
				continue
			}
			if n.Claim != e.Claim {
				continue
			}

			var kind model.EvidenceRelationKind
			var strength float64

			if n.Value == e.Value {
				if n.SourceID == e.SourceID {
					kind = model.EvidenceRelationDuplicates
					strength = 1.0
				} else {
					kind = model.EvidenceRelationSupports
					strength = clampFloat(min(n.Confidence, e.Confidence), 0.0, 1.0)
				}
			} else {
				// Paraphrase gate: when the differing Values are highly
				// similar (same fact, different wording — a paraphrase
				// suppressed by the acceptance gate), suppress the CONTRADICTS
				// edge entirely. This is a
				// pure abstention — no edge is emitted and no new relation Kind is
				// introduced (no enum or migration change). Genuine contradictions
				// (Scenario C) fall through with overlap < threshold and emit
				// CONTRADICTS as before. See Stage 2 acceptance-gate protocol
				// (tau = contradictionSimilarityThreshold).
				if ValueSimilarity(n.Value, e.Value) >= contradictionSimilarityThreshold {
					continue
				}
				kind = model.EvidenceRelationContradicts
				strength = 1.0
			}

			emitRelation(&relations, &seen, n.ID, e.ID, kind, strength)
			emitRelation(&relations, &seen, e.ID, n.ID, kind, strength)
		}
	}

	sort.Slice(relations, func(i, j int) bool {
		if relations[i].From != relations[j].From {
			return relations[i].From < relations[j].From
		}
		if relations[i].To != relations[j].To {
			return relations[i].To < relations[j].To
		}
		return relations[i].Kind < relations[j].Kind
	})

	return relations
}

func emitRelation(relations *[]model.EvidenceRelation, seen *map[string]bool, from, to model.EvidenceID, kind model.EvidenceRelationKind, strength float64) {
	key := string(from) + "|" + string(to) + "|" + string(kind)
	if (*seen)[key] {
		return
	}
	(*seen)[key] = true
	*relations = append(*relations, model.EvidenceRelation{
		From:     from,
		To:       to,
		Kind:     kind,
		Strength: strength,
	})
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
