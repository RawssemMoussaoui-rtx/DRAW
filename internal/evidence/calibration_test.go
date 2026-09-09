package evidence

import (
	"testing"

	"draw/internal/model"
)

// This file contains real-text calibration tests for the Stage 2 acceptance
// (paraphrase) gate implemented in relations.go.
//
// Background (ground truth, DO NOT MODIFY similarity.go / relations.go):
//
//   - ValueSimilarity(a, b) float64  : n-gram (n=1,2,3) multiset Jaccard, max
//     across n. Computed in internal/evidence/similarity.go.
//   - contradictionSimilarityThreshold == 0.8  is the gate (tau).
//   - ComputeRelations emits CONTRADICTS (bidirectionally => 2 edges) for a
//     same-Claim / different-Value pair IFF ValueSimilarity < tau. When the
//     overlap is >= tau the pair is treated as a paraphrase and the edge is
//     suppressed entirely (0 edges) — a pure abstention, no new Kind.
//
// These cases span the 0.70-0.90 overlap stratum so the gate boundary around
// tau=0.8 is exercised on both sides, with emphasis on near-synonym
// adversarial pairs: sentences that are semantically opposite but lexically
// similar (e.g. "X increased" vs "X decreased", or a negation-insertion pair
// vs. its affirmative form). Several of those adversarial pairs land *above*
// tau and are therefore (arguably incorrectly) suppressed — these cases
// document that known false-negative failure mode of the lexical gate.

// calibrate runs one calibration pair: two evidence items sharing a Claim but
// with different Values. It computes the real ValueSimilarity overlap, checks
// the pair lands in the intended overlap stratum [lo, hi], and asserts that
// ComputeRelations emits CONTRADICTS iff overlap < tau (2 bidirectional edges),
// otherwise suppresses (0 edges). The behavior prediction is derived from the
// ACTUAL computed overlap, so the test is self-validating.
func calibrate(t *testing.T, claim, valA, valB string, lo, hi float64) {
	t.Helper()

	const srcA = "srcA.calibration"
	const srcB = "srcB.calibration"
	a := mkEvidence("a.cal", srcA, claim, valA, 1.0)
	b := mkEvidence("b.cal", srcB, claim, valB, 1.0)

	overlap := ValueSimilarity(valA, valB)
	rels := ComputeRelations([]model.Evidence{a}, []model.Evidence{b})
	gotContradicts := kindCount(rels, model.EvidenceRelationContradicts)

	suppressed := overlap >= contradictionSimilarityThreshold
	wantContradicts := 0
	if !suppressed {
		wantContradicts = 2
	}
	t.Logf("overlap=%.4f suppressed=%v wantContradicts=%d gotContradicts=%d edges=%d | A=%q B=%q",
		overlap, suppressed, wantContradicts, gotContradicts, len(rels), valA, valB)

	if overlap < lo-1e-9 || overlap > hi+1e-9 {
		t.Errorf("overlap %.4f outside intended stratum [%.3f, %.3f] for %q vs %q",
			overlap, lo, hi, valA, valB)
	}
	if gotContradicts != wantContradicts {
		t.Errorf("overlap=%.4f (suppressed=%v): expected %d CONTRADICTS edges, got %d (rels=%+v)",
			overlap, suppressed, wantContradicts, gotContradicts, rels)
	}
}

// ---------------------------------------------------------------------------
// Near-synonym adversarial pairs — semantically OPPOSITE but lexically similar.
// Emphasis group 1: negation-insertion adversarial pairs (above tau => the
// lexical gate FALSELY suppresses a real contradiction — documented failure
// mode of the gate).
// ---------------------------------------------------------------------------

// TestCalib_Adversarial_NegationInsert_ParisCapital: the headline case from the
// brief. "Paris is the capital of France" vs "France is not the capital of
// Paris" is a semantic contradiction but shares all content tokens plus "not",
// so overlap is high and the pair is suppressed (no CONTRADICTS edge).
func TestCalib_Adversarial_NegationInsert_ParisCapital(t *testing.T) {
	calibrate(t, "claim:CapitalFact",
		"Paris is the capital of France",
		"France is not the capital of Paris",
		0.84, 0.87)
}

// TestCalib_Adversarial_NegationInsert_CapitalNotParis: second negation
// adversarial. Same direction of fact, negation inserted mid-sentence.
func TestCalib_Adversarial_NegationInsert_CapitalNotParis(t *testing.T) {
	calibrate(t, "claim:CapitalFact",
		"The capital of France is Paris",
		"The capital of France is not Paris",
		0.84, 0.87)
}

// ---------------------------------------------------------------------------
// Near-synonym adversarial pairs — minimal antonym swap, content-token count
// swept to stride the strata just below tau into the CONTRADICTS-emitting zone.
// ---------------------------------------------------------------------------

// TestCalib_Adversarial_AntonymSwap_Short: "increased" vs "decreased", no
// shared filler beyond the frame. Stratum ~0.71 (< tau) => CONTRADICTS.
func TestCalib_Adversarial_AntonymSwap_Short(t *testing.T) {
	calibrate(t, "claim:TaxPolicy",
		"The policy increased taxes this year",
		"The policy decreased taxes this year",
		0.70, 0.73)
}

// TestCalib_Adversarial_AntonymSwap_Medium: same swap + one shared filler
// token, pushing overlap to ~0.75 (still < tau) => CONTRADICTS.
func TestCalib_Adversarial_AntonymSwap_Medium(t *testing.T) {
	calibrate(t, "claim:TaxPolicy",
		"The policy increased taxes this year significantly",
		"The policy decreased taxes this year significantly",
		0.74, 0.77)
}

// TestCalib_Adversarial_AntonymSwap_Long: +another filler token, ~0.78 (< tau)
// => still CONTRADICTS.
func TestCalib_Adversarial_AntonymSwap_Long(t *testing.T) {
	calibrate(t, "claim:TaxPolicy",
		"The policy increased taxes this year significantly and",
		"The policy decreased taxes this year significantly and",
		0.76, 0.79)
}

// TestCalib_Adversarial_AntonymSwap_Suppressed: adding enough shared filler
// pushes the SAME antonym swap above tau (~0.846), demonstrating the gate
// FALSELY suppresses this genuine contradiction.
func TestCalib_Adversarial_AntonymSwap_Suppressed(t *testing.T) {
	calibrate(t, "claim:TaxPolicy",
		"The policy increased taxes this year in a very significant way indeed",
		"The policy decreased taxes this year in a very significant way indeed",
		0.83, 0.86)
}

// TestCalib_Adversarial_ProfitsRoseFell_Quarter: "rose" vs "fell" antonym swap
// on a company-profits claim, ~0.71 (< tau) => CONTRADICTS.
func TestCalib_Adversarial_ProfitsRoseFell_Quarter(t *testing.T) {
	calibrate(t, "claim:Profit",
		"Company profits rose sharply in 2023",
		"Company profits fell sharply in 2023",
		0.70, 0.73)
}

// TestCalib_Adversarial_DrugEffectiveIneffective: "effective" vs
// "ineffective" — a near-morphological antonym that is not a strict prefix
// match, ~0.71 (< tau) => CONTRADICTS.
func TestCalib_Adversarial_DrugEffectiveIneffective(t *testing.T) {
	calibrate(t, "claim:DrugEfficacy",
		"The drug is effective for patients",
		"The drug is ineffective for patients",
		0.70, 0.73)
}

// TestCalib_Adversarial_TemperatureRoseDropped: "rose" vs "dropped" antonym
// swap on a temperature claim, ~0.71 (< tau) => CONTRADICTS.
func TestCalib_Adversarial_TemperatureRoseDropped(t *testing.T) {
	calibrate(t, "claim:Temperature",
		"The temperature rose to ninety degrees",
		"The temperature dropped to ninety degrees",
		0.70, 0.73)
}

// TestCalib_Adversarial_SalesIncrDecr_Year: "increased" vs "decreased" on a
// sales claim with enough shared tokens to sit at ~0.75 (< tau) => CONTRADICTS.
func TestCalib_Adversarial_SalesIncrDecr_Year(t *testing.T) {
	calibrate(t, "claim:Sales",
		"Sales increased dramatically in the last year",
		"Sales decreased dramatically in the last year",
		0.74, 0.77)
}

// ---------------------------------------------------------------------------
// Paraphrase / near-synonym controls (same meaning, different wording).
// ---------------------------------------------------------------------------

// TestCalib_Paraphrase_ScenarioE: the canonical paraphrase fixture from
// similarity_test.go — same fact, reordered wording, high overlap (~0.857)
// => suppressed (no edge), exactly what the gate should do.
func TestCalib_Paraphrase_ScenarioE(t *testing.T) {
	calibrate(t, "claim:CapitalFact",
		"The capital city of France is Paris",
		"Paris is the capital of France",
		0.84, 0.87)
}

// TestCalib_Paraphrase_FalsePositive_BelowTau: near-synonym paraphrase
// ("scheduled" vs "set") that happens to land just BELOW tau (~0.778) and is
// therefore (arguably incorrectly) emitted as CONTRADICTS — documented
// false-positive failure mode of the lexical gate. Same meaning, different
// wording, yet flagged as contradiction.
func TestCalib_Paraphrase_FalsePositive_BelowTau(t *testing.T) {
	calibrate(t, "claim:ProjectDeadline",
		"The project is scheduled to finish in June",
		"The project is set to finish in June",
		0.76, 0.79)
}

// TestCalib_Paraphrase_WordOrderSuppressed: full content overlap with only
// word-order rearrangement pushes overlap to ~0.875 (>= tau) => suppressed,
// confirming reordering-only pairs round the gate correctly.
func TestCalib_Paraphrase_WordOrderSuppressed(t *testing.T) {
	calibrate(t, "claim:MeetingTime",
		"The meeting is scheduled at 3 PM today",
		"The 3 PM meeting is scheduled today",
		0.86, 0.89)
}

// ---------------------------------------------------------------------------
// True contradiction controls (genuinely different values for same claim).
// ---------------------------------------------------------------------------

// TestCalib_TrueContradiction_DifferentCity: "Paris" vs "Lyon" as the capital —
// a genuine factual contradiction inside the stratum (~0.71 < tau) => CONTRADICTS.
func TestCalib_TrueContradiction_DifferentCity(t *testing.T) {
	calibrate(t, "claim:CapitalFact",
		"The capital of France is Paris",
		"The capital of France is Lyon",
		0.70, 0.73)
}

// TestCalib_TrueContradiction_LowOverlap_StudyWorks: same claim, genuinely
// opposite findings phrased quite differently => low overlap (~0.40, below the
// 0.70 stratum) => CONTRADICTS (control for the clear-contradiction regime).
func TestCalib_TrueContradiction_LowOverlap_StudyWorks(t *testing.T) {
	calibrate(t, "claim:TreatmentEfficacy",
		"The study confirms the treatment works",
		"The study found the treatment does not work",
		0.0, 0.45)
}

// TestCalib_TrueContradiction_LowOverlap_GuiltyAcquitted: "guilty" vs
// "acquitted" — opposite legal outcomes, low overlap (~0.33, below the stratum)
// => CONTRADICTS.
func TestCalib_TrueContradiction_LowOverlap_GuiltyAcquitted(t *testing.T) {
	calibrate(t, "claim:DefendantVerdict",
		"The defendant is guilty",
		"The defendant was acquitted",
		0.0, 0.45)
}
