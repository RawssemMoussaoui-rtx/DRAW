package evidence

import (
	"fmt"
	"testing"

	"draw/internal/model"
)

func TestHighQualityThreshold(t *testing.T) {
	if HighQualityThreshold != 0.7 {
		t.Errorf("expected HighQualityThreshold=0.7, got %f", HighQualityThreshold)
	}
}

func TestKContradictions(t *testing.T) {
	if KContradictions != 2 {
		t.Errorf("expected KContradictions=2, got %d", KContradictions)
	}
}

func TestComputeVerification_NoRelations(t *testing.T) {
	ev := model.Evidence{ID: model.EvidenceID("ev1")}
	rels := []model.EvidenceRelation{}
	quality := map[model.EvidenceID]float64{}

	state := ComputeVerification(ev, rels, HighQualityThreshold, KContradictions, quality)

	if state != model.VerificationUnverified {
		t.Errorf("expected UNVERIFIED, got %s", state)
	}
}

func TestComputeVerification_KContradicts(t *testing.T) {
	ev := model.Evidence{ID: model.EvidenceID("ev1")}
	rels := []model.EvidenceRelation{
		{From: model.EvidenceID("c1"), To: model.EvidenceID("ev1"), Kind: model.EvidenceRelationContradicts, Strength: 1.0},
		{From: model.EvidenceID("c2"), To: model.EvidenceID("ev1"), Kind: model.EvidenceRelationContradicts, Strength: 1.0},
	}
	quality := map[model.EvidenceID]float64{
		model.EvidenceID("c1"): 0.9,
		model.EvidenceID("c2"): 0.9,
	}

	state := ComputeVerification(ev, rels, HighQualityThreshold, KContradictions, quality)

	if state != model.VerificationDisputed {
		t.Errorf("expected DISPUTED (K=2 contradicts), got %s", state)
	}
}

func TestComputeVerification_HighQualitySupportNoContradict(t *testing.T) {
	ev := model.Evidence{ID: model.EvidenceID("ev1")}
	rels := []model.EvidenceRelation{
		{From: model.EvidenceID("hq_supporter"), To: model.EvidenceID("ev1"), Kind: model.EvidenceRelationSupports, Strength: 0.9},
	}
	quality := map[model.EvidenceID]float64{
		model.EvidenceID("hq_supporter"): 0.9,
	}

	state := ComputeVerification(ev, rels, HighQualityThreshold, KContradictions, quality)

	if state != model.VerificationVerified {
		t.Errorf("expected VERIFIED (HQ support, no contradicts), got %s", state)
	}
}

func TestComputeVerification_LowQualitySupportNoContradict(t *testing.T) {
	ev := model.Evidence{ID: model.EvidenceID("ev1")}
	rels := []model.EvidenceRelation{
		{From: model.EvidenceID("lq_supporter"), To: model.EvidenceID("ev1"), Kind: model.EvidenceRelationSupports, Strength: 0.4},
	}
	quality := map[model.EvidenceID]float64{
		model.EvidenceID("lq_supporter"): 0.4,
	}

	state := ComputeVerification(ev, rels, HighQualityThreshold, KContradictions, quality)

	if state != model.VerificationPartiallyVerified {
		t.Errorf("expected PARTIALLY_VERIFIED (LQ support, no contradicts), got %s", state)
	}
}

func TestComputeVerification_ContradictionWinsOverSupport(t *testing.T) {
	ev := model.Evidence{ID: model.EvidenceID("ev1")}
	rels := []model.EvidenceRelation{
		{From: model.EvidenceID("hq_supporter"), To: model.EvidenceID("ev1"), Kind: model.EvidenceRelationSupports, Strength: 0.9},
		{From: model.EvidenceID("c1"), To: model.EvidenceID("ev1"), Kind: model.EvidenceRelationContradicts, Strength: 1.0},
		{From: model.EvidenceID("c2"), To: model.EvidenceID("ev1"), Kind: model.EvidenceRelationContradicts, Strength: 1.0},
	}
	quality := map[model.EvidenceID]float64{
		model.EvidenceID("hq_supporter"): 0.9,
		model.EvidenceID("c1"):           0.5,
		model.EvidenceID("c2"):           0.5,
	}

	state := ComputeVerification(ev, rels, HighQualityThreshold, KContradictions, quality)

	if state != model.VerificationDisputed {
		t.Errorf("expected DISPUTED (contradiction wins over HQ support), got %s", state)
	}
}

func TestComputeVerification_LessThanKContradicts(t *testing.T) {
	ev := model.Evidence{ID: model.EvidenceID("ev1")}
	rels := []model.EvidenceRelation{
		{From: model.EvidenceID("c1"), To: model.EvidenceID("ev1"), Kind: model.EvidenceRelationContradicts, Strength: 1.0},
		{From: model.EvidenceID("lq"), To: model.EvidenceID("ev1"), Kind: model.EvidenceRelationSupports, Strength: 0.4},
	}
	quality := map[model.EvidenceID]float64{
		model.EvidenceID("c1"): 0.9,
		model.EvidenceID("lq"): 0.4,
	}

	state := ComputeVerification(ev, rels, HighQualityThreshold, KContradictions, quality)

	if state != model.VerificationPartiallyVerified {
		t.Errorf("expected PARTIALLY_VERIFIED (1 contradict < K=2, LQ support), got %s", state)
	}
}

func TestComputeVerification_BelowThresholdQuality(t *testing.T) {
	ev := model.Evidence{ID: model.EvidenceID("ev1")}
	rels := []model.EvidenceRelation{
		{From: model.EvidenceID("near_hq"), To: model.EvidenceID("ev1"), Kind: model.EvidenceRelationSupports, Strength: 0.69},
	}
	quality := map[model.EvidenceID]float64{
		model.EvidenceID("near_hq"): 0.69,
	}

	state := ComputeVerification(ev, rels, HighQualityThreshold, KContradictions, quality)

	if state != model.VerificationPartiallyVerified {
		t.Errorf("expected PARTIALLY_VERIFIED (quality 0.69 < 0.7), got %s", state)
	}
}

func TestComputeVerification_Deterministic(t *testing.T) {
	ev := model.Evidence{ID: model.EvidenceID("ev1")}
	rels := []model.EvidenceRelation{
		{From: model.EvidenceID("hq_supporter"), To: model.EvidenceID("ev1"), Kind: model.EvidenceRelationSupports, Strength: 0.9},
		{From: model.EvidenceID("c1"), To: model.EvidenceID("ev1"), Kind: model.EvidenceRelationContradicts, Strength: 1.0},
		{From: model.EvidenceID("c2"), To: model.EvidenceID("ev1"), Kind: model.EvidenceRelationContradicts, Strength: 1.0},
	}
	quality := map[model.EvidenceID]float64{
		model.EvidenceID("hq_supporter"): 0.9,
		model.EvidenceID("c1"):           0.5,
		model.EvidenceID("c2"):           0.5,
	}

	s1 := ComputeVerification(ev, rels, HighQualityThreshold, KContradictions, quality)
	s2 := ComputeVerification(ev, rels, HighQualityThreshold, KContradictions, quality)
	s3 := ComputeVerification(ev, rels, HighQualityThreshold, KContradictions, quality)

	if s1 != s2 || s2 != s3 {
		t.Errorf("ComputeVerification is not deterministic: %s, %s, %s", s1, s2, s3)
	}
}

func TestComputeVerification_OutgoingSupportDoesNotVerify(t *testing.T) {
	ev := model.Evidence{ID: model.EvidenceID("ev1")}
	rels := []model.EvidenceRelation{
		{From: model.EvidenceID("ev1"), To: model.EvidenceID("target"), Kind: model.EvidenceRelationSupports, Strength: 0.9},
	}
	quality := map[model.EvidenceID]float64{
		model.EvidenceID("target"): 0.9,
	}

	state := ComputeVerification(ev, rels, HighQualityThreshold, KContradictions, quality)

	if state != model.VerificationPartiallyVerified {
		t.Errorf("expected PARTIALLY_VERIFIED (outgoing support only, no incoming), got %s", state)
	}
}

func TestContradictionCount(t *testing.T) {
	ev := model.Evidence{ID: model.EvidenceID("ev1")}
	rels := []model.EvidenceRelation{
		{From: model.EvidenceID("ev1"), To: model.EvidenceID("c1"), Kind: model.EvidenceRelationContradicts},
		{From: model.EvidenceID("c2"), To: model.EvidenceID("ev1"), Kind: model.EvidenceRelationContradicts},
		{From: model.EvidenceID("s1"), To: model.EvidenceID("ev1"), Kind: model.EvidenceRelationSupports},
	}

	count := ContradictionCount(ev, rels)
	if count != 2 {
		t.Errorf("expected 2 contradicts, got %d", count)
	}
}

func TestSupportsCount(t *testing.T) {
	ev := model.Evidence{ID: model.EvidenceID("ev1")}
	rels := []model.EvidenceRelation{
		{From: model.EvidenceID("ev1"), To: model.EvidenceID("s1"), Kind: model.EvidenceRelationSupports},
		{From: model.EvidenceID("c1"), To: model.EvidenceID("ev1"), Kind: model.EvidenceRelationContradicts},
	}

	count := SupportsCount(ev, rels)
	if count != 1 {
		t.Errorf("expected 1 support, got %d", count)
	}
}

func TestHasHighQualitySupport(t *testing.T) {
	ev := model.Evidence{ID: model.EvidenceID("ev1")}
	quality := map[model.EvidenceID]float64{
		model.EvidenceID("hq"): 0.9,
		model.EvidenceID("lq"): 0.4,
	}

	t.Run("high_quality_incoming", func(t *testing.T) {
		rels := []model.EvidenceRelation{
			{From: model.EvidenceID("hq"), To: model.EvidenceID("ev1"), Kind: model.EvidenceRelationSupports},
		}
		if !HasHighQualitySupport(ev, rels, HighQualityThreshold, quality) {
			t.Error("expected true for HQ incoming support")
		}
	})

	t.Run("low_quality_incoming", func(t *testing.T) {
		rels := []model.EvidenceRelation{
			{From: model.EvidenceID("lq"), To: model.EvidenceID("ev1"), Kind: model.EvidenceRelationSupports},
		}
		if HasHighQualitySupport(ev, rels, HighQualityThreshold, quality) {
			t.Error("expected false for LQ incoming support")
		}
	})

	t.Run("outgoing_support_not_counted", func(t *testing.T) {
		rels := []model.EvidenceRelation{
			{From: model.EvidenceID("ev1"), To: model.EvidenceID("hq"), Kind: model.EvidenceRelationSupports},
		}
		if HasHighQualitySupport(ev, rels, HighQualityThreshold, quality) {
			t.Error("expected false for outgoing-only support")
		}
	})
}

func makeContradictionRels(evID model.EvidenceID, strengths []float64) []model.EvidenceRelation {
	rels := make([]model.EvidenceRelation, len(strengths))
	for i, s := range strengths {
		rels[i] = model.EvidenceRelation{
			From:     model.EvidenceID(fmt.Sprintf("c%d", i)),
			To:       evID,
			Kind:     model.EvidenceRelationContradicts,
			Strength: s,
		}
	}
	return rels
}

func TestQuantizedStrength(t *testing.T) {
	tests := []struct {
		input    float64
		expected int64
	}{
		{0.0, 0},
		{-0.5, 0},
		{1.0, 1000},
		{1.5, 1000},
		{0.5, 500},
		{0.61, 610},
		{0.90, 900},
		{0.74, 740},
		{0.76, 760},
		{0.999, 999},
	}
	for _, tt := range tests {
		got := quantizedStrength(tt.input)
		if got != tt.expected {
			t.Errorf("quantizedStrength(%f) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

func TestTopKContradictionStrengthMilli(t *testing.T) {
	ev := model.Evidence{ID: model.EvidenceID("ev1")}

	t.Run("empty_relations", func(t *testing.T) {
		got := TopKContradictionStrengthMilli(ev, []model.EvidenceRelation{})
		if got != 0 {
			t.Errorf("expected 0, got %d", got)
		}
	})

	t.Run("single_contradiction", func(t *testing.T) {
		rels := makeContradictionRels(ev.ID, []float64{1.0})
		got := TopKContradictionStrengthMilli(ev, rels)
		if got != 1000 {
			t.Errorf("expected 1000, got %d", got)
		}
	})

	t.Run("two_strongest_selected_ignores_weak", func(t *testing.T) {
		rels := makeContradictionRels(ev.ID, []float64{1.0, 0.5, 0.3})
		got := TopKContradictionStrengthMilli(ev, rels)
		if got != 1500 {
			t.Errorf("expected 1500 (top-2: 1000+500), got %d", got)
		}
	})

	t.Run("only_contradicts_counted", func(t *testing.T) {
		rels := []model.EvidenceRelation{
			{From: model.EvidenceID("c1"), To: ev.ID, Kind: model.EvidenceRelationContradicts, Strength: 1.0},
			{From: model.EvidenceID("s1"), To: ev.ID, Kind: model.EvidenceRelationSupports, Strength: 1.0},
		}
		got := TopKContradictionStrengthMilli(ev, rels)
		if got != 1000 {
			t.Errorf("expected 1000 (only contradicts counted), got %d", got)
		}
	})

	t.Run("incident_to_ev", func(t *testing.T) {
		rels := []model.EvidenceRelation{
			{From: ev.ID, To: model.EvidenceID("other"), Kind: model.EvidenceRelationContradicts, Strength: 0.8},
			{From: ev.ID, To: model.EvidenceID("other2"), Kind: model.EvidenceRelationContradicts, Strength: 0.7},
		}
		got := TopKContradictionStrengthMilli(ev, rels)
		if got != 1500 {
			t.Errorf("expected 1500 (800+700), got %d", got)
		}
	})
}

func TestComputeVerification_Top2DisputedAcceptance(t *testing.T) {
	ev := model.Evidence{ID: model.EvidenceID("ev1")}
	quality := map[model.EvidenceID]float64{}

	repeatStrength := func(s float64, n int) []float64 {
		strengths := make([]float64, n)
		for i := range strengths {
			strengths[i] = s
		}
		return strengths
	}

	tests := []struct {
		name      string
		strengths []float64
		expected  model.VerificationState
	}{
		{
			name:      "2x strength 1.0",
			strengths: []float64{1.0, 1.0},
			expected:  model.VerificationDisputed,
		},
		{
			name:      "2x strength 0.61",
			strengths: []float64{0.61, 0.61},
			expected:  model.VerificationPartiallyVerified,
		},
		{
			name:      "4x strength 0.5",
			strengths: repeatStrength(0.5, 4),
			expected:  model.VerificationPartiallyVerified,
		},
		{
			name:      "20x strength 0.1",
			strengths: repeatStrength(0.1, 20),
			expected:  model.VerificationPartiallyVerified,
		},
		{
			name:      "1x 1.0 + 1x 0.5",
			strengths: []float64{1.0, 0.5},
			expected:  model.VerificationDisputed,
		},
		{
			name:      "1x 0.90 + 1x 0.74",
			strengths: []float64{0.90, 0.74},
			expected:  model.VerificationDisputed,
		},
		{
			name:      "3x strength 0.76",
			strengths: []float64{0.76, 0.76, 0.76},
			expected:  model.VerificationDisputed,
		},
		{
			name:      "1x 0.76 + 2x 0.74",
			strengths: []float64{0.76, 0.74, 0.74},
			expected:  model.VerificationDisputed,
		},
		{
			name:      "1x strength 1.00 alone",
			strengths: []float64{1.0},
			expected:  model.VerificationPartiallyVerified,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rels := makeContradictionRels(ev.ID, tt.strengths)
			state := ComputeVerification(ev, rels, HighQualityThreshold, KContradictions, quality)
			if state != tt.expected {
				top2 := TopKContradictionStrengthMilli(ev, rels)
				t.Errorf("expected %s, got %s (top2 milli=%d)", tt.expected, state, top2)
			}
		})
	}
}

func TestComputeVerification_Top2DisputedDeterministic(t *testing.T) {
	ev := model.Evidence{ID: model.EvidenceID("ev1")}
	quality := map[model.EvidenceID]float64{}

	tests := []struct {
		name      string
		strengths []float64
	}{
		{"2x 1.0", []float64{1.0, 1.0}},
		{"1x 1.0 + 1x 0.5", []float64{1.0, 0.5}},
		{"1x 0.90 + 1x 0.74", []float64{0.90, 0.74}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rels := makeContradictionRels(ev.ID, tt.strengths)
			results := make([]model.VerificationState, 5)
			for i := 0; i < 5; i++ {
				results[i] = ComputeVerification(ev, rels, HighQualityThreshold, KContradictions, quality)
			}
			for i := 1; i < 5; i++ {
				if results[i] != results[0] {
					t.Fatalf("non-deterministic: run 0=%s, run %d=%s", results[0], i, results[i])
				}
			}
		})
	}
}
