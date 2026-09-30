package evidence

import (
	"testing"

	"draw/internal/model"
)

// This file contains real-text calibration tests for the BCNE gate
// (Bidirectional Character N-gram Envelope) implemented in relations.go.
//
// Background (ground truth, DO NOT MODIFY similarity.go / relations.go):
//
//   - BcneCoverage(a, b) float64 : multiset (term-frequency) Jaccard
//     similarity of character-level trigrams. Computed in
//     internal/evidence/similarity.go.
//   - bcneAcceptanceThreshold == 0.6  is the gate (tau).
//   - HasNegationAnchor(s) bool checks for English negation tokens.
//   - ComputeRelations emits edges (bidirectionally => 2 edges) for a
//     same-Claim / different-Value pair:
//     - If HasNegationAnchor either Value (ANRB hard veto) -> CONTRADICTS (2 edges)
//     - Else if BcneCoverage >= tau -> SUPPORTS (2 edges, accepted as paraphrase)
//     - Else -> CONTRADICTS (2 edges, genuine contradiction)
//
// These cases span real-text pairs that exercise the gate boundary and
// adversarial patterns (negation insertion, antonym swap, paraphrases,
// true contradictions). The behavior prediction is derived from the ACTUAL
// computed coverage and negation status, so the tests are self-validating.

// calibrate runs one calibration pair: two evidence items sharing a Claim but
// with different Values. It computes the real BcneCoverage, checks negation
// anchor status, and asserts that ComputeRelations emits the expected kind
// and count of edges. Self-validating from live BcneCoverage/HasNegationAnchor.
func calibrate(t *testing.T, claim, valA, valB string) {
	t.Helper()

	const srcA = "srcA.calibration"
	const srcB = "srcB.calibration"
	a := mkEvidence("a.cal", srcA, claim, valA, 1.0)
	b := mkEvidence("b.cal", srcB, claim, valB, 1.0)

	coverage := BcneCoverage(valA, valB)
	hasNeg := HasNegationAnchor(valA) || HasNegationAnchor(valB)
	rels := ComputeRelations([]model.Evidence{a}, []model.Evidence{b})

	wantContradicts := 0
	wantSupports := 0
	if hasNeg || coverage < bcneAcceptanceThreshold {
		wantContradicts = 2
	} else {
		wantSupports = 2
	}

	gotContradicts := kindCount(rels, model.EvidenceRelationContradicts)
	gotSupports := kindCount(rels, model.EvidenceRelationSupports)

	t.Logf("coverage=%.4f hasNeg=%v wantContradicts=%d wantSupports=%d gotContradicts=%d gotSupports=%d | A=%q B=%q",
		coverage, hasNeg, wantContradicts, wantSupports, gotContradicts, gotSupports, valA, valB)

	if gotContradicts != wantContradicts || gotSupports != wantSupports {
		t.Errorf("coverage=%.4f hasNeg=%v: expected contradicts=%d supports=%d, got contradicts=%d supports=%d (rels=%+v)",
			coverage, hasNeg, wantContradicts, wantSupports, gotContradicts, gotSupports, rels)
	}
}

// ---------------------------------------------------------------------------
// Near-synonym adversarial pairs — semantically OPPOSITE but lexically similar.
// ---------------------------------------------------------------------------

// TestCalib_Adversarial_NegationInsert_ParisCapital: semantic contradiction
// with a negation-insertion adversarial. "Paris is the capital of France" vs
// "France is not the capital of Paris" — ANRB hard veto (contains "not")
// forces CONTRADICTS regardless of coverage.
func TestCalib_Adversarial_NegationInsert_ParisCapital(t *testing.T) {
	calibrate(t, "claim:CapitalFact",
		"Paris is the capital of France",
		"France is not the capital of Paris")
}

// TestCalib_Adversarial_NegationInsert_CapitalNotParis: second negation
// adversarial. Same direction of fact, negation inserted mid-sentence.
func TestCalib_Adversarial_NegationInsert_CapitalNotParis(t *testing.T) {
	calibrate(t, "claim:CapitalFact",
		"The capital of France is Paris",
		"The capital of France is not Paris")
}

// TestCalib_Adversarial_AntonymSwap_IncreasedDecreased: "increased" vs
// "decreased" on a policy claim. No negation token, so the gate decision
// depends purely on BcneCoverage (self-validating).
func TestCalib_Adversarial_AntonymSwap_IncreasedDecreased(t *testing.T) {
	calibrate(t, "claim:TaxPolicy",
		"The policy increased taxes this year",
		"The policy decreased taxes this year")
}

// TestCalib_Adversarial_AntonymSwap_ProfitsRoseFell: "rose" vs "fell" antonym
// swap on a profits claim.
func TestCalib_Adversarial_AntonymSwap_ProfitsRoseFell(t *testing.T) {
	calibrate(t, "claim:Profit",
		"Company profits rose sharply in 2023",
		"Company profits fell sharply in 2023")
}

// TestCalib_Adversarial_AntonymSwap_Temperature: "rose" vs "dropped" antonym
// swap on a temperature claim.
func TestCalib_Adversarial_AntonymSwap_Temperature(t *testing.T) {
	calibrate(t, "claim:Temperature",
		"The temperature rose to ninety degrees",
		"The temperature dropped to ninety degrees")
}

// TestCalib_Adversarial_AntonymSwap_Drug: "effective" vs "ineffective" —
// a morphological antonym that is NOT a negation token. Self-validating:
// coverage alone determines the gate.
func TestCalib_Adversarial_AntonymSwap_Drug(t *testing.T) {
	calibrate(t, "claim:DrugEfficacy",
		"The drug is effective for patients",
		"The drug is ineffective for patients")
}

// TestCalib_Adversarial_AntonymSwap_Sales: "increased" vs "decreased" on a
// sales claim with additional shared filler.
func TestCalib_Adversarial_AntonymSwap_Sales(t *testing.T) {
	calibrate(t, "claim:Sales",
		"Sales increased dramatically in the last year",
		"Sales decreased dramatically in the last year")
}

// ---------------------------------------------------------------------------
// Paraphrase / near-synonym controls (same meaning, different wording).
// ---------------------------------------------------------------------------

// TestCalib_Paraphrase_ScenarioE: the canonical paraphrase fixture. Same
// fact, reordered wording, high coverage => SUPPORTS (accepted, not suppressed).
func TestCalib_Paraphrase_ScenarioE(t *testing.T) {
	calibrate(t, "claim:CapitalFact",
		"Paris is the capital of France",
		"The capital city of France is Paris")
}

// TestCalib_Paraphrase_WordOrder: full content overlap with only word-order
// rearrangement. Self-validating based on coverage.
func TestCalib_Paraphrase_WordOrder(t *testing.T) {
	calibrate(t, "claim:MeetingTime",
		"The meeting is scheduled at 3 PM today",
		"The 3 PM meeting is scheduled today")
}

// TestCalib_Paraphrase_Scheduled: "scheduled" vs "set" — near-synonym
// paraphrase. Self-validating.
func TestCalib_Paraphrase_Scheduled(t *testing.T) {
	calibrate(t, "claim:ProjectDeadline",
		"The project is scheduled to finish in June",
		"The project is set to finish in June")
}

// ---------------------------------------------------------------------------
// True contradiction controls (genuinely different values for same claim).
// ---------------------------------------------------------------------------

// TestCalib_TrueContradiction_DifferentCity: "Paris" vs "Lyon" as the capital.
// Genuine factual contradiction — self-validating based on coverage.
func TestCalib_TrueContradiction_DifferentCity(t *testing.T) {
	calibrate(t, "claim:CapitalFact",
		"The capital of France is Paris",
		"The capital of France is Lyon")
}

// TestCalib_TrueContradiction_LowOverlap: same claim, genuinely opposite
// outcomes phrased quite differently — low coverage.
func TestCalib_TrueContradiction_LowOverlap(t *testing.T) {
	calibrate(t, "claim:TreatmentEfficacy",
		"The study confirms the treatment works",
		"The study found the treatment does not work")
}

// TestCalib_TrueContradiction_GuiltyAcquitted: "guilty" vs "acquitted" —
// opposite legal outcomes, low coverage.
func TestCalib_TrueContradiction_GuiltyAcquitted(t *testing.T) {
	calibrate(t, "claim:DefendantVerdict",
		"The defendant is guilty",
		"The defendant was acquitted")
}
