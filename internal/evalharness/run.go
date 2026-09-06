package evalharness

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"draw/internal/evidence"
	"draw/internal/model"
)

// RunV1OnScenario injects the scenario's evidence, computes relations + verification
// (driving the real internal/evidence package), persists via the in-memory store, then
// performs the R3 read-back path and returns the ObservedState.
//
// RunV1OnScenario is the single entry point where each scenario executes, so it is
// also where performance/resource measurements are taken: execution time
// (time.Since), memory (runtime.ReadMemStats before/after) and a per-scenario DB
// query count (via the counting driver wired through testStore.queryCount). These
// are recorded on st.perf and surfaced in the report's performance section; they
// do not alter any of the M1-M7 calculations or their (deterministic) values.
func RunV1OnScenario(t *testing.T, s Scenario, st *testStore) ObservedState {
	t.Helper()
	st.sid = model.SessionID(fmt.Sprintf("session-%s", s.ID))
	st.tid = model.TaskID(fmt.Sprintf("task-%s", s.ID))

	// The DB query counter accumulates everything the store has done so far
	// (including schema migrations applied in setupTestStore). Reset it here so
	// the recorded value reflects ONLY this scenario's execution.
	if st.queryCount != nil {
		atomic.StoreInt64(st.queryCount, 0)
	}

	// Clean baseline for memory measurement, taken outside the timed window.
	runtime.GC()
	var memStart runtime.MemStats
	runtime.ReadMemStats(&memStart)
	start := time.Now()

	allEvid := putEvidence(t, s, st)
	edges := evidence.ComputeRelations(allEvid, allEvid)
	if err := st.ES.PutRelations(edges); err != nil {
		t.Fatalf("put relations: %v", err)
	}
	qm := qualityMap(s, allEvid)
	for i := range allEvid {
		allEvid[i].Verification = evidence.ComputeVerification(
			allEvid[i], edges,
			evidence.HighQualityThreshold, evidence.KContradictions, qm)
		if _, err := st.ES.Put(allEvid[i]); err != nil {
			t.Fatalf("put verification state: %v", err)
		}
	}
	appendSyntheticEvents(t, st, st.sid)
	state := readBack(t, st, s)

	elapsed := time.Since(start)
	var memEnd runtime.MemStats
	runtime.ReadMemStats(&memEnd)

	st.perf = PerfMetrics{
		Elapsed:    elapsed,
		AllocDelta: memEnd.TotalAlloc - memStart.TotalAlloc,
		LiveHeap:   memEnd.Alloc,
		Sys:        memEnd.Sys,
		DBQueries:  atomic.LoadInt64(st.queryCount),
	}
	return state
}

// determinismSignature canonicalises an ObservedState for M7 with IDs and
// timestamps zeroed: EvidenceIDs are remapped to dense node indices, CollectedAt
// is excluded from nodes, Event.ID/TS are excluded, and all lists are sorted.
func determinismSignature(state ObservedState) string {
	sorted := make([]model.Evidence, len(state.Evidence))
	copy(sorted, state.Evidence)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].SourceID != sorted[j].SourceID {
			return sorted[i].SourceID < sorted[j].SourceID
		}
		if sorted[i].Claim != sorted[j].Claim {
			return sorted[i].Claim < sorted[j].Claim
		}
		return sorted[i].Value < sorted[j].Value
	})
	idx := make(map[model.EvidenceID]int, len(sorted))
	for i, ev := range sorted {
		idx[ev.ID] = i
	}
	type node struct {
		SourceID, Topic, Claim, Value, Verification string
		Confidence                                  float64
	}
	nodes := make([]node, 0, len(sorted))
	for _, ev := range sorted {
		nodes = append(nodes, node{
			SourceID: string(ev.SourceID), Topic: ev.Topic, Claim: ev.Claim, Value: ev.Value,
			Confidence: ev.Confidence, Verification: string(ev.Verification),
		})
	}
	type edge struct {
		From, To int
		Kind     string
		Strength float64
	}
	edges := make([]edge, 0, len(state.Edges))
	for _, e := range state.Edges {
		edges = append(edges, edge{From: idx[e.From], To: idx[e.To], Kind: string(e.Kind), Strength: e.Strength})
	}
	sort.SliceStable(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		if edges[i].To != edges[j].To {
			return edges[i].To < edges[j].To
		}
		return edges[i].Kind < edges[j].Kind
	})
	type evt struct{ Kind, Level, Message, Data string }
	evts := make([]evt, 0, len(state.Events))
	for _, e := range state.Events {
		evts = append(evts, evt{Kind: e.Kind, Level: e.Level, Message: e.Message, Data: e.Data})
	}
	sort.SliceStable(evts, func(i, j int) bool {
		if evts[i].Kind != evts[j].Kind {
			return evts[i].Kind < evts[j].Kind
		}
		if evts[i].Level != evts[j].Level {
			return evts[i].Level < evts[j].Level
		}
		if evts[i].Message != evts[j].Message {
			return evts[i].Message < evts[i].Message
		}
		return evts[i].Data < evts[j].Data
	})
	var b strings.Builder
	fmt.Fprintf(&b, "EVIDENCE:%d\n", len(nodes))
	for _, n := range nodes {
		fmt.Fprintf(&b, "  %s|%s|%s|%s|%g|%s\n", n.SourceID, n.Topic, n.Claim, n.Value, n.Confidence, n.Verification)
	}
	fmt.Fprintf(&b, "EDGES:%d\n", len(edges))
	for _, e := range edges {
		fmt.Fprintf(&b, "  %d|%d|%s|%g\n", e.From, e.To, e.Kind, e.Strength)
	}
	fmt.Fprintf(&b, "EVENTS:%d\n", len(evts))
	for _, e := range evts {
		fmt.Fprintf(&b, "  %s|%s|%s|%s\n", e.Kind, e.Level, e.Message, e.Data)
	}
	return b.String()
}
