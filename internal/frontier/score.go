package frontier

import (
	"math"

	"draw/internal/config"
	"draw/internal/model"
	"draw/internal/storage"
)

type scorer struct {
	weights config.ScoringWeights
	costs   map[model.TaskType]int
	maxCost int
}

func newScorer(cfg config.SchedulerConfig) scorer {
	costs := cfg.CostModel
	if costs == nil {
		costs = model.DefaultCostModel()
	}
	maxCost := 0
	for _, c := range costs {
		if c > maxCost {
			maxCost = c
		}
	}
	if maxCost <= 0 {
		maxCost = 1
	}
	return scorer{weights: cfg.ScoringWeights, costs: costs, maxCost: maxCost}
}

// saturateContradiction squashes a raw contradiction/unreliability signal x into
// the bounded interval (-scale, +scale) via tanh(alpha*x). tanh is chosen for its
// near-linear behaviour near zero (small contradictions affect priority gently)
// and its saturating tail (diminishing returns so a single pathological source
// can never dominate the queue indefinitely). alpha sets the saturation steepness;
// scale sets the maximum magnitude of the contribution.
func saturateContradiction(x, alpha, scale float64) float64 {
	return math.Tanh(alpha*x) * scale
}

func (s scorer) score(c URLCandidate, src storage.SourceRegistry) float64 {
	priority := clamp01(float64(safePriority(c.PriorityHint)) / 1000.0)
	sourceValue := clamp01(qualityFor(c.Domain, src))
	// contradictionSignal is the complement of source quality (1 - sourceValue),
	// derived from the SourceRegistry profile. A poor or unknown source
	// (sourceValue -> 0) yields a high signal, flagging the candidate as a
	// worthwhile investigation target to resolve the conflict. Unknown domains
	// resolve to qualityFor's 0.3 baseline, giving signal 0.7.
	contradictionSignal := 1 - sourceValue
	cost := s.costs[model.TaskTypeFetchHTTP]
	if cost <= 0 {
		cost = 1
	}
	normCost := clamp01(float64(cost) / float64(s.maxCost))
	w := s.weights
	return w.Priority*priority +
		w.InformationValue*0 +
		w.FreshnessValue*0.5 +
		w.SourceValue*sourceValue +
		w.ContradictionValue*saturateContradiction(contradictionSignal, w.ContradictionAlpha, w.ContradictionScale) -
		w.Cost*normCost -
		w.ErrorRisk*0
}

func safePriority(p int) int {
	if p <= 0 {
		return 500
	}
	if p > 1000 {
		return 1000
	}
	return p
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}
