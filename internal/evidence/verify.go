package evidence

import (
	"math"
	"sort"

	"draw/internal/model"
)

// HighQualityThreshold is the default QualityScore at or above which a source is
// considered "high-quality" for VERIFIED state transitions. Per Phase-F decision:
// configurable default 0.7. Unknown/provisional domains (0.3 baseline) do NOT qualify.
const HighQualityThreshold float64 = 0.7

// KContradictions is the threshold for DISPUTED state and replan trigger.
// Per spec §0.8.3 and §0.9: K=2. This mirrors config.DefaultReplan().KContradictions.
const KContradictions int = 2

// KContradictionStrengthMilli is the threshold (in integer milli-units) above
// which the sum of the two strongest CONTRADICTS edge strengths triggers the
// Disputed verification state. Per §0.8.3/§10: 1500 milli-units (= 1.5).
const KContradictionStrengthMilli int64 = 1500

// quantizedStrength converts a float64 strength in [0,1] to an integer
// milli-unit value (0→0, 1.0→1000). Values outside [0,1] are clamped.
func quantizedStrength(s float64) int64 {
	if s <= 0 {
		return 0
	}
	if s >= 1 {
		return 1000
	}
	return int64(math.Round(s * 1000))
}

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

// TopKContradictionStrengthMilli returns the sum of the two strongest
// CONTRADICTS edge strengths incident to ev, expressed in integer milli-units.
// It reuses ContradictionCount's exact contradiction-selection predicate:
//
//	rel.Kind == EvidenceRelationContradicts && (rel.From == ev.ID || rel.To == ev.ID)
//
// Selected strengths are quantized via quantizedStrength, then sorted
// deterministically by strength descending with a tie-break by From+To+Kind.
// Only the top two entries are summed — fewer than two contradictions means
// missing slots contribute zero.
//
// A single definitive contradiction (≈1.0) plus a single moderate one (≈0.5+)
// is sufficient for Disputed. Contradictions beyond the two strongest do not
// affect the decision — this mirrors the original k=2 count semantics in
// strength-aware form.
func TopKContradictionStrengthMilli(ev model.Evidence, allRelations []model.EvidenceRelation) int64 {
	type contradictionEntry struct {
		strengthMilli int64
		from          model.EvidenceID
		to            model.EvidenceID
		kind          model.EvidenceRelationKind
	}

	var entries []contradictionEntry
	for _, rel := range allRelations {
		// Reuse ContradictionCount's exact contradiction-selection predicate.
		if rel.Kind != model.EvidenceRelationContradicts {
			continue
		}
		if rel.From != ev.ID && rel.To != ev.ID {
			continue
		}
		entries = append(entries, contradictionEntry{
			strengthMilli: quantizedStrength(rel.Strength),
			from:          rel.From,
			to:            rel.To,
			kind:          rel.Kind,
		})
	}

	// Sort deterministically by strength descending, then tie-break by From+To+Kind.
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].strengthMilli != entries[j].strengthMilli {
			return entries[i].strengthMilli > entries[j].strengthMilli
		}
		if string(entries[i].from) != string(entries[j].from) {
			return string(entries[i].from) < string(entries[j].from)
		}
		if string(entries[i].to) != string(entries[j].to) {
			return string(entries[i].to) < string(entries[j].to)
		}
		return string(entries[i].kind) < string(entries[j].kind)
	})

	// Sum only the top two. Fewer than two entries means missing slots contribute zero.
	var sum int64
	top := 2
	if len(entries) < top {
		top = len(entries)
	}
	for i := 0; i < top; i++ {
		sum += entries[i].strengthMilli
	}
	return sum
}

// ComputeVerification determines the VerificationState for a single evidence item
// given its incident relations and the pre-resolved quality scores of all
// evidence items involved.
//
// The Master passes qualityThreshold (typically HighQualityThreshold) and
// evidenceQuality (EvidenceID → QualityScore resolved via SourceRegistry).
//
// State machine (per §0.8.3):
//   - top-2 contradiction strength >= 1500 milli            → DISPUTED
//   - hasHQSupport && contradicts == 0                       → VERIFIED
//   - supports > 0 || contradicts > 0                       → PARTIALLY_VERIFIED
//   - else                                                 → UNVERIFIED
func ComputeVerification(ev model.Evidence, allRelations []model.EvidenceRelation, qualityThreshold float64, k int, evidenceQuality map[model.EvidenceID]float64) model.VerificationState {
	contradicts := ContradictionCount(ev, allRelations)
	supports := SupportsCount(ev, allRelations)
	hasHQSupport := HasHighQualitySupport(ev, allRelations, qualityThreshold, evidenceQuality)

	topKStrengthMilli := TopKContradictionStrengthMilli(ev, allRelations)
	if topKStrengthMilli >= KContradictionStrengthMilli {
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
