package evalharness

import (
	"encoding/json"
	"fmt"

	"draw/internal/evidence"
	"draw/internal/model"
)

func accOf(ok bool) float64 {
	if ok {
		return 1.0
	}
	return 0.0
}

func mergeRate(fp, total int) float64 {
	if total == 0 {
		return 0.0
	}
	return float64(fp) / float64(total)
}

func computeMetrics(s Scenario, state ObservedState) []MetricResult {
	return []MetricResult{
		computeM1(s, state),
		computeM2(s, state),
		computeM3(s, state),
		computeM4(s, state),
		computeM5(s, state),
		computeM6(s, state),
	}
}

// M1 — independence_group_count: connected components of evidence via
// SUPPORTS/DUPLICATES edges (by EvidenceID) vs expect.
func computeM1(s Scenario, state ObservedState) MetricResult {
	parent := map[model.EvidenceID]model.EvidenceID{}
	var find func(model.EvidenceID) model.EvidenceID
	find = func(x model.EvidenceID) model.EvidenceID {
		if parent[x] == "" {
			parent[x] = x
			return x
		}
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b model.EvidenceID) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}
	for _, ev := range state.Evidence {
		find(ev.ID)
	}
	for _, e := range state.Edges {
		k := string(e.Kind)
		if k == string(model.EvidenceRelationSupports) || k == string(model.EvidenceRelationDuplicates) {
			union(e.From, e.To)
		}
	}
	seen := map[model.EvidenceID]bool{}
	for id := range parent {
		seen[find(id)] = true
	}
	predicted := len(seen)
	exp := s.Expect.IndependentGroups
	v, _ := json.Marshal(struct {
		Predicted int     `json:"Predicted"`
		Expected  int     `json:"Expected"`
		Accuracy  float64 `json:"Accuracy"`
	}{predicted, exp, accOf(predicted == exp)})
	return MetricResult{Name: "M1", Value: string(v), Status: StatusDerived}
}

func evDomainMap(state ObservedState) map[model.EvidenceID]string {
	m := map[model.EvidenceID]string{}
	for _, ev := range state.Evidence {
		m[ev.ID] = string(ev.SourceID)
	}
	return m
}

func pairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "|" + b
}

// M2 — support_false_positive_rate: by unordered domain pair, Independent flag from topology.
func computeM2(s Scenario, state ObservedState) MetricResult {
	dom := evDomainMap(state)
	pairs := map[string]bool{}
	var fp, tp int
	for _, e := range state.Edges {
		if string(e.Kind) != string(model.EvidenceRelationSupports) {
			continue
		}
		a := dom[e.From]
		b := dom[e.To]
		if a == b {
			continue
		}
		key := pairKey(a, b)
		if pairs[key] {
			continue
		}
		pairs[key] = true
		if independentFor(a, b, s) {
			tp++
		} else {
			fp++
		}
	}
	total := fp + tp
	v, _ := json.Marshal(struct {
		FP    int     `json:"FP"`
		TP    int     `json:"TP"`
		Total int     `json:"Total"`
		Rate  float64 `json:"Rate"`
	}{fp, tp, total, mergeRate(fp, total)})
	return MetricResult{Name: "M2", Value: string(v), Status: StatusDerived}
}

// M3 — contradiction_detection_rate: ground-truth CONTRADICT topology pairs with
// an observed CONTRADICTS edge (undirected) / total.
func computeM3(s Scenario, state ObservedState) MetricResult {
	dom := evDomainMap(state)
	observed := map[string]bool{}
	for _, e := range state.Edges {
		if string(e.Kind) == string(model.EvidenceRelationContradicts) {
			observed[pairKey(dom[e.From], dom[e.To])] = true
		}
	}
	var total, detected int
	for _, t := range s.Topology {
		if t.Relation != "CONTRADICT" {
			continue
		}
		total++
		if observed[pairKey(t.A, t.B)] {
			detected++
		}
	}
	v, _ := json.Marshal(struct {
		Detected int     `json:"Detected"`
		Total    int     `json:"Total"`
		Rate     float64 `json:"Rate"`
	}{detected, total, mergeRate(detected, total)})
	return MetricResult{Name: "M3", Value: string(v), Status: StatusDerived}
}

// M4 — paraphrase_dedup_recall: ground-truth DUPLICATE pairs whose domains land in
// the same SUPPORTS/DUPLICATES component.
func computeM4(s Scenario, state ObservedState) MetricResult {
	dom := evDomainMap(state)
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if parent[x] == "" {
			parent[x] = x
			return x
		}
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}
	for _, ev := range state.Evidence {
		find(string(ev.SourceID))
	}
	for _, e := range state.Edges {
		k := string(e.Kind)
		if k == string(model.EvidenceRelationSupports) || k == string(model.EvidenceRelationDuplicates) {
			union(dom[e.From], dom[e.To])
		}
	}
	var total, collapsed int
	for _, t := range s.Topology {
		if t.Relation != "DUPLICATE" {
			continue
		}
		total++
		if find(t.A) == find(t.B) {
			collapsed++
		}
	}
	v, _ := json.Marshal(struct {
		Collapsed int     `json:"Collapsed"`
		Total     int     `json:"Total"`
		Recall    float64 `json:"Recall"`
	}{collapsed, total, mergeRate(collapsed, total)})
	return MetricResult{Name: "M4", Value: string(v), Status: StatusDerived}
}

// M5 — saturation_novelty_slope: least-squares slope of per-task novelty ratio.
func computeM5(s Scenario, state ObservedState) MetricResult {
	_ = state
	taskDomains := map[model.TaskID]map[string]bool{}
	var taskOrder []model.TaskID
	seenTask := map[model.TaskID]bool{}
	for _, src := range s.Sources {
		t := src.TaskID
		if t == "" {
			t = model.TaskID(fmt.Sprintf("task-%s", s.ID))
		}
		if !seenTask[t] {
			seenTask[t] = true
			taskOrder = append(taskOrder, t)
		}
		if taskDomains[t] == nil {
			taskDomains[t] = map[string]bool{}
		}
		taskDomains[t][src.Domain] = true
	}
	seenBefore := map[string]bool{}
	var novelty []float64
	for _, t := range taskOrder {
		total := len(taskDomains[t])
		newCount := 0
		for d := range taskDomains[t] {
			if !seenBefore[d] {
				newCount++
			}
		}
		for d := range taskDomains[t] {
			seenBefore[d] = true
		}
		if total == 0 {
			novelty = append(novelty, 1.0)
		} else {
			novelty = append(novelty, float64(newCount)/float64(total))
		}
	}
	window := len(taskOrder)
	slope := leastSquaresSlope(novelty)
	v, _ := json.Marshal(struct {
		Slope   float64   `json:"Slope"`
		Window  int       `json:"Window"`
		Novelty []float64 `json:"Novelty"`
	}{slope, window, novelty})
	return MetricResult{Name: "M5", Value: string(v), Status: StatusInferred}
}

func leastSquaresSlope(ys []float64) float64 {
	n := len(ys)
	if n == 0 {
		return 0.0
	}
	var sx, sy, sxx, sxy float64
	for i, y := range ys {
		x := float64(i)
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	denom := float64(n)*sxx - sx*sx
	if denom == 0 {
		return 0.0
	}
	return (float64(n)*sxy - sx*sy) / denom
}

// M6 — verification_state_correctness: re-derive expected state independently and
// compare to the system-stored VerificationState.
func computeM6(s Scenario, state ObservedState) MetricResult {
	qm := qualityMap(s, state.Evidence)
	expCounts := map[string]int{}
	obsCounts := map[string]int{}
	var matches, total int
	for _, ev := range state.Evidence {
		total++
		expState := deriveState(ev, state.Edges, qm)
		obsState := state.Verifications[ev.ID]
		expCounts[string(expState)]++
		obsCounts[string(obsState)]++
		if expState == obsState {
			matches++
		}
	}
	acc := 0.0
	if total > 0 {
		acc = float64(matches) / float64(total)
	}
	v, _ := json.Marshal(struct {
		Matches  int            `json:"Matches"`
		Total    int            `json:"Total"`
		Accuracy float64        `json:"Accuracy"`
		Expected map[string]int `json:"Expected"`
		Observed map[string]int `json:"Observed"`
	}{matches, total, acc, expCounts, obsCounts})
	return MetricResult{Name: "M6", Value: string(v), Status: StatusDerived}
}

func deriveState(ev model.Evidence, edges []model.EvidenceRelation, qm map[model.EvidenceID]float64) model.VerificationState {
	var contradicts, supports int
	hasHQ := false
	for _, e := range edges {
		if e.From != ev.ID && e.To != ev.ID {
			continue
		}
		kk := string(e.Kind)
		switch kk {
		case string(model.EvidenceRelationContradicts):
			contradicts++
		case string(model.EvidenceRelationSupports):
			supports++
			if e.To == ev.ID {
				if q, ok := qm[e.From]; ok && q >= evidence.HighQualityThreshold {
					hasHQ = true
				}
			}
		}
	}
	if contradicts >= evidence.KContradictions {
		return model.VerificationDisputed
	}
	if hasHQ && contradicts == 0 {
		return model.VerificationVerified
	}
	if supports > 0 || contradicts > 0 {
		return model.VerificationPartiallyVerified
	}
	return model.VerificationUnverified
}

func independentFor(a, b string, s Scenario) bool {
	for _, t := range s.Topology {
		if (t.A == a && t.B == b) || (t.A == b && t.B == a) {
			return t.Independent
		}
	}
	return true
}
