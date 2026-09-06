package evidence

import "draw/internal/model"

// HighQualityThreshold is the default QualityScore at or above which a source is
// considered "high-quality" for VERIFIED state transitions. Per Phase-F decision:
// configurable default 0.7. Unknown/provisional domains (0.3 baseline) do NOT qualify.
const HighQualityThreshold float64 = 0.7

// KContradictions is the threshold for DISPUTED state and replan trigger.
// Per spec §0.8.3 and §0.9: K=2. This mirrors config.DefaultReplan().KContradictions.
const KContradictions int = 2

// ContradictionCount returns the number of CONTRADICTS edges incident to ev
// (edges where ev is either the From or the To endpoint).
func ContradictionCount(ev model.Evidence, allRelations []model.EvidenceRelation) int {
	count := 0
	for _, rel := range allRelations {
		if rel.Kind != model.EvidenceRelationContradicts {
			continue
		}
		if rel.From == ev.ID || rel.To == ev.ID {
			count++
		}
	}
	return count
}

// SupportsCount returns the number of SUPPORTS edges incident to ev
// (edges where ev is either the From or the To endpoint).
func SupportsCount(ev model.Evidence, allRelations []model.EvidenceRelation) int {
	count := 0
	for _, rel := range allRelations {
		if rel.Kind != model.EvidenceRelationSupports {
			continue
		}
		if rel.From == ev.ID || rel.To == ev.ID {
			count++
		}
	}
	return count
}

// HasHighQualitySupport checks whether any incoming SUPPORTS edge (To == ev.ID)
// originates from a source whose resolved quality is >= qualityThreshold.
// The Master builds evidenceQuality by resolving each evidence item's SourceID
// through SourceRegistry.Lookup and mapping EvidenceID → QualityScore.
func HasHighQualitySupport(ev model.Evidence, allRelations []model.EvidenceRelation, qualityThreshold float64, evidenceQuality map[model.EvidenceID]float64) bool {
	for _, rel := range allRelations {
		if rel.Kind != model.EvidenceRelationSupports {
			continue
		}
		if rel.To != ev.ID {
			continue
		}
		q, ok := evidenceQuality[rel.From]
		if ok && q >= qualityThreshold {
			return true
		}
	}
	return false
}

// ComputeVerification determines the VerificationState for a single evidence item
// given its incident relations and the pre-resolved quality scores of all
// evidence items involved.
//
// The Master passes qualityThreshold (typically HighQualityThreshold) and
// evidenceQuality (EvidenceID → QualityScore resolved via SourceRegistry).
//
// State machine (per §0.8.3):
//   - contradicts >= k                         → DISPUTED
//   - hasHQSupport && contradicts == 0         → VERIFIED
//   - supports > 0 || contradicts > 0          → PARTIALLY_VERIFIED
//   - else                                     → UNVERIFIED
func ComputeVerification(ev model.Evidence, allRelations []model.EvidenceRelation, qualityThreshold float64, k int, evidenceQuality map[model.EvidenceID]float64) model.VerificationState {
	contradicts := ContradictionCount(ev, allRelations)
	supports := SupportsCount(ev, allRelations)
	hasHQSupport := HasHighQualitySupport(ev, allRelations, qualityThreshold, evidenceQuality)

	if contradicts >= k {
		return model.VerificationDisputed
	}
	if hasHQSupport && contradicts == 0 {
		return model.VerificationVerified
	}
	if supports > 0 || contradicts > 0 {
		return model.VerificationPartiallyVerified
	}
	return model.VerificationUnverified
}
