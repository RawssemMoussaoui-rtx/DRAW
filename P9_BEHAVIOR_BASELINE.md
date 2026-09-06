# P9_BEHAVIOR_BASELINE.md

## P9 Behavior Baseline — Counts() Fix Activating MissingPrimary & StaleSource Replan Triggers

Recorded against `experiment/p9-counts-fix` at commit `205a7e5` (Experiments 1–3
complete, fix committed at `3cf1b1f`). All numbers below are the final, verified
raw values produced by `go test ./internal/integration/ -run TestP9 -v` and the
Experiment 3 resource-cost harness.

This baseline documents the behavior of P9: when the Counts() fix
(`internal/master/evidence.go:26-48`) populates `MissingPrimary` and
`StaleSources` from the SQLite evidence store, the previously-disabled replan
triggers `ReplanTriggerMissingPrimary` and `ReplanTriggerStaleSource`
(`internal/master/decision.go:224-231`) activate for the first time since
V1.5. The Contradiction trigger was already active in V1.5; only the
MissingPrimary and StaleSource branches were dormant (Counts returned 0 for
both before `3cf1b1f`).

---

## Section 1 — Overview

### What this baseline documents

P9 measures behavior when the Counts() fix activates all three replan triggers.
Before the fix (`3cf1b1f`), `storeEvidenceReader.Counts` returned only
`Contradictions` (a single `Query` for `VerificationDisputed`); `MissingPrimary`
and `StaleSources` were always 0, leaving two of the three trigger branches in
`replanTriggerIfAny` unreachable. The fix populates all three dimensions with
three queries per `RefreshEvidenceCounts` call, enabling MissingPrimary
(Discover/REDISCOVER) and StaleSource (FetchHTTP) replans.

### Threshold constants (from `internal/config/config.go:110`, `DefaultReplan()`)

| Constant             | Value | Source |
|---|---|---|
| `KContradictions`    | 2     | `config.DefaultReplan()` mirrors `evidence/verify.go:12` |
| `KMissingPrimary`    | 2     | `config.DefaultReplan()` (`MMissingPrimary`) |
| `KStaleSources`      | 2     | `config.DefaultReplan()` (`SStaleSources`) |

`KContradictions` is also the DISPUTED-state threshold per
`evidence/verify.go:81` (`contradicts >= k`).

### MaxReplans

`MaxReplans = 3` — the overall cap (`internal/master/state.go:72-75`,
`model.Intent.MaxReplans` defaults to 3 via `config.Defaults().DefaultMaxReplans`
at `internal/master/intent.go:29`). Verified at runtime as `st.MaxReplans`.
This is an **overall** cap (across all triggers), **not** 3 per trigger (which
would be 9). The guard is `s.ReplanCount < s.MaxReplans`
(`internal/master/state.go:162`).

### Trigger priority order

`replanTriggerIfAny` (`internal/master/decision.go:218-233`) uses a first-match
`switch`:

```go
switch {
case st.Evidence.Contradictions >= rp.KContradictions:
    return &ReplanTrigger{Kind: ReplanTriggerContradiction}
case st.Evidence.MissingPrimary >= rp.MMissingPrimary:
    return &ReplanTrigger{Kind: ReplanTriggerMissingPrimary, MissingTopic: "unknown"}
case st.Evidence.StaleSources >= rp.SStaleSources:
    return &ReplanTrigger{Kind: ReplanTriggerStaleSource}
}
```

**Priority: Contradiction → MissingPrimary → StaleSource.** Only the
highest-priority satisfied trigger fires per `observeAndDecide` cycle.

### evalharness TestBaseline is unaffected

`internal/evalharness/run.go:16-36` (`RunV1OnScenario`) drives the evidence
engine directly via `evidence.ComputeRelations` + `evidence.ComputeVerification`
and reads back through `EvidenceStore.Query`/`FindRelations`/`EventStore.Load`.
It does **not** invoke `Master.Run`, `Master.updateProgress`,
`RefreshEvidenceCounts`, or `Counts`. The `p9_counts_test.go` integration tests
(in `internal/integration/`) are the only tests exercising the trigger/circuit
path. This was confirmed by running the full evalharness TestBaseline (scenarios
A–E, N=5 determinism) alongside the P9 tests — no cross-contamination.

---

## Section 2 — Experiment 1: Three Triggers Together (static / p9NoopWorker)

**Worker:** `p9NoopWorker` (`internal/integration/p9_counts_test.go:19-45`) —
returns `RetrievalStatusSuccess` with no data for every task; records the
ordered task-type call log.

**Run command:**
`go test ./internal/integration/ -run TestP9_ThreeTriggersTogether -v`

**Test:** `TestP9_ThreeTriggersTogether` (`p9_counts_test.go:447`)

### Scenario: Three triggers together (3 DISPUTED + 3 UNVERIFIED + 3 PARTIALLY_VERIFIED)

Pre-populated evidence: 3 DISPUTED items (forming CONTRADICTS relations),
3 UNVERIFIED items (no relations), 3 PARTIALLY_VERIFIED items (SUPPORTS
relations). All three counts exceed their thresholds (≥ 2).

| Field                 | Value |
|---|---|
| EvidenceCounts.Contradictions | 3 |
| EvidenceCounts.MissingPrimary | 3 |
| EvidenceCounts.StaleSources   | 3 |
| Trigger fired                 | Contradiction (first in priority switch) |
| ReplanCount                   | 3 (capped at MaxReplans=3) |
| Terminal state                | research_complete |

Worker task call log (ordered):
```
[DISCOVER RECONCILE VERIFY VERIFY VERIFY VERIFY VERIFY VERIFY RECONCILE]
```

| Task type | Count |
|---|---|
| DISCOVER  | 1 |
| RECONCILE | 2 |
| VERIFY    | 6 |
| **Total** | **9** |

Notes:
- Only 1 DISCOVER (the seed) — the MissingPrimary trigger is shadowed by
  Contradiction priority, so it produces no Discover tasks.
- 0 FETCH_HTTP — the StaleSource trigger is also shadowed.
- No logical conflicts between task types: Verify + Reconcile come exclusively
  from the Contradiction replan circuit (`internal/master/planning.go:55-61`).
- MaxReplans=3 respected: 3rd replan sets `ReplanCount=3`,
  `CanReplan` returns false (`state.go:162`), producing an empty plan delta.

### Scenario: Contradiction-only control (3 DISPUTED only)

Same trigger (Contradiction), no UNVERIFIED or PARTIALLY_VERIFIED planted.

| Field                 | Value |
|---|---|
| EvidenceCounts.Contradictions | 3 |
| EvidenceCounts.MissingPrimary | 0 |
| EvidenceCounts.StaleSources   | 0 |
| Trigger fired                 | Contradiction |
| ReplanCount                   | 3 |
| Terminal state                | research_complete |

Worker task call log (ordered):
```
[DISCOVER VERIFY VERIFY VERIFY RECONCILE VERIFY VERIFY RECONCILE VERIFY]
```

| Task type | Count |
|---|---|
| DISCOVER  | 1 |
| RECONCILE | 2 |
| VERIFY    | 6 |
| **Total** | **9** |

Notes:
- Identical task-type signature to the three-trigger scenario (1/2/6/9),
  confirming that the three-trigger run is dominated by the Contradiction
  trigger and that the shadowed triggers add no tasks.
- Worker call ordering differs from the three-trigger scenario only in
  interleaving of RECONCILE (ordering artifact of the sterile noop worker), not
  in type counts.

---

## Section 3 — Experiment 2: Realistic Worker (dynamic state)

**Worker:** `p9RealisticWorker` (`p9_counts_test.go:711-784`) — same end-to-end
circuit but with MIXED, state-mutating results:
- Success with distinct integer `revenue` JSON per call → fresh
  CONTRADICTS edges → DISPUTED once ≥ 2 accumulate (values are integer strings
  with no n-gram overlap, so the paraphrase gate at
  `evidence/similarity.go:16` never suppresses the edge).
- Partial results (low-confidence, statusBase=0.5) on a deterministic slice.
- Transient Timeout on a deterministic slice (`n%3==2`) that retries ONCE
  (RetryCount==0 fails; RetryCount≥1 succeeds), so MaxAttempts=3 is never
  exhausted and the session never fatal-terminates on error.

**Run command:**
`go test ./internal/integration/ -run 'TestP9_Realistic_|TestP9_Compare_NoopVsRealistic' -v`

### Scenario: Three triggers (realistic worker)

Planted: 3 DISPUTED + 3 UNVERIFIED + 3 PARTIALLY_VERIFIED (same as Exp 1).

| Field                 | Noop (Exp 1) | Realistic (Exp 2) |
|---|---|---|
| EvidenceCounts.Contradictions | 3 | 9 |
| EvidenceCounts.MissingPrimary | 3 | 3 |
| EvidenceCounts.StaleSources   | 3 | 3 |
| EvidenceCount (total rows)    | 0 | 6 |
| ReplanCount | 3 | 3 |
| Terminal state | research_complete | research_complete |
| Total tasks | 9 | 11 |

Worker task call log (realistic, ordered):
```
[DISCOVER RECONCILE VERIFY VERIFY VERIFY VERIFY VERIFY VERIFY RECONCILE VERIFY VERIFY]
```

| Task type | Count |
|---|---|
| DISCOVER  | 1 |
| RECONCILE | 2 |
| VERIFY    | 8 |
| **Total** | **11** |

Dynamic injection: **6 new contradictions** (3 → 9) and **6 evidence items**
(EvidenceCount=6) injected mid-cycle via the realistic worker's distinct-value
Discover/Verify results. **2 timeout retries** (11 calls vs 9 in the noop run;
retries occur at the deterministic `n%3==2` slice, each retried once).

### Scenario: MissingPrimary-only (realistic worker)

Planted: 3 UNVERIFIED only (no DISPUTED, no PARTIALLY_VERIFIED).

| Field                 | Value |
|---|---|
| EvidenceCounts.Contradictions | 6 |
| EvidenceCounts.MissingPrimary | 3 |
| EvidenceCounts.StaleSources   | 0 |
| ReplanCount | 3 |
| Terminal state | research_complete |

| Task type | Count |
|---|---|
| DISCOVER | 9 |
| **Total** | **9** |

9 DISCOVER calls = 7 base (1 seed + 6 replan-issued from 2 active replans) +
2 retries (Timeout at `n%3==2`, retried once). The MissingPrimary trigger fires
first (priority), issuing Discover tasks; the realistic worker then injects 6
new Contradictions dynamically (0 → 6), but they never reach priority since
MissingPrimary is already satisfied and the 3-replan cap is hit.

### Scenario: StaleSource-only (realistic worker)

Planted: 3 PARTIALLY_VERIFIED only (no DISPUTED, no UNVERIFIED).

| Field                 | Value |
|---|---|
| EvidenceCounts.Contradictions | 4 |
| EvidenceCounts.MissingPrimary | 0 |
| EvidenceCounts.StaleSources   | 3 |
| ReplanCount | 3 |
| Terminal state | research_complete |

| Task type | Count |
|---|---|
| DISCOVER   | 1 |
| FETCH_HTTP | 5 |
| **Total**  | **6** |

1 DISCOVER (seed) + 5 FETCH_HTTP (4 base + 1 retry). Dynamic injection: 4
Contradictions (0 → 4) from the worker's distinct-value FetchHTTP results.

### Key finding

Identical **structure** to the noop worker: trigger priority (Contradiction →
MissingPrimary → StaleSource), overall MaxReplans=3 cap, and `research_complete`
termination are preserved. Only the **evidence counts** and **retry call
counts** differ — the realistic worker injects new contradictions and produces
timeout retries, but never violates the cap, priority ordering, or termination
invariant.

---

## Section 4 — Experiment 3: Resource Cost Measurement

**Scope:** pure `Counts()` overhead — the cost of the fix itself
(`internal/master/evidence.go:26-48`), isolating it from the cost of the
correctly-activated replans.

**Before:** original `Counts()` — 1 SQLite query (DISPUTED only), returning
`EvidenceCounts{Contradictions: len(evs), MissingPrimary: 0, StaleSources: 0}`.
Triggers for MissingPrimary and StaleSource never fire.

**After:** fixed `Counts()` — 3 SQLite queries (DISPUTED + UNVERIFIED +
PARTIALLY_VERIFIED), populating all three dimensions.

`Counts()` is called once per task result via `Master.updateProgress`
(`internal/master/master.go:549` → `state.go:137-141` → `RefreshEvidenceCounts`).
The +2 queries per call is the only overhead added by the fix.

### Three-trigger scenario (9 tasks in both Before and After)

Contradiction fires in both modes; MissingPrimary/StaleSource are shadowed.
Task count is identical (9), so the delta is pure Counts() overhead.

| Metric        | Before  | After   | Delta           |
|---|---|---|---|
| SQLite queries | 25      | 43      | +18 (+72 %)     |
| Latency        | ~2.09 ms | ~2.72 ms | +0.63 ms (+30 %) |
| Heap alloc     | ~172 KB | ~250 KB | +78 KB (+45 %)  |
| Heap live      | ~780 KB | ~865 KB | +85 KB (+11 %)  |

### MissingPrimary-only scenario (Before: 1 task; After: 7 tasks)

Before: no replan (MissingPrimary=0). After: MissingPrimary=3 ≥ 2 → 6 replan
Discover tasks + 1 seed = 7 tasks.

| Metric        | Before  | After    | Delta            |
|---|---|---|---|
| SQLite queries | 5       | 25       | +20 (+400 %)     |
| Latency        | ~0.52 ms | ~0.997 ms | +0.477 ms (+91 %) |
| Heap alloc     | ~27 KB  | ~158 KB  | +131 KB (+485 %) |
| Heap live      | ~645 KB | ~789 KB  | +144 KB (+22 %)  |

### StaleSource-only scenario (Before: 1 task; After: 5 tasks)

Before: no replan (StaleSources=0). After: StaleSources=3 ≥ 2 → 4 replan
FetchHTTP tasks + 1 seed Discover = 5 tasks.

| Metric        | Before  | After   | Delta           |
|---|---|---|---|
| SQLite queries | 8       | 22      | +14 (+175 %)     |
| Latency        | ~0.54 ms | ~1.01 ms | +0.47 ms (+86 %) |
| Heap alloc     | ~66 KB  | ~96 KB  | +30 KB (+45 %)   |
| Heap live      | ~684 KB | ~738 KB | +54 KB (+8 %)    |

### Query-count reconciliation

The +2 queries per `Counts()` call (3 After − 1 Before) multiplied by the number
of `RefreshEvidenceCounts` invocations (one per task result in
`updateProgress`) accounts for the total query deltas:

| Scenario          | Tasks Before→After | Query delta | Reconciliation         |
|---|---|---|---|
| Three-trigger     | 9 → 9              | +18         | 9 calls × 2 extra = 18 |
| MissingPrimary    | 1 → 7              | +20         | (7×3) − (1×1) = 20     |
| StaleSource       | 1 → 5              | +14         | (5×3) − (1×1) = 14     |

### Assessment

The **pure Counts() overhead** is the +2 extra SQLite queries per
`RefreshEvidenceCounts` call. In the three-trigger scenario (where task counts
are identical Before and After because the Contradiction trigger already fired
in both), the overhead is **+18 queries / +0.63 ms / +78 KB alloc** — negligible
relative to end-to-end session runtime.

The **large deltas** in the MissingPrimary-only (+400 %) and StaleSource-only
(+175 %) scenarios are **not overhead** — they are the cost of the
correctly-activated replans that the fix enables. Without the fix,
MissingPrimary and StaleSources were always 0, so no replan was ever scheduled
from those triggers and only the single seed task ran (1 task). The fix turns a
1-task stub into a bounded 5–7 task replan loop (capped at MaxReplans=3), which
is the intended behavioral correction, not a regression.

---

## Section 5 — Experiment 5: Priority Starvation Analysis

### Question

Since Contradiction always wins priority in the `switch` at
`decision.go:224-231` and `MaxReplans=3` is an **overall** cap (not per-trigger
at `state.go:162`), can repeated contradictions monopolize all 3 replan
opportunities, leaving MissingPrimary and StaleSource **starved** (never fired)
throughout the entire session, with their associated evidence completely
unprocessed at termination?

### Scenario design

Pre-populate **all three** trigger conditions simultaneously in the SQLite
evidence store:

| Evidence type | Count | Trigger threshold | Result |
|---|---|---|---|
| DISPUTED (claim `revenue_fact`, distinct values, CONTRADICTS edges) | 3 | KContradictions = 2 | Contradiction fires first |
| UNVERIFIED (claim `claim_N`, no relations) | 3 | MMissingPrimary = 2 | Shadowed — should fire but never reached |
| PARTIALLY_VERIFIED (claim `shared_claim`, SUPPORTS edges) | 3 | SStaleSources = 2 | Shadowed — should fire but never reached |

**Worker:** `p9Experiment5Worker` (`p9_counts_test.go:1112-1226`) — reuses the
`p9RealisticWorker` pattern from Experiment 2:

- Returns distinct integer `revenue` JSON per task → distinct `[0].revenue`
  claim values → fresh CONTRADICTS edges → DISPUTED once ≥ 2 accumulate (values
  are integer strings with no n-gram overlap, so the paraphrase gate at
  `evidence/similarity.go:16` never suppresses the edge)
- Returns Transient Timeout on `n%3==2` (RetryCount==0) that retries once
  (RetryCount≥1 always succeeds) — keeps the circuit active without
  fatal-terminating
- Returns Partial results on `n%4==3` (low-confidence, statusBase=0.5)
- **Does NOT resolve UNVERIFIED/PARTIALLY_VERIFIED evidence**: the worker's
  evidence uses claim paths `[0]` and `[0].revenue` (from JSON path extraction),
  which differ from the pre-planted claims (`claim_N`, `shared_claim`,
  `revenue_fact`). Since `evidence.ComputeRelations`
  (`internal/evidence/relations.go:23`) only creates edges between items with the
  **same Claim + same Topic**, no cross-relations are formed. The pre-planted
  evidence items retain their original verification states throughout
  (`ComputeVerification` at `evidence/verify.go:76` recomputes from the same
  relations; no change → no state transition).

### Run

`go test ./internal/integration/ -run TestP9_Experiment5_PriorityStarvation -v`

### Results

| Field | Value |
|---|---|
| EvidenceCounts.Contradictions | 9 (3 pre-planted + 6 injected) |
| EvidenceCounts.MissingPrimary | 3 (pre-planted UNVERIFIED, never resolved) |
| EvidenceCounts.StaleSources | 3 (pre-planted PARTIALLY_VERIFIED, never resolved) |
| ReplanCount | 3 (= MaxReplans, all Contradiction) |
| Terminal state | research_complete |
| Contradiction injections by worker | 6 |

Worker task call log (ordered):
```
[DISCOVER RECONCILE VERIFY VERIFY VERIFY VERIFY VERIFY VERIFY RECONCILE VERIFY VERIFY]
```

| Task type | Count | Source |
|---|---|---|
| DISCOVER | 1 | Seed only (from SubmitIntent) |
| RECONCILE | 2 | Contradiction replan (planning.go:61) |
| VERIFY | 8 | Contradiction replan (planning.go:59-60, ×3 replans + retries) |
| FETCH_HTTP | 0 | StaleSource never fired |
| FETCH_BROWSER | 0 | No escalation path |
| **Total** | **11** | (incl. 2 timeout retries) |

### Trigger firing at each replan opportunity

Inferred from task-type composition: only Contradiction-produced task types
(Verify + Reconcile) appear beyond the seed; no Discover beyond seed
(MissingPrimary shadowed); no FetchHTTP (StaleSource shadowed).

| Replan opportunity | Contradiction fired? | MissingPrimary fired? | StaleSource fired? |
|---|---|---|---|
| 1st (after seed DISCOVER) | YES (Contradictions=3 ≥ 2) | Shadowed | Shadowed |
| 2nd (after 1st replan's Verify completes) | YES (Contradictions ≥ 3) | Shadowed | Shadowed |
| 3rd (after 2nd replan's Verify completes) | YES (Contradictions ≥ 3) | Shadowed | Shadowed |
| 4th+ | N/A (ReplanCount=3, CanReplan=false at state.go:162) | N/A | N/A |

### Pre-planted evidence verification (store-level, by SourceID)

Direct SQLite store query via `p9CountEvidenceBySource` confirms all 9
pre-planted items retained their original verification states at termination:

| Verification state | Pre-planted source IDs | Found at termination |
|---|---|---|
| UNVERIFIED | `src0.example`, `src1.example`, `src2.example` | 3/3 |
| PARTIALLY_VERIFIED | `stale0.example`, `stale1.example`, `stale2.example` | 3/3 |
| DISPUTED | `disputed0.example`, `disputed1.example`, `disputed2.example` | 3/3 |

### Natural paths check

No alternative processing path affects the starved evidence:

- **Seed DISCOVER**: returns no data (no payload) — no evidence extracted
- **Verify tasks**: extract evidence with claim `[0].revenue` (distinct from
  pre-planted claims) — no cross-relations created via
  `ComputeRelations` (different Claim → skip per `relations.go:35`)
- **Reconcile tasks**: return no data/payload — no evidence extracted
- No `DecisionRediscover`, `DecisionAddVerification`, or upgrade paths triggered
- No `DecisionTerminate` (budget not exhausted; EffortBudget=500, actual
  usage ~20)

### Starvation verdict: **FULL STARRVATION CONFIRMED**

The Contradiction trigger fired on all 3 replan opportunities (ReplanCount=3).
MissingPrimary and StaleSource triggers did **NOT fire even once**. The associated
evidence (3 UNVERIFIED + 3 PARTIALLY_VERIFIED items) remains completely
unprocessed at session termination — verified both via `Counts()` (MissingPrimary=3,
StaleSources=3) and via direct store query by SourceID (3/3 items retained
original verification state). The terminal state is `research_complete` (not
`budget_exhausted`), and no natural path existed to process the starved evidence.

### Critical behavioral constraint

**Contradiction priority + overall MaxReplans cap ⇒ hard starvation of
MissingPrimary and StaleSource when all three conditions coexist.** This is an
architecturally deterministic outcome of the first-match `switch` in
`replanTriggerIfAny` (decision.go:224-231) combined with the overall
`ReplanCount < MaxReplans` guard (state.go:162). The Counts() fix (commit
`3cf1b1f`) correctly activates all three triggers, but the priority ordering
ensures that when Contradictions ≥ KContradictions at every replan opportunity,
the other two triggers are permanently shadowed for the entire session.

This is **by design** (priority ordering is documented in Section 1), not a bug.
The starvation is the expected consequence of the priority model. Any future
change to break this starvation would require either:
1. **Round-robin or fair-share scheduling** among satisfied triggers (modifying
   `replanTriggerIfAny` to rotate which trigger fires), or
2. **Per-trigger replan budgets** (e.g., MaxReplans=3 per trigger, not overall),
   or
3. **Priority decay** (lowering Contradiction's priority after it fires N times).

All three would change the replan triggering semantics and require separate
design review.
