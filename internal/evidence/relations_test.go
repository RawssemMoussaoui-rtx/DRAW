package evidence

import (
	"testing"

	"draw/internal/model"
)

func TestComputeRelations_SameClaimSameValueDifferentSource(t *testing.T) {
	newE := []model.Evidence{makeEv("n1", "src_a", "claim1", "val1", 0.8)}
	existE := []model.Evidence{makeEv("e1", "src_b", "claim1", "val1", 0.9)}

	rels := ComputeRelations(newE, existE)

	if len(rels) != 2 {
		t.Fatalf("expected 2 relations (bidirectional), got %d", len(rels))
	}

	for _, rel := range rels {
		if rel.Kind != model.EvidenceRelationSupports {
			t.Errorf("expected SUPPORTS, got %s", rel.Kind)
		}
		if rel.Strength != 0.8 {
			t.Errorf("expected strength 0.8 (min(0.8,0.9)), got %f", rel.Strength)
		}
	}

	hasNToE := false
	hasEToN := false
	for _, rel := range rels {
		if rel.From == "n1" && rel.To == "e1" {
			hasNToE = true
		}
		if rel.From == "e1" && rel.To == "n1" {
			hasEToN = true
		}
	}
	if !hasNToE || !hasEToN {
		t.Error("expected both n1->e1 and e1->n1 directions")
	}
}

func TestComputeRelations_SameClaimSameValueSameSource(t *testing.T) {
	newE := []model.Evidence{makeEv("n1", "src_a", "claim1", "val1", 0.8)}
	existE := []model.Evidence{makeEv("e1", "src_a", "claim1", "val1", 0.9)}

	rels := ComputeRelations(newE, existE)

	if len(rels) != 2 {
		t.Fatalf("expected 2 relations (bidirectional), got %d", len(rels))
	}

	for _, rel := range rels {
		if rel.Kind != model.EvidenceRelationDuplicates {
			t.Errorf("expected DUPLICATES, got %s", rel.Kind)
		}
		if rel.Strength != 1.0 {
			t.Errorf("expected strength 1.0, got %f", rel.Strength)
		}
	}
}

func TestComputeRelations_SameClaimDifferentValue(t *testing.T) {
	newE := []model.Evidence{makeEv("n1", "src_a", "claim1", "val1", 0.8)}
	existE := []model.Evidence{makeEv("e1", "src_b", "claim1", "val2", 0.9)}

	rels := ComputeRelations(newE, existE)

	if len(rels) != 2 {
		t.Fatalf("expected 2 relations (bidirectional), got %d", len(rels))
	}

	for _, rel := range rels {
		if rel.Kind != model.EvidenceRelationContradicts {
			t.Errorf("expected CONTRADICTS, got %s", rel.Kind)
		}
		if rel.Strength != 1.0 {
			t.Errorf("expected strength 1.0, got %f", rel.Strength)
		}
	}
}

func TestComputeRelations_DifferentTopicNoRelation(t *testing.T) {
	newE := []model.Evidence{makeEvTopic("n1", "src_a", "topic_a", "claim1", "val1", 0.8)}
	existE := []model.Evidence{makeEvTopic("e1", "src_b", "topic_b", "claim1", "val1", 0.9)}

	rels := ComputeRelations(newE, existE)

	if len(rels) != 0 {
		t.Fatalf("expected 0 relations for different topic, got %d", len(rels))
	}
}

func TestComputeRelations_DifferentClaimNoRelation(t *testing.T) {
	newE := []model.Evidence{makeEv("n1", "src_a", "claim1", "val1", 0.8)}
	existE := []model.Evidence{makeEv("e1", "src_b", "claim2", "val1", 0.9)}

	rels := ComputeRelations(newE, existE)

	if len(rels) != 0 {
		t.Fatalf("expected 0 relations for different claim, got %d", len(rels))
	}
}

func TestComputeRelations_EmptyInput(t *testing.T) {
	rels := ComputeRelations(nil, nil)
	if len(rels) != 0 {
		t.Fatalf("expected 0 relations for empty input, got %d", len(rels))
	}

	rels = ComputeRelations([]model.Evidence{makeEv("n1", "src", "c", "v", 0.5)}, nil)
	if len(rels) != 0 {
		t.Fatalf("expected 0 relations for empty existing, got %d", len(rels))
	}

	rels = ComputeRelations(nil, []model.Evidence{makeEv("e1", "src", "c", "v", 0.5)})
	if len(rels) != 0 {
		t.Fatalf("expected 0 relations for empty new, got %d", len(rels))
	}
}

func TestComputeRelations_CorrectStrength(t *testing.T) {
	newE := []model.Evidence{makeEv("n1", "src_a", "claim1", "val1", 0.5)}
	existE := []model.Evidence{makeEv("e1", "src_b", "claim1", "val1", 0.7)}

	rels := ComputeRelations(newE, existE)

	for _, rel := range rels {
		if rel.Strength != 0.5 {
			t.Errorf("expected strength 0.5 (min(0.5,0.7)), got %f", rel.Strength)
		}
	}
}

func TestComputeRelations_ConfidenceClamped(t *testing.T) {
	newE := []model.Evidence{makeEv("n1", "src_a", "claim1", "val1", 1.5)}
	existE := []model.Evidence{makeEv("e1", "src_b", "claim1", "val1", -0.3)}

	rels := ComputeRelations(newE, existE)

	for _, rel := range rels {
		if rel.Kind != model.EvidenceRelationSupports {
			t.Errorf("expected SUPPORTS, got %s", rel.Kind)
		}
		if rel.Strength != 0.0 {
			t.Errorf("expected clamped strength 0.0 (min(1.5,-0.3) clamped), got %f", rel.Strength)
		}
	}
}

func TestComputeRelations_NoDuplicates(t *testing.T) {
	newE := []model.Evidence{
		makeEv("n1", "src_a", "claim1", "val1", 0.8),
		makeEv("n2", "src_b", "claim1", "val1", 0.9),
	}
	existE := []model.Evidence{
		makeEv("e1", "src_c", "claim1", "val1", 0.9),
	}

	rels := ComputeRelations(newE, existE)

	seen := make(map[string]bool)
	for _, rel := range rels {
		key := string(rel.From) + "|" + string(rel.To) + "|" + string(rel.Kind)
		if seen[key] {
			t.Errorf("duplicate relation found: %s", key)
		}
		seen[key] = true
	}
}

func TestComputeRelations_SortedDeterministic(t *testing.T) {
	newE := []model.Evidence{
		makeEv("n3", "src_a", "claim1", "val1", 0.8),
		makeEv("n1", "src_b", "claim1", "val1", 0.9),
		makeEv("n2", "src_c", "claim1", "val2", 0.7),
	}
	existE := []model.Evidence{
		makeEv("e2", "src_d", "claim1", "val2", 0.8),
		makeEv("e1", "src_e", "claim1", "val1", 0.9),
	}

	rels := ComputeRelations(newE, existE)

	for i := 1; i < len(rels); i++ {
		if rels[i-1].From > rels[i].From {
			t.Errorf("relations not sorted by From: %s > %s", rels[i-1].From, rels[i].From)
		} else if rels[i-1].From == rels[i].From && rels[i-1].To > rels[i].To {
			t.Errorf("relations not sorted by To: %s > %s", rels[i-1].To, rels[i].To)
		} else if rels[i-1].From == rels[i].From && rels[i-1].To == rels[i].To && rels[i-1].Kind > rels[i].Kind {
			t.Errorf("relations not sorted by Kind: %s > %s", rels[i-1].Kind, rels[i].Kind)
		}
	}
}

func TestComputeRelations_Deterministic(t *testing.T) {
	newE := []model.Evidence{
		makeEv("n1", "src_a", "claim1", "val1", 0.8),
		makeEv("n2", "src_b", "claim1", "val2", 0.7),
		makeEv("n3", "src_a", "claim2", "val3", 0.9),
	}
	existE := []model.Evidence{
		makeEv("e1", "src_c", "claim1", "val1", 0.9),
		makeEv("e2", "src_d", "claim1", "val2", 0.8),
	}

	r1 := ComputeRelations(newE, existE)
	r2 := ComputeRelations(newE, existE)
	r3 := ComputeRelations(newE, existE)

	if !sameRelations(r1, r2) || !sameRelations(r2, r3) {
		t.Error("ComputeRelations is not deterministic across runs")
	}
}

func TestComputeRelations_MultiplePairs(t *testing.T) {
	newE := []model.Evidence{
		makeEv("n1", "src_a", "claim1", "val1", 0.9),
		makeEv("n2", "src_b", "claim1", "val2", 0.8),
	}
	existE := []model.Evidence{
		makeEv("e1", "src_c", "claim1", "val1", 0.9),
		makeEv("e2", "src_d", "claim2", "val1", 0.8),
	}

	rels := ComputeRelations(newE, existE)

	expectedKinds := make(map[model.EvidenceRelationKind]bool)
	for _, rel := range rels {
		switch {
		case (rel.From == "n1" || rel.From == "e1") && (rel.To == "n1" || rel.To == "e1"):
			expectedKinds[model.EvidenceRelationSupports] = true
		case (rel.From == "n2" || rel.From == "e1") && (rel.To == "n2" || rel.To == "e1"):
			expectedKinds[model.EvidenceRelationContradicts] = true
		}
	}

	if !expectedKinds[model.EvidenceRelationSupports] {
		t.Error("expected a SUPPORTS relation between n1 and e1")
	}
	if !expectedKinds[model.EvidenceRelationContradicts] {
		t.Error("expected a CONTRADICTS relation between n2 and e1")
	}
}

func TestComputeRelations_SameEvidenceSkipped(t *testing.T) {
	newE := []model.Evidence{makeEv("n1", "src_a", "claim1", "val1", 0.8)}
	existE := []model.Evidence{makeEv("n1", "src_a", "claim1", "val1", 0.8)}

	rels := ComputeRelations(newE, existE)

	if len(rels) != 0 {
		t.Fatalf("expected 0 relations for identical IDs, got %d", len(rels))
	}
}

// helpers

func makeEv(id, source, claim, value string, conf float64) model.Evidence {
	return model.Evidence{
		ID:         model.EvidenceID(id),
		SourceID:   model.SourceID(source),
		Topic:      "topic1",
		Claim:      claim,
		Value:      value,
		Confidence: conf,
	}
}

func makeEvTopic(id, source, topic, claim, value string, conf float64) model.Evidence {
	return model.Evidence{
		ID:         model.EvidenceID(id),
		SourceID:   model.SourceID(source),
		Topic:      topic,
		Claim:      claim,
		Value:      value,
		Confidence: conf,
	}
}

func sameRelations(a, b []model.EvidenceRelation) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
