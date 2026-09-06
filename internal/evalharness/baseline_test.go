package evalharness

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"draw/internal/model"
)

// reportTimestamp is a fixed constant (P13): NO time.Now() anywhere in report
// generation, so the rendered report is byte-identical across repeated runs.
const reportTimestamp = "2026-08-23T16:06:51Z"

func commitHash() string {
	if b, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		return strings.TrimSpace(string(b))
	}
	return "unknown-commit"
}

func expM1(pred, exp int) string {
	b, _ := json.Marshal(struct {
		Predicted int     `json:"Predicted"`
		Expected  int     `json:"Expected"`
		Accuracy  float64 `json:"Accuracy"`
	}{pred, exp, accOf(pred == exp)})
	return string(b)
}

func expM2(fp, tp, total int) string {
	b, _ := json.Marshal(struct {
		FP    int     `json:"FP"`
		TP    int     `json:"TP"`
		Total int     `json:"Total"`
		Rate  float64 `json:"Rate"`
	}{fp, tp, total, mergeRate(fp, total)})
	return string(b)
}

func expM3(detected, total int) string {
	b, _ := json.Marshal(struct {
		Detected int     `json:"Detected"`
		Total    int     `json:"Total"`
		Rate     float64 `json:"Rate"`
	}{detected, total, mergeRate(detected, total)})
	return string(b)
}

func expM4(collapsed, total int) string {
	b, _ := json.Marshal(struct {
		Collapsed int     `json:"Collapsed"`
		Total     int     `json:"Total"`
		Recall    float64 `json:"Recall"`
	}{collapsed, total, mergeRate(collapsed, total)})
	return string(b)
}

func expM5(slope float64, window int, novelty []float64) string {
	b, _ := json.Marshal(struct {
		Slope   float64   `json:"Slope"`
		Window  int       `json:"Window"`
		Novelty []float64 `json:"Novelty"`
	}{slope, window, novelty})
	return string(b)
}

func expM6(matches, total int, exp, obs map[string]int) string {
	acc := 0.0
	if total > 0 {
		acc = float64(matches) / float64(total)
	}
	b, _ := json.Marshal(struct {
		Matches  int            `json:"Matches"`
		Total    int            `json:"Total"`
		Accuracy float64        `json:"Accuracy"`
		Expected map[string]int `json:"Expected"`
		Observed map[string]int `json:"Observed"`
	}{matches, total, acc, exp, obs})
	return string(b)
}

func expM7(eq bool) string {
	b, _ := json.Marshal(struct {
		Equal bool `json:"Equal"`
	}{eq})
	return string(b)
}

func assertMetric(t *testing.T, sc string, m MetricResult, want string) {
	t.Helper()
	if m.Value != want {
		t.Errorf("Scenario %s %s: got %s, want %s", sc, m.Name, m.Value, want)
	}
}

func TestBaseline(t *testing.T) {
	commit := commitHash()
	results := collectResults(t)
	report := renderReport(results, commit)
	if err := os.WriteFile("../../BASELINE_RESULTS.md", []byte(report), 0644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	t.Log("report written to ../../BASELINE_RESULTS.md (deterministic; no time.Now)")
}

// collectResults runs every scenario twice (for M7 determinism) and returns the
// ScenarioResult slice (metrics + perf) that drives renderReport. Factorised
// out of TestBaseline so TestPerfReportDeterminism can reuse it to compare two
// independent report generations.
func collectResults(t *testing.T) []ScenarioResult {
	t.Helper()
	var results []ScenarioResult
	for _, s := range allScenarios() {
		st1 := setupTestStore(t)
		state1 := RunV1OnScenario(t, s, &st1)
		st2 := setupTestStore(t)
		state2 := RunV1OnScenario(t, s, &st2)

		sig1 := determinismSignature(state1)
		sig2 := determinismSignature(state2)
		m7equal := sig1 == sig2

		metrics := computeMetrics(s, state1)
		metrics = append(metrics, MetricResult{Name: "M7", Value: expM7(m7equal), Status: StatusObserved})

		sup := countEdges(state1.Edges, string(model.EvidenceRelationSupports))
		con := countEdges(state1.Edges, string(model.EvidenceRelationContradicts))
		dup := countEdges(state1.Edges, string(model.EvidenceRelationDuplicates))
		t.Logf("Scenario %s edges: SUPPORTS=%d CONTRADICTS=%d DUPLICATES=%d", s.ID, sup, con, dup)

		if !m7equal {
			t.Fatalf("Scenario %s M7 determinism FAILED: signatures differ", s.ID)
		}

		mm := map[string]MetricResult{}
		for _, m := range metrics {
			mm[m.Name] = m
		}
		assertAllMetrics(t, s, mm, sup, con, dup)

		results = append(results, ScenarioResult{ScenarioID: s.ID, Metrics: metrics, Perf: st1.perf})
	}
	return results
}

// semanticSection returns the report portion that must be byte-identical across
// runs: the header, per-scenario M1-M7 metrics and the Findings. Everything
// from the performance section onward is non-deterministic (timing/memory vary)
// and is excluded from the comparison.
func semanticSection(report string) string {
	idx := strings.Index(report, "\n## Performance & Resource Measurements")
	if idx < 0 {
		return report
	}
	return report[:idx]
}

// TestPerfReportDeterminism generates the baseline report TWICE and asserts that
// the semantic parts (M1-M7 + Findings) are byte-identical between the two
// runs, while the new performance section carries reasonable, non-negative
// numbers and a structurally-stable DB query count.
func TestPerfReportDeterminism(t *testing.T) {
	commit := commitHash()

	results1 := collectResults(t)
	results2 := collectResults(t)

	sem1, sem2 := semanticSection(renderReport(results1, commit)), semanticSection(renderReport(results2, commit))
	if sem1 != sem2 {
		t.Errorf("semantic report (M1-M7 + Findings) is NOT byte-identical across two runs\n")
		n := 0
		for i := 0; i < len(sem1) && i < len(sem2); i++ {
			if sem1[i] != sem2[i] {
				n++
			}
		}
		t.Logf("first-differing bytes: %d, lengths: %d vs %d", n, len(sem1), len(sem2))
		t.Logf("semantic1:\n%s\n---\nsemantic2:\n%s", sem1, sem2)
	} else {
		t.Logf("semantic report (M1-M7 + Findings) is byte-identical across two runs (%d bytes)", len(sem1))
	}

	for i, r := range results1 {
		p, q := r.Perf, results2[i].Perf
		if r.ScenarioID != results2[i].ScenarioID {
			t.Fatalf("scenario order mismatch: %s vs %s", r.ScenarioID, results2[i].ScenarioID)
		}
		if p.DBQueries <= 0 {
			t.Errorf("scenario %s: DBQueries=%d, want >0", r.ScenarioID, p.DBQueries)
		}
		if p.AllocDelta <= 0 {
			t.Errorf("scenario %s: AllocDelta=%d, want >0", r.ScenarioID, p.AllocDelta)
		}
		if p.LiveHeap <= 0 {
			t.Errorf("scenario %s: LiveHeap=%d, want >0", r.ScenarioID, p.LiveHeap)
		}
		if p.DBQueries != q.DBQueries {
			t.Errorf("scenario %s: DBQueries not stable across runs (%d vs %d)", r.ScenarioID, p.DBQueries, q.DBQueries)
		}
		t.Logf("scenario %s perf: queries=%d elapsed=%s alloc=%d live=%d",
			r.ScenarioID, p.DBQueries, formatDuration(p.Elapsed), p.AllocDelta, p.LiveHeap)
	}
}

func assertAllMetrics(t *testing.T, s Scenario, mm map[string]MetricResult, sup, con, dup int) {
	t.Helper()
	ID := string(s.ID)
	switch s.ID {
	case "A":
		assertMetric(t, ID, mm["M1"], expM1(1, 1))
		assertMetric(t, ID, mm["M2"], expM2(1, 5, 6))
		assertMetric(t, ID, mm["M3"], expM3(0, 0))
		assertMetric(t, ID, mm["M4"], expM4(0, 0))
		assertMetric(t, ID, mm["M5"], expM5(0.0, 1, []float64{1}))
		assertMetric(t, ID, mm["M6"], expM6(4, 4, map[string]int{"VERIFIED": 4}, map[string]int{"VERIFIED": 4}))
		assertMetric(t, ID, mm["M7"], expM7(true))
		if sup != 12 {
			t.Errorf("A SUPPORTS=%d want 12", sup)
		}
		if con != 0 {
			t.Errorf("A CONTRADICTS=%d want 0", con)
		}
		if dup != 0 {
			t.Errorf("A DUPLICATES=%d want 0", dup)
		}
	case "B":
		assertMetric(t, ID, mm["M1"], expM1(3, 3))
		assertMetric(t, ID, mm["M2"], expM2(0, 0, 0))
		assertMetric(t, ID, mm["M3"], expM3(0, 0))
		assertMetric(t, ID, mm["M4"], expM4(0, 0))
		assertMetric(t, ID, mm["M5"], expM5(0.0, 1, []float64{1}))
		assertMetric(t, ID, mm["M6"], expM6(3, 3, map[string]int{"UNVERIFIED": 3}, map[string]int{"UNVERIFIED": 3}))
		assertMetric(t, ID, mm["M7"], expM7(true))
		if sup != 0 || con != 0 || dup != 0 {
			t.Errorf("B edges: S=%d C=%d D=%d want 0", sup, con, dup)
		}
	case "C":
		assertMetric(t, ID, mm["M1"], expM1(2, 2))
		assertMetric(t, ID, mm["M2"], expM2(0, 0, 0))
		assertMetric(t, ID, mm["M3"], expM3(1, 1))
		assertMetric(t, ID, mm["M4"], expM4(0, 0))
		assertMetric(t, ID, mm["M5"], expM5(0.0, 1, []float64{1}))
		assertMetric(t, ID, mm["M6"], expM6(2, 2, map[string]int{"DISPUTED": 2}, map[string]int{"DISPUTED": 2}))
		assertMetric(t, ID, mm["M7"], expM7(true))
		if con != 2 {
			t.Errorf("C CONTRADICTS=%d want 2", con)
		}
		if sup != 0 || dup != 0 {
			t.Errorf("C S=%d D=%d want 0", sup, dup)
		}
	case "D":
		assertMetric(t, ID, mm["M1"], expM1(6, 6))
		// NOTE: M2 is intentionally NOT asserted for Scenario D. The original
		// scenarios.go fixture (D.02000.json, which pins a documented M2 target of
		// 20) is absent from this workspace, so the star-composition fixture
		// actually materialised here yields a different M2 value. Asserting M2
		// would hard-code an artifact of the star topology rather than a verified
		// invariant, and would silently regress if the fixture is later restored.
		// Per owner ruling: do not assert M2 on D.
		t.Logf("Scenario D M2 not asserted (fixture gap): %s", mm["M2"].Value)
		assertMetric(t, ID, mm["M3"], expM3(0, 0))
		assertMetric(t, ID, mm["M4"], expM4(0, 0))
		novD := []float64{1, 2.0 / 3.0, 1.0 / 3.0, 0.25, 0.2, 1.0 / 6.0}
		assertMetric(t, ID, mm["M5"], expM5(leastSquaresSlope(novD), 6, novD))
		assertMetric(t, ID, mm["M6"], expM6(24, 24, map[string]int{"PARTIALLY_VERIFIED": 24}, map[string]int{"PARTIALLY_VERIFIED": 24}))
		assertMetric(t, ID, mm["M7"], expM7(true))
		if sup != 80 {
			t.Errorf("D SUPPORTS=%d want 80", sup)
		}
		if con != 0 || dup != 0 {
			t.Errorf("D C=%d D=%d want 0", con, dup)
		}
	case "E":
		assertMetric(t, ID, mm["M1"], expM1(2, 1))
		assertMetric(t, ID, mm["M2"], expM2(0, 0, 0))
		assertMetric(t, ID, mm["M3"], expM3(0, 0))
		assertMetric(t, ID, mm["M4"], expM4(0, 1))
		assertMetric(t, ID, mm["M5"], expM5(0.0, 1, []float64{1}))
		assertMetric(t, ID, mm["M6"], expM6(2, 2, map[string]int{"UNVERIFIED": 2}, map[string]int{"UNVERIFIED": 2}))
		assertMetric(t, ID, mm["M7"], expM7(true))
		if sup != 0 || con != 0 || dup != 0 {
			t.Errorf("E edges: S=%d C=%d D=%d want 0 (paraphrase gate suppresses)", sup, con, dup)
		}
	}
}

func renderReport(results []ScenarioResult, commit string) string {
	var b strings.Builder
	b.WriteString("# Baseline measurement of unmodified V1\n\n")
	b.WriteString("Reproducer: internal/evalharness TestBaseline (P13: deterministic, no time.Now).\n\n")
	b.WriteString("Timestamp (UTC): " + reportTimestamp + "\n")
	b.WriteString("Git commit: " + commit + "\n\n")
	for _, r := range results {
		b.WriteString("## Scenario " + string(r.ScenarioID) + "\n\n")
		for _, m := range r.Metrics {
			b.WriteString(m.Name + ": " + m.Value + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("## Findings\n\n")
	b.WriteString("- Scenario A: chain of duplicated/derived sources. 12 SUPPORTS edges, 4 VERIFIED. M1 accuracy=1, M2 FP=1/TP=5.\n")
	b.WriteString("- Scenario B: genuinely independent sources. 0 edges, 3 UNVERIFIED. M1=3.\n")
	b.WriteString("- Scenario C: known contradiction. 2 CONTRADICTS edges (incident count=2 >= K=2), 2 DISPUTED. M3 detected.\n")
	b.WriteString("- Scenario D: saturation. 80 SUPPORTS edges across 6 independent groups; declining novelty; 24 PARTIALLY_VERIFIED. M2 not asserted (see note).\n")
	b.WriteString("- Scenario E: paraphrase. ValueSimilarity=0.857 >= tau=0.8 suppresses the CONTRADICTS edge -> 0 edges -> UNVERIFIED (corrected per P12; the stale V1.0 prose that said DISPUTED has been reconciled to UNVERIFIED).\n")

	b.WriteString("\n## Performance & Resource Measurements\n\n")
	b.WriteString("Per-scenario measurements taken around `RunV1OnScenario` (the single scenario entry point). " +
		"Timing uses time.Since; memory uses runtime.ReadMemStats before/after; DB queries are counted via a " +
		"counting SQLite driver wrapper (see perf.go) whose only behaviour is incrementing a counter — it is " +
		"wired purely within this package and touches no Tier B / production file. Migrations applied in " +
		"setupTestStore are excluded (the counter is reset at the start of RunV1OnScenario).\n\n")
	b.WriteString("Time and memory are inherently non-deterministic and are expected to differ between runs; " +
		"the M1-M7 section above is deterministic (no time.Now) and should be byte-identical across runs. " +
		"The DB query count is structural and is likewise stable across runs.\n\n")
	for _, r := range results {
		p := r.Perf
		b.WriteString("### Scenario " + string(r.ScenarioID) + "\n")
		fmt.Fprintf(&b, "- Elapsed: %s\n", formatDuration(p.Elapsed))
		fmt.Fprintf(&b, "- Allocated (TotalAlloc delta): %d bytes\n", p.AllocDelta)
		fmt.Fprintf(&b, "- Live heap (Alloc): %d bytes\n", p.LiveHeap)
		fmt.Fprintf(&b, "- DB queries: %d\n", p.DBQueries)
		b.WriteString("\n")
	}
	b.WriteString("## Performance measurement methodology\n\n")
	b.WriteString("- Elapsed: time.Since(start) around the full RunV1OnScenario body (inject evidence, " +
		"ComputeRelations, persist edges, ComputeVerification, persist verification, append synthetic " +
		"events, R3 read-back).\n")
	b.WriteString("- Allocated (TotalAlloc delta): runtime.MemStats.TotalAlloc difference measured after a " +
		"runtime.GC baseline, capturing total bytes allocated during the scenario.\n")
	b.WriteString("- Live heap (Alloc): runtime.MemStats.Alloc immediately after the scenario (live heap).\n")
	b.WriteString("- DB queries: atomic counter incremented on each Exec/Query dispatched through the " +
		"counting driver connection during the scenario.\n")
	return b.String()
}

// formatDuration renders a Duration as a fixed-style human string. Only the
// numeric value varies between runs; the format is stable.
func formatDuration(d time.Duration) string {
	if d == 0 {
		return "0s"
	}
	if d < time.Microsecond {
		return fmt.Sprintf("%d ns", int64(d))
	}
	if d < time.Millisecond {
		return fmt.Sprintf("%.3f us", float64(d)/float64(time.Microsecond))
	}
	return fmt.Sprintf("%.3f ms", float64(d)/float64(time.Millisecond))
}
