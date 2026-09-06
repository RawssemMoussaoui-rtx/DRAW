package evalharness

import (
	"fmt"

	"draw/internal/model"
)

func evidenceID(s ScenarioID, idx int) model.EvidenceID {
	return model.EvidenceID(fmt.Sprintf("%s:%d", s, idx))
}

func buildEvidence(s Scenario, sid model.SessionID, tid model.TaskID) []model.Evidence {
	evs := make([]model.Evidence, len(s.Sources))
	for i, src := range s.Sources {
		t := src.TaskID
		if t == "" {
			t = tid
		}
		evs[i] = model.Evidence{
			ID:            evidenceID(s.ID, i),
			SessionID:     sid,
			TaskID:        t,
			SourceID:      model.NewSourceID(src.Domain),
			Topic:         s.Topic,
			Claim:         src.Claim,
			Value:         src.Value,
			Confidence:    src.Confidence,
			Verification:  model.VerificationUnverified,
			CollectedAt:   baseTime,
		}
	}
	return evs
}

// ScenarioA — chain of duplicated/derived sources; expect 1 independent group.
func ScenarioA() Scenario {
	return Scenario{
		ID:      "A",
		Name:    "chain of duplicated/derived sources",
		Topic:   "research",
		Sources: []SourceSpec{
			{Domain: "hq1.example", Claim: "claim:A", Value: "value:A", Confidence: 1.0, Quality: 1.0},
			{Domain: "hq2.example", Claim: "claim:A", Value: "value:A", Confidence: 1.0, Quality: 1.0},
			{Domain: "lq1.example", Claim: "claim:A", Value: "value:A", Confidence: 0.3, Quality: 0.3},
			{Domain: "lq2.example", Claim: "claim:A", Value: "value:A", Confidence: 0.3, Quality: 0.3},
		},
		Quality: map[string]float64{"hq1.example": 1.0, "hq2.example": 1.0, "lq1.example": 0.3, "lq2.example": 0.3},
		Topology: []GroundTruthEdge{
			{A: "hq1.example", B: "hq2.example", Relation: "INDEPENDENT", Independent: true},
			{A: "hq1.example", B: "lq1.example", Relation: "INDEPENDENT", Independent: true},
			{A: "hq1.example", B: "lq2.example", Relation: "INDEPENDENT", Independent: true},
			{A: "hq2.example", B: "lq1.example", Relation: "INDEPENDENT", Independent: true},
			{A: "hq2.example", B: "lq2.example", Relation: "INDEPENDENT", Independent: true},
			{A: "lq1.example", B: "lq2.example", Relation: "INDEPENDENT", Independent: false},
		},
		Expect: Expectation{IndependentGroups: 1},
	}
}

// ScenarioB — genuinely independent sources; expect 3 groups.
func ScenarioB() Scenario {
	return Scenario{
		ID:      "B",
		Name:    "genuinely independent sources",
		Topic:   "research",
		Sources: []SourceSpec{
			{Domain: "delta.example", Claim: "claim:B1", Value: "value:B1", Confidence: 1.0, Quality: 1.0},
			{Domain: "epsilon.example", Claim: "claim:B2", Value: "value:B2", Confidence: 1.0, Quality: 1.0},
			{Domain: "zeta.example", Claim: "claim:B3", Value: "value:B3", Confidence: 1.0, Quality: 1.0},
		},
		Quality: map[string]float64{"delta.example": 1.0, "epsilon.example": 1.0, "zeta.example": 1.0},
		Topology: []GroundTruthEdge{
			{A: "delta.example", B: "epsilon.example", Relation: "INDEPENDENT", Independent: true},
			{A: "delta.example", B: "zeta.example", Relation: "INDEPENDENT", Independent: true},
			{A: "epsilon.example", B: "zeta.example", Relation: "INDEPENDENT", Independent: true},
		},
		Expect: Expectation{IndependentGroups: 3},
	}
}

// ScenarioC — known contradiction pair; expect detection.
func ScenarioC() Scenario {
	return Scenario{
		ID:      "C",
		Name:    "known contradiction pair",
		Topic:   "research",
		Sources: []SourceSpec{
			{Domain: "eta.example", Claim: "claim:Q", Value: "yes", Confidence: 1.0, Quality: 1.0},
			{Domain: "theta.example", Claim: "claim:Q", Value: "no", Confidence: 1.0, Quality: 1.0},
		},
		Quality: map[string]float64{"eta.example": 1.0, "theta.example": 1.0},
		Topology: []GroundTruthEdge{
			{A: "eta.example", B: "theta.example", Relation: "CONTRADICT", Independent: true},
		},
		Expect: Expectation{IndependentGroups: 2, Contradiction: true},
	}
}

// ScenarioD — saturation: declining novelty. Star-reuse composition; sizes [3,3,3,4,5,6], 9 unique domains.
func ScenarioD() Scenario {
	s := Scenario{
		ID:    "D",
		Name:  "saturation declining novelty",
		Topic: "research",
		Quality: map[string]float64{},
		Expect:  Expectation{IndependentGroups: 6, SaturationWindow: 6},
	}
	type grp struct {
		task    string
		claim   string
		value   string
		domains []string
	}
	groups := []grp{
		{"D-00", "fact:0", "value0", []string{"d0", "d1", "d2"}},
		{"D-01", "fact:1", "value1", []string{"d3", "d4", "d0"}},
		{"D-02", "fact:2", "value2", []string{"d5", "d0", "d1"}},
		{"D-03", "fact:3", "value3", []string{"d6", "d0", "d1", "d2"}},
		{"D-04", "fact:4", "value4", []string{"d7", "d0", "d1", "d2", "d3"}},
		{"D-05", "fact:5", "value5", []string{"d8", "d0", "d1", "d2", "d3", "d4"}},
	}
	for _, g := range groups {
		for _, d := range g.domains {
			s.Sources = append(s.Sources, SourceSpec{
				Domain: d, Claim: g.claim, Value: g.value,
				Confidence: 0.3, Quality: 0.3, TaskID: model.TaskID(g.task),
			})
			s.Quality[d] = 0.3
		}
	}
	return s
}

// ScenarioE — paraphrase; expect dedup-engine bottleneck.
func ScenarioE() Scenario {
	return Scenario{
		ID:      "E",
		Name:    "paraphrase dedup bottleneck",
		Topic:   "research",
		Sources: []SourceSpec{
			{Domain: "alpha.example", Claim: "claim:Capital", Value: "Paris is the capital of France", Confidence: 0.3, Quality: 0.3},
			{Domain: "beta.example", Claim: "claim:Capital", Value: "The capital city of France is Paris", Confidence: 0.3, Quality: 0.3},
		},
		Quality: map[string]float64{"alpha.example": 0.3, "beta.example": 0.3},
		Topology: []GroundTruthEdge{
			{A: "alpha.example", B: "beta.example", Relation: "DUPLICATE", Independent: false},
		},
		Expect: Expectation{IndependentGroups: 1, ParaphrasePairs: 1},
	}
}

func allScenarios() []Scenario {
	return []Scenario{ScenarioA(), ScenarioB(), ScenarioC(), ScenarioD(), ScenarioE()}
}
