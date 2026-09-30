package evidence

import (
	"testing"

	"draw/internal/model"
)

func mkEvidence(id, source, claim, value string, conf float64) model.Evidence {
	return model.Evidence{
		ID:         model.EvidenceID(id),
		SourceID:   model.SourceID(source),
		Topic:      "research",
		Claim:      claim,
		Value:      value,
		Confidence: conf,
	}
}

// kindCount returns how many emitted edges have the given kind.
func kindCount(rels []model.EvidenceRelation, k model.EvidenceRelationKind) int {
	n := 0
	for _, r := range rels {
		if r.Kind == k {
			n++
		}
	}
	return n
}

// anyKind returns true if any edge has the given kind.
func anyKind(rels []model.EvidenceRelation, k model.EvidenceRelationKind) bool {
	return kindCount(rels, k) > 0
}

// ---------------------------------------------------------------------------
// (a) Scenario E paraphrase pair — now ACCEPTED as SUPPORTS by the BCNE gate.
// On PATCHED relations.go: same Claim + different Value (paraphrase) with
// BcneCoverage >= tau and no negation anchor -> 2 bidirectional SUPPORTS edges
// (not suppressed). This test exercises PATCHED BCNE code; on unpatched
// V1.6 code (which suppresses) it would FAIL because it expects 2 SUPPORTS.
// ---------------------------------------------------------------------------
func TestComputeRelations_ParaphraseAccepted(t *testing.T) {
	a := mkEvidence("evE1", "alpha.example", "claim:Capital", "Paris is the capital of France", 0.3)
	b := mkEvidence("evE2", "beta.example", "claim:Capital", "The capital city of France is Paris", 0.3)

	coverage := BcneCoverage(a.Value, b.Value)
	hasNeg := HasNegationAnchor(a.Value) || HasNegationAnchor(b.Value)
	if hasNeg {
		t.Fatalf("Scenario E pair should not have negation anchors")
	}
	if coverage < bcneAcceptanceThreshold {
		t.Fatalf("fixture coverage %.4f < tau %.2f; Scenario E is no longer a paraphrase", coverage, bcneAcceptanceThreshold)
	}

	rels := ComputeRelations([]model.Evidence{a}, []model.Evidence{b})
	gotSupports := kindCount(rels, model.EvidenceRelationSupports)
	if gotSupports != 2 {
		t.Fatalf("expected 2 SUPPORTS edges for paraphrase pair (coverage=%.4f >= tau=%.2f); got %d supports: %+v",
			coverage, bcneAcceptanceThreshold, gotSupports, rels)
	}
	if anyKind(rels, model.EvidenceRelationContradicts) {
		t.Fatal("paraphrase pair must not emit CONTRADICTS")
	}
}

// ---------------------------------------------------------------------------
// (b) True-contradiction regression — Scenario C pair.
// Same Claim, genuinely different values ("yes" vs "no") -> coverage 0.0 < tau,
// so CONTRADICTS must be emitted (both directions).
// ---------------------------------------------------------------------------
func TestComputeRelations_TrueContradictionEmitted(t *testing.T) {
	a := mkEvidence("evC1", "eta.example", "claim:Q", "yes", 1.0)
	b := mkEvidence("evC2", "theta.example", "claim:Q", "no", 1.0)

	coverage := BcneCoverage(a.Value, b.Value)
	if coverage >= bcneAcceptanceThreshold {
		t.Fatalf("Scenario C coverage %.4f should be < tau %.2f", coverage, bcneAcceptanceThreshold)
	}

	rels := ComputeRelations([]model.Evidence{a}, []model.Evidence{b})
	got := kindCount(rels, model.EvidenceRelationContradicts)
	if got != 2 {
		t.Fatalf("expected 2 (bidirectional) CONTRADICTS edges for Scenario C; got %d edges=%+v", got, rels)
	}
}

// ---------------------------------------------------------------------------
// (c) Boundary cases straddling tau = bcneAcceptanceThreshold (0.6).
// Real text: one pair below threshold (CONTRADICTS), one above (SUPPORTS).
// Self-validating from actual BcneCoverage values.
// ---------------------------------------------------------------------------
func TestComputeRelations_BoundaryNearThreshold(t *testing.T) {
	// "The defendant is guilty" vs "The defendant was acquitted" — different
	// predicates, low trigram overlap.
	below := "The defendant is guilty"
	otherBelow := "The defendant was acquitted"
	// "The temperature rose to ninety degrees" vs "The temperature dropped..." —
	// same structure, only verb differs, high trigram overlap.
	above := "The temperature rose to ninety degrees"
	otherAbove := "The temperature dropped to ninety degrees"

	oBelow := BcneCoverage(below, otherBelow)
	oAbove := BcneCoverage(above, otherAbove)
	if oBelow >= bcneAcceptanceThreshold {
		t.Fatalf("below pair coverage %.4f must be < %.2f", oBelow, bcneAcceptanceThreshold)
	}
	if oAbove < bcneAcceptanceThreshold {
		t.Fatalf("above pair coverage %.4f must be >= %.2f", oAbove, bcneAcceptanceThreshold)
	}

	// below -> emits CONTRADICTS (bidirectional).
	c := mkEvidence("evB1", "srcB1.example", "claim:Bound", below, 1.0)
	d := mkEvidence("evB2", "srcB2.example", "claim:Bound", otherBelow, 1.0)
	if got := kindCount(ComputeRelations([]model.Evidence{c}, []model.Evidence{d}), model.EvidenceRelationContradicts); got != 2 {
		t.Fatalf("below-threshold pair should emit 2 CONTRADICTS; got %d", got)
	}

	// above -> SUPPORTS (bidirectional, accepted as paraphrase).
	e := mkEvidence("evA1", "srcA1.example", "claim:Bound", above, 1.0)
	f := mkEvidence("evA2", "srcA2.example", "claim:Bound", otherAbove, 1.0)
	if got := kindCount(ComputeRelations([]model.Evidence{e}, []model.Evidence{f}), model.EvidenceRelationSupports); got != 2 {
		t.Fatalf("above-threshold pair should emit 2 SUPPORTS; got %d", got)
	}
}

// ---------------------------------------------------------------------------
// BcneCoverage edge cases.
// ---------------------------------------------------------------------------
func TestBcneCoverage_EdgeCases(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want float64 // exact value (0.0 or 1.0) or -1 for "range check only"
	}{
		{"identical", "Paris is the capital of France", "Paris is the capital of France", 1.0},
		{"identical single token", "yes", "yes", 1.0},
		{"completely different single", "yes", "no", 0.0},
		{"completely different multi", "hello world foo bar", "cat dog bird fish", 0.0},
		{"both empty", "", "", 0.0},
		{"one empty", "hello", "", 0.0},
		{"one whitespace", "hello", "   ", 0.0},
		{"case folding", "Paris", "paris", 1.0},
		{"whitespace only", "   ", "   ", 1.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BcneCoverage(tc.a, tc.b)
			if got < 0.0 || got > 1.0 {
				t.Fatalf("BcneCoverage out of [0,1]: got %f", got)
			}
			if tc.want >= 0.0 && got != tc.want {
				t.Fatalf("expected %.1f; got %f", tc.want, got)
			}
		})
	}

	// Sanity: Scenario C overlap computed from real fixture strings.
	if got := BcneCoverage("yes", "no"); got != 0.0 {
		t.Fatalf("Scenario C coverage should be 0.0; got %f", got)
	}
	// Sanity: Scenario E overlap computed from real fixture strings.
	if got := BcneCoverage("Paris is the capital of France", "The capital city of France is Paris"); got < bcneAcceptanceThreshold {
		t.Fatalf("Scenario E coverage should be >= %.2f; got %f", bcneAcceptanceThreshold, got)
	}
}

// ---------------------------------------------------------------------------
// HasNegationAnchor tests.
// ---------------------------------------------------------------------------
func TestHasNegationAnchor_Positive(t *testing.T) {
	cases := []string{
		"France is not the capital of Paris",
		"They cannot confirm the results",
		"He did not respond",
		"She doesn't know",
		"Wasn't there a warning",
		"Nothing is certain",
		"I will never agree to this",
	}
	for _, text := range cases {
		if !HasNegationAnchor(text) {
			t.Errorf("expected negation anchor in %q", text)
		}
	}
}

func TestHasNegationAnchor_Negative(t *testing.T) {
	cases := []string{
		"The capital of France is Paris",
		"The temperature rose to ninety degrees",
		"Company profits fell sharply in 2023",
		"The defendant was acquitted",
		"hello world",
	}
	for _, text := range cases {
		if HasNegationAnchor(text) {
			t.Errorf("did not expect negation anchor in %q", text)
		}
	}
}

func TestHasNegationAnchor_NoFalsePositives(t *testing.T) {
	// These contain negation-like substrings but not whole-word matches.
	cases := []string{
		"notice",
		"nothingness",
		"knot",
		"notable",
		"notoriety",
		"canopy",
		"know",
		"knows",
	}
	for _, text := range cases {
		if HasNegationAnchor(text) {
			t.Errorf("false positive: expected no negation anchor in %q", text)
		}
	}
}
