# Experiment 3 — Resource Cost Measurement of Counts() Overhead

**Branch:** `experiment/p9-counts-fix`  
**Commit:** `3cf1b1f` (fix) → measured Before/After  
**Method:** instrumented SQLite `Query` call-count driver + `testing.Benchmark` allocation sampling over the `storeEvidenceReader.Counts()` path in `internal/master/evidence.go:26-48`.

## Motivation

The Counts() fix (commit `3cf1b1f`, `internal/master/evidence.go`) changed
`storeEvidenceReader.Counts` from a single-query stub that returned
`EvidenceCounts{Contradictions: len(evs)}` (only querying
`VerificationDisputed`) to a three-query loop that also resolves
`MissingPrimary` (UNVERIFIED) and `StaleSources` (PARTIALLY_VERIFIED).

Experiment 3 isolates the **pure Counts() overhead** — i.e. the extra SQLite
queries and CPU/alloc cost attributable *only* to the fix — versus the much
larger cost of the *correctly-activated replans* that the fix enables (new
Discover/FetchHTTP/Verify task dispatches that did not run before).

## Methodology

- **Before**: original `Counts()` — 1 query per `RefreshEvidenceCounts` call
  (`master.go:549`), only `Contradictions` populated; `MissingPrimary` and
  `StaleSources` always return 0 → no replan from those triggers.
- **After**: fixed `Counts()` — 3 queries per call (one per verification state)
  populating all three dimensions.
- Each `Counts()` call happens once per task result in `Master.updateProgress`
  (`master.go:540-550`). The task-count delta between Before and After therefore
  directly multiplies the query delta.
- "Queries" = `storage.SQLiteEvidenceStore.Query` invocations traced through
  the counting driver wrapper (same wrapper introduced by the `p3-resource-metrics`
  line). "alloc" = `testing.B` reported heap allocations attributed to the
  `Counts()` call stack. "live" = `runtime.MemStats.HeapInuse` sampled at the
  same points.

All measurements are single-run snapshots on the measurement host; timing is
non-deterministic but query counts and allocation deltas are reproducible.

## Results

### Three-trigger scenario (3 DISPUTED + 3 UNVERIFIED + 3 PARTIALLY_VERIFIED)

Both Before and After execute 9 tasks (Contradiction trigger fires first in both
cases; MissingPrimary/StaleSource are shadowed). The only difference is the
Counts() query fan-in.

| Metric            | Before (1 query) | After (3 queries) | Delta        |
|---|---|---|---|
| SQLite queries    | 25               | 43               | +18 (+72 %)  |
| Latency (mean)    | ~2.09 ms         | ~2.72 ms         | +0.63 ms (+30 %) |
| Heap alloc        | ~172 KB          | ~250 KB          | +78 KB (+45 %)  |
| Heap live         | ~780 KB          | ~865 KB          | +85 KB (+11 %)  |

### MissingPrimary-only scenario (3 UNVERIFIED)

Before: no replan (MissingPrimary=0 → no trigger). After: MissingPrimary=3 ≥ 2
→ trigger fires → 6 replan-issued Discover tasks + 1 seed = 7 tasks.

| Metric            | Before (1 task)  | After (7 tasks)  | Delta        |
|---|---|---|---|
| SQLite queries    | 5                | 25               | +20 (+400 %) |
| Latency (mean)    | ~0.52 ms         | ~0.997 ms        | +0.477 ms (+91 %) |
| Heap alloc        | ~27 KB           | ~158 KB          | +131 KB (+485 %) |
| Heap live         | ~645 KB          | ~789 KB          | +144 KB (+22 %) |

### StaleSource-only scenario (3 PARTIALLY_VERIFIED)

Before: no replan (StaleSources=0 → no trigger). After: StaleSources=3 ≥ 2
→ trigger fires → 4 replan-issued FetchHTTP tasks + 1 seed Discover = 5 tasks.

| Metric            | Before (1 task)  | After (5 tasks)  | Delta        |
|---|---|---|---|
| SQLite queries    | 8                | 22               | +14 (+175 %) |
| Latency (mean)    | ~0.54 ms         | ~1.01 ms         | +0.47 ms (+86 %) |
| Heap alloc        | ~66 KB           | ~96 KB           | +30 KB (+45 %)  |
| Heap live         | ~684 KB          | ~738 KB          | +54 KB (+8 %)  |

## Query-count reconciliation

The per-call Counts() delta is exactly **+2 queries** (3 After − 1 Before).
Multiplying by the number of `RefreshEvidenceCounts` invocations (one per task
result in `updateProgress`) plus the baseline non-Counts queries explains the
total deltas:

| Scenario          | Tasks (Before→After) | Counts() calls (Before→After) | Query delta |
|---|---|---|---|
| Three-trigger     | 9 → 9              | 9 → 9              | 9 × 2 = 18 |
| MissingPrimary    | 1 → 7              | 1 → 7              | (7×3) − (1×1) = 20 |
| StaleSource       | 1 → 5              | 1 → 5              | (5×3) − (1×1) = 14 |

## Assessment

The **pure Counts() overhead** is the +2 queries per `RefreshEvidenceCounts`
call. In the three-trigger scenario (where task counts are identical Before and
After because the Contradiction trigger already fired), the overhead is **+18
queries / +0.63 ms / +78 KB alloc** — negligible relative to end-to-end session
runtime.

The **large deltas** in the MissingPrimary-only and StaleSource-only scenarios
(+400 %, +175 % query growth) are **not** overhead — they are the cost of the
*correctly-activated replans* that the fix enables. Without the fix,
MissingPrimary and StaleSources were always 0, so no replan was ever scheduled
from those triggers and only the single seed task ran. The fix turns a
1-task stub into a bounded 5–7 task replan loop, which is the intended behavioral
correction, not a regression.
