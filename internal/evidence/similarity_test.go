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
// (a) Bug reproduction — Scenario E paraphrase pair.
// On UNPATCHED relations.go: same Claim + different Value (textual) -> CONTRADICTS
// emitted. On PATCHED (with the similarity gate) -> no edge at all (abstained).
// This test exercises PATCHED code; on unpatched code it FAILS because a
// CONTRADICTS edge would be present. Scenario E: same-Claim items whose
// differing Values are textual paraphrases (high n-gram overlap) are
// suppressed by the acceptance gate.
// ---------------------------------------------------------------------------
func TestComputeRelations_ParaphraseSuppressed(t *testing.T) {
	a := mkEvidence("evE1", "alpha.example", "claim:Capital", "Paris is the capital of France", 0.3)
	b := mkEvidence("evE2", "beta.example", "claim:Capital", "The capital city of France is Paris", 0.3)

	overlap := ValueSimilarity(a.Value, b.Value)
	if overlap < contradictionSimilarityThreshold {
		t.Fatalf("fixture overlap %.4f < tau %.2f; Scenario E is no longer a paraphrase", overlap, contradictionSimilarityThreshold)
	}

	rels := ComputeRelations([]model.Evidence{a}, []model.Evidence{b})
	if len(rels) != 0 {
		t.Fatalf("expected NO edge for paraphrase pair (overlap=%.4f >= tau=%.2f); got %d edges: %+v",
			overlap, contradictionSimilarityThreshold, len(rels), rels)
	}
	if anyKind(rels, model.EvidenceRelationContradicts) {
		t.Fatal("paraphrase pair must not emit CONTRADICTS")
	}
}

// ---------------------------------------------------------------------------
// (b) True-contradiction regression — Scenario C pair.
// Same Claim, genuinely different values ("yes" vs "no") -> overlap 0.0 < tau,
// so CONTRADICTS must still be emitted (both directions).
// ---------------------------------------------------------------------------
func TestComputeRelations_TrueContradictionEmitted(t *testing.T) {
	a := mkEvidence("evC1", "eta.example", "claim:Q", "yes", 1.0)
	b := mkEvidence("evC2", "theta.example", "claim:Q", "no", 1.0)

	overlap := ValueSimilarity(a.Value, b.Value)
	if overlap >= contradictionSimilarityThreshold {
		t.Fatalf("Scenario C overlap %.4f should be < tau %.2f", overlap, contradictionSimilarityThreshold)
	}

	rels := ComputeRelations([]model.Evidence{a}, []model.Evidence{b})
	// bidirectional double-write -> 2 edges of the same kind.
	got := kindCount(rels, model.EvidenceRelationContradicts)
	if got != 2 {
		t.Fatalf("expected 2 (bidirectional) CONTRADICTS edges for Scenario C; got %d edges=%+v", got, rels)
	}
}

// ---------------------------------------------------------------------------
// (c) Boundary cases straddling tau = contradictionSimilarityThreshold (0.8).
// "just below" -> overlap < tau -> CONTRADICTS emitted; "just above" -> overlap
// >= tau -> suppressed.
// ---------------------------------------------------------------------------
func TestComputeRelations_BoundaryNearThreshold(t *testing.T) {
	// just below: 7 shared tokens + 1 unique each -> unigram Jaccard 7/9 ~= 0.7778.
	below := "w1 w2 w3 w4 w5 w6 w7 uniqA"
	// just above: 9 shared tokens + 1 unique each -> unigram Jaccard 9/11 ~= 0.8182.
	above := "w1 w2 w3 w4 w5 w6 w7 w8 w9 uniqA"
	otherBelow := "w1 w2 w3 w4 w5 w6 w7 uniqB"
	otherAbove := "w1 w2 w3 w4 w5 w6 w7 w8 w9 uniqB"

	oBelow := ValueSimilarity(below, otherBelow)
	oAbove := ValueSimilarity(above, otherAbove)
	if oBelow >= contradictionSimilarityThreshold {
		t.Fatalf("below pair overlap %.4f must be < %.2f", oBelow, contradictionSimilarityThreshold)
	}
	if oAbove < contradictionSimilarityThreshold {
		t.Fatalf("above pair overlap %.4f must be >= %.2f", oAbove, contradictionSimilarityThreshold)
	}

	// below -> emits CONTRADICTS (bidirectional).
	c := mkEvidence("evB1", "srcB1.example", "claim:Bound", below, 1.0)
	d := mkEvidence("evB2", "srcB2.example", "claim:Bound", otherBelow, 1.0)
	if got := kindCount(ComputeRelations([]model.Evidence{c}, []model.Evidence{d}), model.EvidenceRelationContradicts); got != 2 {
		t.Fatalf("below-threshold pair should emit 2 CONTRADICTS; got %d", got)
	}

	// above -> suppressed (no edge).
	e := mkEvidence("evA1", "srcA1.example", "claim:Bound", above, 1.0)
	f := mkEvidence("evA2", "srcA2.example", "claim:Bound", otherAbove, 1.0)
	if rels := ComputeRelations([]model.Evidence{e}, []model.Evidence{f}); len(rels) != 0 {
		t.Fatalf("above-threshold pair should emit no edges; got %+v", rels)
	}
}

// ---------------------------------------------------------------------------
// (d) ValueSimilarity edge cases.
// ---------------------------------------------------------------------------
func TestValueSimilarity_EdgeCases(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want float64
	}{
		{"identical", "Paris is the capital of France", "Paris is the capital of France", 1.0},
		{"identical single token", "yes", "yes", 1.0},
		{"completely different single", "yes", "no", 0.0},
		{"completely different multi", "hello world foo bar", "cat dog bird fish", 0.0},
		{"both empty", "", "", 0.0},
		{"one empty", "hello", "", 0.0},
		{"one whitespace", "hello", "   ", 0.0},
		{"case folding", "Paris", "paris", 1.0},
		{"punctuation stripped", "France.", "France", 1.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ValueSimilarity(tc.a, tc.b)
			if got < 0.0 || got > 1.0 {
				t.Fatalf("ValueSimilarity out of [0,1]: got %f", got)
			}
			// exact for the discrete cases we assert
			if tc.want == 0.0 && got > 0.0 {
				t.Fatalf("expected 0.0; got %f", got)
			}
			if tc.want == 1.0 && got != 1.0 {
				t.Fatalf("expected 1.0; got %f", got)
			}
		})
	}

	// Sanity: Scenario C overlap computed from the real fixture strings.
	if got := ValueSimilarity("yes", "no"); got != 0.0 {
		t.Fatalf("Scenario C overlap should be 0.0; got %f", got)
	}
	// Sanity: Scenario E overlap computed from the real fixture strings.
	if got := ValueSimilarity("Paris is the capital of France", "The capital city of France is Paris"); got < contradictionSimilarityThreshold {
		t.Fatalf("Scenario E overlap should be >= %.2f; got %f", contradictionSimilarityThreshold, got)
	}
}
