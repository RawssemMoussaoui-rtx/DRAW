package evidence

import (
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
