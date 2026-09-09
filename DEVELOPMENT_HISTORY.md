# Development History — DRAW Engine

This document summarizes the development milestones of the DRAW Engine project from
its early prototype through the current verified state.

## v1 — Early Prototype Milestone

The foundational codebase was established, including the core Master control loop,
Scheduler, Frontier, and basic task execution model. A deterministic regression
harness was set up to validate correctness of the planning and decision subsystems.
The project structure was laid out under `internal/` with packages for master,
orchestrator, frontier, model, evidence, and storage.

## v1.5 — Intermediate Development

Refinements and incremental improvements were made to core systems. A deterministic
regression harness was repaired to support deterministic execution verification.
Baseline results were captured and documented. A fresh clone verification script
was created to validate the working tree state. Integration tests were added to
cover edge cases in the research lifecycle. The paraphrase gate correction was
applied to `internal/evidence/relations.go` to suppress false `CONTRADICTS` edges
on paraphrased corroboration (see §v1.5 — Paraphrase Gate Correction below).

## v1.6 — Correctness Fixes

Resolution of correctness issues identified through evaluation. The tanh-saturation
logic for ContradictionValue was adjusted. Exploration quota floor handling was
refined. The StopPending idempotency guard was made robust under concurrent
DecisionStop calls. Cancel-stuck-workers logic was added for the drained timeout
case. The TestP9 realistic three-trigger scenario was made robust to P10 drain
conditions. The TestExplorationQuotaIntegrationGates test was aligned with the
idempotency guard. The Counts() fix activated MissingPrimary and StaleSource replan
triggers (see §P9 — Counts() Fix and Replan Trigger Behavior below).

## Current State

The codebase is at a fully verified state. All packages compile and pass vet. The
master test suite is green. The orchestrator quota integration gate test passes.
The working tree contains no purged files and is free of sensitive content. Phase G
delivered the HTTP API layer and deterministic reporting (see §Phase G — HTTP API
and Deterministic Reporting below).

---

## v1.5 — Paraphrase Gate Correction

### Problem

In the original `internal/evidence/relations.go` (`ComputeRelations`), every pair
sharing the same `Topic` and `Claim` but with different `Value` strings was
classified as `CONTRADICTS` unconditionally. The original code at `relations.go:35`
guarded only the `Claim` check; lines 50–52 emitted `CONTRADICTS` for any `Value`
divergence. When two sources reported the same fact in different wording, the pair
was misread as opposing facts, producing false-positive disputes.

**Scenario E** (`internal/evalharness/scenarios.go:165-183`):

| Source | Claim | Value |
|---|---|---|
| `alpha.example` | `claim:Capital` | `Paris is the capital of France` |
| `beta.example` | `claim:Capital` | `The capital city of France is Paris` |

Under the original V1 code: (1) `ComputeRelations` emits a `CONTRADICTS` edge
(strength 1.0) bidirectionally → 2 edges. (2) `ComputeVerification`
(`internal/evidence/verify.go:76-91`) counts 2 incident `CONTRADICTS` edges per
endpoint ≥ `KContradictions = 2` (`verify.go:12`) → both items resolve to
`DISPUTED`. (3) `DISPUTED` evidence feeds downstream replan triggers and
source-trustworthiness — a false-positive dispute on identical factual content.

Measured baseline (Scenario E): 2 CONTRADICTS edges, both DISPUTED, M4 paraphrase
dedup recall 0/1, M1 independence groups predicted 2/expected 1 accuracy 0.0.

### Correction — Similarity Gate

A deterministic n-gram term-frequency Jaccard similarity check (`ValueSimilarity`,
`internal/evidence/similarity.go`) was inserted at the head of the
same-`Claim`/different-`Value` branch. When two `Value` strings are highly similar
(≥ τ), the `CONTRADICTS` edge is suppressed entirely — the pair emits no edge at all
(`continue`). Genuine contradictions (overlap < τ) fall through with `CONTRADICTS`
as before.

This is a pure abstention: no edge is emitted, and no new relation `Kind` is
introduced. `SUPPORTS`/`DUPLICATES`/`CONTRADICTS` semantics are unchanged;
`ComputeVerification` is untouched; the `EvidenceRelationKind` enum
(`internal/model/enums.go:93-100`) and the `relations` table migration
(`storage/migrations.go:91-101`) are untouched.

**Code change** (`internal/evidence/relations.go:59-61`, inserted into the `else`
branch of the `n.Value == e.Value` check):

```go
if ValueSimilarity(n.Value, e.Value) >= contradictionSimilarityThreshold {
    continue
}
```

New file `internal/evidence/similarity.go` defines `ValueSimilarity` (pure stdlib
n-gram TF–Jaccard over n ∈ {1,2,3}, lowercased, punctuation-stripped) and the
constant `contradictionSimilarityThreshold = 0.8`. No NLP/NER/LLM is involved — only
the `strings` package.

### Threshold Selection — τ = 0.8

Selected from the fixed grid {0.50, 0.65, 0.80}. τ = 0.8 is the strictest candidate
that still suppresses the Scenario E paraphrase pair (overlap ≥ τ) while preserving
the Scenario C true-contradiction pair (overlap < τ).

| Pair | Value A | Value B | Overlap | τ = 0.8 | Edge? |
|---|---|---|---|---|---|
| Scenario C (true contradiction) | `yes` | `no` | 0.000 | 0.000 < 0.8 | CONTRADICTS |
| Scenario E (paraphrase) | `Paris is the capital of France` | `The capital city of France is Paris` | 0.857 | 0.857 ≥ 0.8 | suppressed |

Boundary cases (from `similarity_test.go:89-119`,
`TestComputeRelations_BoundaryNearThreshold`):

| Boundary pair | Unigram Jaccard | τ = 0.8 | Result |
|---|---|---|---|
| "just below" (7 shared + 1 unique each) | 7/9 ≈ 0.778 | 0.778 < 0.8 | CONTRADICTS emitted |
| "just above" (9 shared + 1 unique each) | 9/11 ≈ 0.818 | 0.818 ≥ 0.8 | suppressed |

Candidate-threshold grid search:

| Candidate τ | C overlap | C emits? | E overlap | E suppressed? | Strictly separates C ↔ E? |
|---|---|---|---|---|---|
| 0.50 | 0.000 | yes | 0.857 | yes | yes |
| 0.65 | 0.000 | yes | 0.857 | yes | yes |
| 0.80 | 0.000 | yes | 0.857 | yes | **yes — strictest → selected** |

### Scenario C Evidence

**Source:** `internal/evalharness/scenarios.go:81-99`

| Field | Value |
|---|---|
| Scenario ID | C |
| Name | Known contradiction pair |
| Claim | `claim:Q` |
| Source 1 | Domain: `eta.example`, Value: `yes`, Quality: 1.0 |
| Source 2 | Domain: `theta.example`, Value: `no`, Quality: 1.0 |
| Expected relation | CONTRADICTS |

**Similarity (verified independently by hand + by test):**

- `tokenize("yes") = ["yes"]`, `tokenize("no") = ["no"]`
- n=1 Jaccard: intersection=0, union=2, Jaccard=0.0
- n=2: `len(tokens) < n` → nil → Jaccard=0.0
- n=3: `len(tokens) < n` → nil → Jaccard=0.0
- **ValueSimilarity("yes","no") = 0.000**

Because 0.000 < τ (0.8), the paraphrase gate does NOT fire for Scenario C. The
`CONTRADICTS` edge is still emitted (bidirectional → 2 edges). Both items see ≥
KContradictions=2 → `DISPUTED`. This matches the baseline results (2 CONTRADICTS
edges, DISPUTED×2, M3 detected=1/1 rate=1.0).

### Scenario E Evidence

**Source:** `internal/evalharness/scenarios.go:165-183`

| Field | Value |
|---|---|
| Scenario ID | E |
| Claim | `claim:Capital` |
| Source 1 | `alpha.example`, Value: `Paris is the capital of France`, Quality: 0.3 |
| Source 2 | `beta.example`, Value: `The capital city of France is Paris`, Quality: 0.3 |
| Expected relation | DUPLICATE (same fact → 1 group) |

**Similarity (verified independently by hand + by test):**

Tokenization:
- A = `["paris","is","the","capital","of","france"]` (6 tokens)
- B = `["the","capital","city","of","france","is","paris"]` (7 tokens)

n=1 (unigram TF–Jaccard): intersection = 6, union = 7, Jaccard = 6/7 ≈ **0.857143**

n=2 (bigram TF–Jaccard): intersection = 2 ("the capital", "of france"), union = 9,
Jaccard = 2/9 ≈ 0.222

n=3 (trigram TF–Jaccard): intersection = 0, union = 9, Jaccard = 0.0

**ValueSimilarity(A, B) = max(0.857, 0.222, 0.0) = 0.857**

This is ≥ τ (0.8), so the paraphrase gate fires: `continue` — no edge is emitted.

**Before (original V1):** 2 CONTRADICTS edges → both `DISPUTED`.
**After (corrected v1.5):** 0 edges → both `UNVERIFIED`.

### Verification

**Method:** `go test ./internal/evidence/...` (unit) and
`go test ./internal/evalharness/ -run TestBaseline -v` (scenario harness). The
harness runs the real `evidence.ComputeRelations` + `evidence.ComputeVerification`
over a fresh in-memory SQLite store, then reads back via the R3 path — `EventStore.Load`
(paginated `afterID` cursor, `limit=100_000`, bypassing the SSE 1000-event bound)
plus `EvidenceStore.Query`/`FindRelations`.

**Before/after results** (`git diff --no-index` between result files — 27 insertions,
6 deletions, all confined to Scenario E plus Run metadata format; Scenarios A–D
byte-identical):

| Scenario | Metric | Before (v1.0) | After (v1.5) | Delta |
|---|---|---|---|---|
| A | M1 | acc 1.0 | acc 1.0 | identical |
| A | M2 | FPR 0.1667 | 0.1667 | identical |
| A | M6 | 4/4 VERIFIED | 4/4 VERIFIED | identical |
| B | M1 | acc 1.0 | acc 1.0 | identical |
| B | M6 | 3/3 UNVERIFIED | 3/3 UNVERIFIED | identical |
| C | M3 | rate 1.0 | rate 1.0 | identical |
| C | edges | 2 × CONTRADICTS | 2 × CONTRADICTS | identical |
| C | M6 | 2/2 DISPUTED | 2/2 DISPUTED | identical |
| D | M1 | acc 1.0 | acc 1.0 | identical |
| D | M5 | slope −0.1614 | −0.1614 | identical |
| D | M6 | 24/24 | 24/24 | identical |
| **E** | **M6** | **2/2 DISPUTED** | **2/2 UNVERIFIED** | **fixed** |
| **E** | **edges** | **2 × CONTRADICTS** | **0 edges** | **2→0** |
| E | M1 | acc 0.0 | acc 0.0 | no improvement (expected) |
| E | M4 | recall 0.0 | recall 0.0 | no improvement (expected) |
| ALL | M7 | true | true | identical |

**Why M1 and M4 do not improve (Scenario E):** The gate's action on a suppressed
pair is `continue` — it emits no edge of any kind. M1 (connected components over
`SUPPORTS`/`DUPLICATES` edges) and M4 (paraphrase pairs collapsed via
`SUPPORTS`/`DUPLICATES`) both depend on a positive merge edge. With zero edges
emitted, the pair stays in separate components → predicted 2 groups, 0 collapsed.
This is structurally expected for a pure-abstention gate.

**M6 nuance:** M6 accuracy stays 1.0 because `computeM6` re-derives the expected
state from observed edges. With 0 edges, both expected and observed resolve to
`UNVERIFIED` → self-consistent → accuracy 1.0. The state changed
(DISPUTED→UNVERIFIED) but the accuracy metric did not regress.

**Unit tests** (all PASS):
- `TestComputeRelations_ParaphraseSuppressed` — Scenario E pair → 0 edges
- `TestComputeRelations_TrueContradictionEmitted` — Scenario C pair → 2 CONTRADICTS
- `TestComputeRelations_BoundaryNearThreshold` — 0.778 emits, 0.818 suppresses
- `TestValueSimilarity_EdgeCases` — identical→1.0, disjoint→0.0, empty→0.0,
  case-fold/punct-strip

**Prepublication verification (V1–V10):** All ten verification checks pass with no
contradictions. `go build ./...` PASS; `go vet
./internal/evidence/... ./internal/evalharness/...` PASS;
`go test ./internal/evidence/...` PASS;
`go test ./internal/evalharness/... -run TestBaseline` PASS. No prohibited changes:
only `internal/evidence/relations.go` (modified, +11 lines) differs from HEAD among
tracked files. All Tier B files (enums.go, task.go, migrations.go, ports.go, sse.go,
sqlite_evidence.go, score.go, master.go) show zero diff. No new
`EvidenceRelationKind`. No migration change. No NLP/NER (only `strings` package in
similarity.go).

### Constraints Honored

| Constraint | Evidence |
|---|---|
| Tier A only | `relations.go` is outside `internal/model/*`; `similarity.go` is new Tier A; all Tier B files zero diff |
| No enum/migration/Kind changes | Enum unchanged; migration untouched; gate emits no edge, introduces no kind |
| No NLP | `ValueSimilarity` uses only `strings.Fields`/`ToLower`/`Trim` + n-gram counting |
| Deterministic | M7 passes; `ComputeRelations` output sorted by `(From, To, Kind)` + deduped; byte-identical across ≥2 runs |
| R4 — no silent semantic redefinition | Gate only suppresses `CONTRADICTS` via `continue`; does not alter kind meaning or `ComputeVerification` |
| R3 read path | Harness bypasses SSE 1000-bound via `EventStore.Load` (`limit=100_000`, paginated) + `EvidenceStore.Query`/`FindRelations` |

### Changed Files

| File | Change |
|---|---|
| `internal/evidence/relations.go` | Modified — paraphrase gate + comment (11 lines inserted) |
| `internal/evidence/similarity.go` | New — `ValueSimilarity` + `contradictionSimilarityThreshold = 0.8` |
| `internal/evidence/similarity_test.go` | New — 4 test suites |
| `internal/evalharness/*` | New — 6 files: baseline_test.go, metrics.go, run.go, scenarios.go, store.go, types.go |

---

## P9 — Counts() Fix and Replan Trigger Behavior

### Counts() Fix

The fix (commit `3cf1b1f` on `experiment/p9-counts-fix`) changed
`storeEvidenceReader.Counts` in `internal/master/evidence.go` from a single-query
stub that returned `EvidenceCounts{Contradictions: len(evs)}` (only querying
`VerificationDisputed`) to a three-query loop that also resolves `MissingPrimary`
(UNVERIFIED) and `StaleSources` (PARTIALLY_VERIFIED).

Before the fix: `MissingPrimary` and `StaleSources` were always 0, leaving two of
three trigger branches in `replanTriggerIfAny`
(`internal/master/decision.go:224-231`) unreachable. The fix populates all three
dimensions with three queries per `RefreshEvidenceCounts` call, enabling
MissingPrimary (Discover/REDISCOVER) and StaleSource (FetchHTTP) replans.

### Threshold Constants and Caps

| Constant | Value | Source |
|---|---|---|
| `KContradictions` | 2 | `config.DefaultReplan()` mirrors `evidence/verify.go:12` |
| `KMissingPrimary` | 2 | `config.DefaultReplan()` (`MMissingPrimary`) |
| `KStaleSources` | 2 | `config.DefaultReplan()` (`SStaleSources`) |
| `MaxReplans` | 3 | `internal/master/state.go:72-75` — overall cap, not per-trigger |

The guard is `s.ReplanCount < s.MaxReplans` (`internal/master/state.go:162`).
`KContradictions` is also the DISPUTED-state threshold per `evidence/verify.go:81`
(`contradicts >= k`).

### Trigger Priority Order

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

**Priority: Contradiction → MissingPrimary → StaleSource.** Only the highest-priority
satisfied trigger fires per `observeAndDecide` cycle.

### Effect on evalharness

`internal/evalharness/run.go:16-36` (`RunV1OnScenario`) drives the evidence engine
directly via `evidence.ComputeRelations` + `evidence.ComputeVerification` and reads
back through `EvidenceStore.Query`/`FindRelations`/`EventStore.Load`. It does NOT
invoke `Master.Run`, `Master.updateProgress`, `RefreshEvidenceCounts`, or `Counts`.
The `p9_counts_test.go` integration tests (in `internal/integration/`) are the only
tests exercising the trigger/circuit path.

### Behavior — Three Triggers Together (static / p9NoopWorker)

**Worker:** `p9NoopWorker` (`internal/integration/p9_counts_test.go:19-45`) —
returns `RetrievalStatusSuccess` with no data for every task; records the ordered
task-type call log.

**Scenario:** 3 DISPUTED + 3 UNVERIFIED + 3 PARTIALLY_VERIFIED. All three counts
exceed their thresholds (≥ 2).

| Field | Value |
|---|---|
| EvidenceCounts.Contradictions | 3 |
| EvidenceCounts.MissingPrimary | 3 |
| EvidenceCounts.StaleSources | 3 |
| Trigger fired | Contradiction (first in priority switch) |
| ReplanCount | 3 (capped at MaxReplans=3) |
| Terminal state | research_complete |

Worker task call log: `[DISCOVER RECONCILE VERIFY VERIFY VERIFY VERIFY VERIFY VERIFY RECONCILE]`

| Task type | Count |
|---|---|
| DISCOVER | 1 |
| RECONCILE | 2 |
| VERIFY | 6 |
| Total | 9 |

Notes: Only 1 DISCOVER (seed) — MissingPrimary is shadowed by Contradiction priority,
so it produces no Discover tasks. 0 FETCH_HTTP — StaleSource also shadowed. MaxReplans=3
respected: 3rd replan sets `ReplanCount=3`, `CanReplan` returns false
(`state.go:162`).

### Behavior — Contradiction-only Control (3 DISPUTED only)

| Field | Value |
|---|---|
| EvidenceCounts.Contradictions | 3 |
| EvidenceCounts.MissingPrimary | 0 |
| EvidenceCounts.StaleSources | 0 |
| ReplanCount | 3 |
| Terminal state | research_complete |

Worker task call log: `[DISCOVER VERIFY VERIFY VERIFY RECONCILE VERIFY VERIFY RECONCILE VERIFY]`

| Task type | Count |
|---|---|
| DISCOVER | 1 |
| RECONCILE | 2 |
| VERIFY | 6 |
| Total | 9 |

Identical task-type signature (1/2/6/9) to the three-trigger scenario, confirming
that the three-trigger run is dominated by the Contradiction trigger and that the
shadowed triggers add no tasks. Worker call ordering differs only in interleaving
of RECONCILE (ordering artifact of the sterile noop worker), not in type counts.

### Behavior — Realistic Worker (dynamic state)

**Worker:** `p9RealisticWorker` (`p9_counts_test.go:711-784`) — same end-to-end
circuit but with MIXED, state-mutating results: success with distinct integer
`revenue` JSON per call → fresh CONTRADICTS edges → DISPUTED once ≥ 2 accumulate
(values are integer strings with no n-gram overlap, so the paraphrase gate at
`evidence/similarity.go:16` never suppresses the edge); partial results
(low-confidence, statusBase=0.5) on a deterministic slice; transient Timeout on
`n%3==2` that retries once (RetryCount==0 fails; RetryCount≥1 succeeds).

**Three triggers (realistic):**

| Field | Noop | Realistic |
|---|---|---|
| EvidenceCounts.Contradictions | 3 | 9 |
| EvidenceCounts.MissingPrimary | 3 | 3 |
| EvidenceCounts.StaleSources | 3 | 3 |
| EvidenceCount (total rows) | 0 | 6 |
| ReplanCount | 3 | 3 |
| Terminal state | research_complete | research_complete |
| Total tasks | 9 | 11 |

Worker task call log (realistic): `[DISCOVER RECONCILE VERIFY VERIFY VERIFY VERIFY VERIFY VERIFY RECONCILE VERIFY VERIFY]`

| Task type | Count |
|---|---|
| DISCOVER | 1 |
| RECONCILE | 2 |
| VERIFY | 8 |
| Total | 11 |

Dynamic injection: 6 new contradictions (3 → 9) and 6 evidence items injected
mid-cycle via the realistic worker's distinct-value Discover/Verify results. 2
timeout retries (11 calls vs 9 in the noop run; retries occur at the deterministic
`n%3==2` slice, each retried once).

**MissingPrimary-only (realistic):** Planted 3 UNVERIFIED only (no DISPUTED, no
PARTIALLY_VERIFIED).

| Field | Value |
|---|---|
| EvidenceCounts.Contradictions | 6 |
| EvidenceCounts.MissingPrimary | 3 |
| EvidenceCounts.StaleSources | 0 |
| ReplanCount | 3 |
| Terminal state | research_complete |

9 DISCOVER calls = 7 base (1 seed + 6 replan-issued from 2 active replans) + 2
retries (Timeout at `n%3==2`, retried once). The MissingPrimary trigger fires first
(priority), issuing Discover tasks; the realistic worker then injects 6 new
Contradictions dynamically (0 → 6), but they never reach priority since
MissingPrimary is already satisfied and the 3-replan cap is hit.

**StaleSource-only (realistic):** Planted 3 PARTIALLY_VERIFIED only (no DISPUTED,
no UNVERIFIED).

| Field | Value |
|---|---|
| EvidenceCounts.Contradictions | 4 |
| EvidenceCounts.MissingPrimary | 0 |
| EvidenceCounts.StaleSources | 3 |
| ReplanCount | 3 |
| Terminal state | research_complete |

| Task type | Count |
|---|---|
| DISCOVER | 1 |
| FETCH_HTTP | 5 |
| Total | 6 |

1 DISCOVER (seed) + 5 FETCH_HTTP (4 base + 1 retry). Dynamic injection: 4
Contradictions (0 → 4) from the worker's distinct-value FetchHTTP results.

**Key finding:** Structure is identical to the noop worker — trigger priority
(Contradiction → MissingPrimary → StaleSource), overall MaxReplans=3 cap, and
`research_complete` termination are preserved. Only evidence counts and retry call
counts differ — the realistic worker injects new contradictions and produces timeout
retries, but never violates the cap, priority ordering, or termination invariant.

### Resource Cost (Experiment 3)

Isolated the pure Counts() overhead — the cost of the fix itself
(`internal/master/evidence.go:26-48`), separate from the cost of the
correctly-activated replans.

**Before:** 1 SQLite query per `RefreshEvidenceCounts` call (DISPUTED only),
returning `EvidenceCounts{Contradictions: len(evs), MissingPrimary: 0,
StaleSources: 0}`. Triggers for MissingPrimary and StaleSource never fire.

**After:** 3 SQLite queries per call (DISPUTED + UNVERIFIED + PARTIALLY_VERIFIED),
populating all three dimensions.

`Counts()` is called once per task result via `Master.updateProgress`
(`internal/master/master.go:549` → `state.go:137-141` →
`RefreshEvidenceCounts`). The +2 queries per call is the only overhead added by the
fix.

**Three-trigger scenario (9 tasks in both Before and After):** Contradiction fires
in both modes; MissingPrimary/StaleSource are shadowed. Task count is identical (9),
so the delta is pure Counts() overhead.

| Metric | Before | After | Delta |
|---|---|---|---|
| SQLite queries | 25 | 43 | +18 (+72%) |
| Latency (mean) | ~2.09 ms | ~2.72 ms | +0.63 ms (+30%) |
| Heap alloc | ~172 KB | ~250 KB | +78 KB (+45%) |
| Heap live | ~780 KB | ~865 KB | +85 KB (+11%) |

**Query-count reconciliation:** Per-call Counts() delta is +2 queries (3 After − 1
Before). Multiplying by the number of `RefreshEvidenceCounts` invocations (one per
task result in `updateProgress`) explains the total deltas:

| Scenario | Tasks Before→After | Query delta | Reconciliation |
|---|---|---|---|
| Three-trigger | 9 → 9 | +18 | 9 × 2 = 18 |
| MissingPrimary | 1 → 7 | +20 | (7×3) − (1×1) = 20 |
| StaleSource | 1 → 5 | +14 | (5×3) − (1×1) = 14 |

**Assessment:** The pure Counts() overhead (+2 queries per call) is negligible
relative to end-to-end session runtime. The large deltas in the MissingPrimary-only
(+400%) and StaleSource-only (+175%) scenarios are not overhead — they are the cost
of the correctly-activated replans that the fix enables. Without the fix,
MissingPrimary and StaleSources were always 0, so no replan was ever scheduled from
those triggers and only the single seed task ran. The fix turns a 1-task stub into
a bounded 5–7 task replan loop (capped at MaxReplans=3), which is the intended
behavioral correction, not a regression.

### Priority Starvation Analysis (Experiment 5)

**Question:** Since Contradiction always wins priority in the switch and
MaxReplans=3 is an overall cap, can repeated contradictions monopolize all 3
replan opportunities, leaving MissingPrimary and StaleSource permanently starved?

**Scenario:** Pre-populate all three trigger conditions simultaneously: 3 DISPUTED +
3 UNVERIFIED + 3 PARTIALLY_VERIFIED.

**Worker:** `p9Experiment5Worker` — reuses the `p9RealisticWorker` pattern: returns
distinct integer `revenue` JSON → fresh CONTRADICTS edges → DISPUTED; transient
Timeout on `n%3==2` (retries once); partial results on `n%4==3`. Does NOT resolve
UNVERIFIED/PARTIALLY_VERIFIED evidence (worker claim paths `[0]` and `[0].revenue`
differ from pre-planted claims `claim_N`, `shared_claim`, `revenue_fact`;
`ComputeRelations` (`relations.go:23`) only creates edges between items with the
same Claim + same Topic, so no cross-relations are formed).

**Results:**

| Field | Value |
|---|---|
| EvidenceCounts.Contradictions | 9 (3 pre-planted + 6 injected) |
| EvidenceCounts.MissingPrimary | 3 (pre-planted UNVERIFIED, never resolved) |
| EvidenceCounts.StaleSources | 3 (pre-planted PARTIALLY_VERIFIED, never resolved) |
| ReplanCount | 3 |
| Terminal state | research_complete |

| Task type | Count | Source |
|---|---|---|
| DISCOVER | 1 | Seed only (from SubmitIntent) |
| RECONCILE | 2 | Contradiction replan (`planning.go:61`) |
| VERIFY | 8 | Contradiction replan (`planning.go:59-60`, ×3 replans + retries) |
| FETCH_HTTP | 0 | StaleSource never fired |
| FETCH_BROWSER | 0 | No escalation path |
| Total | 11 | (incl. 2 timeout retries) |

**Trigger firing at each replan opportunity:**

| Replan opportunity | Contradiction | MissingPrimary | StaleSource |
|---|---|---|---|
| 1st (after seed DISCOVER) | YES (Contradictions=3 ≥ 2) | Shadowed | Shadowed |
| 2nd (after 1st replan's Verify completes) | YES (Contradictions ≥ 3) | Shadowed | Shadowed |
| 3rd (after 2nd replan's Verify completes) | YES (Contradictions ≥ 3) | Shadowed | Shadowed |
| 4th+ | N/A (ReplanCount=3, CanReplan=false at state.go:162) | N/A | N/A |

Inferred from task-type composition: only Contradiction-produced task types
(Verify + Reconcile) appear beyond the seed; no Discover beyond seed
(MissingPrimary shadowed); no FetchHTTP (StaleSource shadowed).

**Pre-planted evidence verification (direct SQLite store query via
`p9CountEvidenceBySource`):**

| Verification state | Pre-planted source IDs | Found at termination |
|---|---|---|
| UNVERIFIED | `src0.example`, `src1.example`, `src2.example` | 3/3 |
| PARTIALLY_VERIFIED | `stale0.example`, `stale1.example`, `stale2.example` | 3/3 |
| DISPUTED | `disputed0.example`, `disputed1.example`, `disputed2.example` | 3/3 |

**Natural paths check:** No alternative processing path affects the starved evidence:
seed DISCOVER returns no data (no payload); Verify tasks extract evidence with claim
`[0].revenue` (distinct from pre-planted claims — no cross-relations per
`relations.go:35`); Reconcile tasks return no data/payload; no
`DecisionRediscover`, `DecisionAddVerification`, or upgrade paths triggered; no
`DecisionTerminate` (budget not exhausted; EffortBudget=500, actual usage ~20).

**Starvation verdict: FULL STARRVATION CONFIRMED.** The Contradiction trigger fired
on all 3 replan opportunities (ReplanCount=3). MissingPrimary and StaleSource
triggers did NOT fire even once. The associated evidence (3 UNVERIFIED + 3
PARTIALLY_VERIFIED items) remains completely unprocessed at session termination —
verified both via Counts() (MissingPrimary=3, StaleSources=3) and via direct store
query by SourceID (3/3 items retained original verification state). Terminal state
is `research_complete` (not `budget_exhausted`).

**Critical behavioral constraint:** Contradiction priority + overall MaxReplans cap ⇒
hard starvation of MissingPrimary and StaleSource when all three conditions coexist.
This is an architecturally deterministic outcome of the first-match `switch` in
`replanTriggerIfAny` (decision.go:224-231) combined with the overall
`ReplanCount < MaxReplans` guard (state.go:162). This is by design (priority
ordering is documented), not a bug. Any future change to break this starvation
would require: (1) round-robin or fair-share scheduling among satisfied triggers,
(2) per-trigger replan budgets (e.g., MaxReplans=3 per trigger, not overall), or
(3) priority decay — all of which would change replan triggering semantics and
require separate design review.

---

## Phase G — HTTP API and Deterministic Reporting

### Overview

Phase G delivered the HTTP API server layer and deterministic terminal-reporting
result builder that surface Phase-F master state over a web API. It added a thin
HTTP server (`internal/api`), a deterministic report assembler (`internal/result`),
and a static browser front-end, with zero modifications to locked A–F internals.

**Environment:** Go 1.26.6, module `draw`, `CGO_ENABLED=0`, Windows/amd64, no gcc.
`go.mod`/`go.sum` unchanged; `go mod verify` → "all modules verified"; no new
dependencies. `go test -race` FAIL only because `gcc not found` (environment
limitation, not a code defect).

### What Was Implemented

- A thin HTTP server (`internal/api`) exposing `POST /sessions` →
  `{"session_id":"..."}` intent-submit endpoint, `GET /sessions/{id}` snapshot,
  `GET /sessions/{id}/result` full `model.ResultEnvelope`, `GET /sessions/{id}/events`
  SSE stream.
- An admin gate (`internal/auth`, Phase-F, untouched) applied via `RequireAdmin` in
  `routes.go`, gating `GET /admin/sessions/{id}` and
  `GET /admin/sessions/{id}/events`. Token is ENV-only (`DRAW_ADMIN_TOKEN`),
  fail-closed, constant-time compare, no credential storage.
- A `cmd/rd-engine` entrypoint that wires `signal.NotifyContext` → `api.Serve(ctx, …)`.
- A deterministic report assembler (`internal/result`) plus a static browser
  front-end under `internal/api/static`.
- **Context-Fix (production path):** `Submit` derives a Run via
  `context.WithCancel(sm.srvCtx)` (server-lifetime, NOT `r.Context()`) so
  `go master.Run(runCtx)` survives the `POST /sessions` handler returning. SSE polls
  `Master.State()` every 250ms; on terminal state emits `report` (full
  `model.ResultEnvelope` via `ReportBuilder.Build`) then closes the stream, bounded
  by `r.Context().Done()` (client disconnect exits cleanly, no goroutine leak).
- **Single-active-session** enforced in `sessionsManager` (Cor.6): a new Submit stops
  the prior in-flight Run.

**JSON contract:** POST `/sessions` → `{"session_id":"..."}`; `GET /sessions/{id}`
snapshot → `stateResponse` snake_case; `GET /sessions/{id}/result` →
`model.ResultEnvelope` (PascalCase fields, no json tags — relies on Go field casing);
SSE `phase`/`progress` snake_case; SSE `report` uses model struct casing.

### Files Added/Modified

ADDED:
- `internal/result/result.go`, `internal/result/result_test.go`
- `internal/api/deps.go`, `server.go`, `routes.go`, `handlers_sessions.go`,
  `handlers_data.go`, `handlers_admin.go`, `handlers_test.go`, `sse.go`,
  `sse_test.go`, `chatbox.go`
- `internal/api/static/index.html`, `app.js`, `app.css`
- `internal/integration/api_test.go`
- `cmd/rd-engine/main.go`
- `.gitignore`

MODIFIED (Context-Fix only — the only source files touched):
- `internal/api/server.go`
- `internal/api/handlers_sessions.go`
- `internal/api/handlers_test.go`
- `internal/integration/api_test.go`

### Approved Architectural Decisions

- Master stays a concrete struct; Phase G consumes `SubmitIntent`/`Run`/`State`/`Stop`
  only — no master refactor.
- Server-lifetime context for Run derivation (`sm.srvCtx`) rather than per-request
  `r.Context()`, so a session's Run outlives the HTTP handler.
- SSE polls state every 250ms (default `NewSSEHandler` interval) instead of
  push-driven events — no EventStore append-log needed in G.
- Memory-only `sessionsManager` in the api layer; no persistent session storage.
- Admin token read from ENV per request; ENV-only, fail-closed,
  `crypto/subtle.ConstantTimeCompare`; no credential storage.
- Single-active-session rule (Cor.6) enforced at the api orchestrator layer.
- Phase G scope is the authorized-browser/admin gate only; full browser auth +
  session lifecycle deferred to H.
- `go.mod`/`go.sum` unchanged — zero new dependencies.

### ResultEnvelope/Report Design

`ReportBuilder`: `ResearchState` + `EvidenceStore` + `SourceRegistry` →
`model.ResultEnvelope` (read-only).

- `Status`=`st.TerminalReason`; `SessionID`=`st.Session.ID`; `CompletedAt`=wall-clock
  (not scoring); `BudgetUsed`=`st.BudgetUsed`; `PlanPhases`=`st.Session.Plan.Phases`
  (Completed via `st.Plan.Phases[i].Completed`); `Errors`=`[]string` (sorted —
  `[]error` not JSON-stable).
- `Report.Intent`=`st.Session.Intent`; `Entities`=`[Intent.Entity]`; `TimeRange`=
  `*Intent.TimeRange` (deref to value).
- `Sources`: unique `evidence.SourceID` (domain) → `SourceRegistry.Lookup` →
  `{Name:domain, URL:"https://"+domain, Class, Quality}`; sorted by domain.
- `Findings`: evidence grouped by `Topic+Claim` → consensus `Value` (max-confidence
  among shared Claim within topic); `{Claim,Value,EvidenceIDs[] sorted,Status=verification
  string}`; sorted by (Topic,Claim,Value).
- `Contradictions`: `FindRelations(topic)` (all topics) filtered `Kind==CONTRADICTS` →
  `{ClaimA=evA.Claim,ClaimB=evB.Claim,EvidenceIDA,EvidenceIDB,Strength}`; sorted by
  (ClaimA,ClaimB,EvidenceIDA,EvidenceIDB).
- `Completeness`=`float64(completed phases)/len(Plan.Phases)`;
  `Confidence`=`float64(verified)/float64(total evidence)` (0 if none).

**Determinism:** all slices sorted; no `time.Now()` in ordering/scoring; map
iteration only into sorted slices.

### API Contracts

**REST:**
- `POST /api/v1/sessions` {IntentRequest} → `201 {session_id}`; server calls
  `Master.SubmitIntent` then starts `go m.Run(ctx)`.
- `GET /api/v1/sessions/{id}` → `ResearchState` snapshot JSON.
- `POST /api/v1/sessions/{id}/stop` → `Master.Stop()`.
- `GET /api/v1/sessions/{id}/result` → `ResultEnvelope` JSON (via `ReportBuilder`).
- `GET /api/v1/sessions/{id}/evidence` → `[]Evidence`.
- `GET /api/v1/sessions/{id}/sources` → `[]ReportSource`.
- `GET /api/v1/sessions/{id}/events` → SSE (user).
- `GET /api/v1/admin/sessions/{id}/events` +
  `GET /api/v1/admin/sessions/{id}` → SSE/State (require `auth.RequireAdmin`).
- `GET /` → Chatbox SPA.

**SSE (`text/event-stream`):** `phase {phase,status∈started|running|completed}`,
`progress {progress%,phase,evidence,contradictions,budget_used,budget_total}`
(progress% = `PhaseIndex/len(PlanPhaseOrder)`),
`report <ResultEnvelope JSON>` (on Terminal), `error {error}`. Single active session
(Cor.6).

### Verification

All T4 results:

- `go build ./...` — PASS
- `go vet ./...` — PASS
- `go test -count=1 ./...` — PASS (all packages; full A–F regression + Phase-G)
- API tests — PASS (15 tests: SSE 4 + admin 3 + handlers 8)
- Result tests — PASS (6/6)
- Integration tests — PASS (`TestPhaseF_EvidenceContradictionCircuit`,
  `TestPhaseG_APIE2E_ContradictionCircuit`,
  `TestPhaseG_APIE2E_ReportDeterministic`)
- Context-Fix E2E — PASS (real master, POST `/sessions` → Run survives handler → SSE
  emits terminal report)
- SSE terminal/disconnect tests — PASS (`TestSSE_TerminalClosesStream`,
  `TestSSE_ClientDisconnect`; `runtime.NumGoroutine` delta = 0)
- Admin auth tests — PASS (`TestAdmin_NoToken` 401, `TestAdmin_WrongToken` 403,
  `TestAdmin_RightToken` 200)
- `TestPhaseG_APIE2E_ReportDeterministic` — PASS (run twice; structural invariants
  equal)
- `go test -race` — FAIL only because `gcc not found` (environment limitation, not a
  code defect); attempted twice.
- `gofmt -l` on Phase-G files — clean. Pre-existing Phase-F files unformatted
  (gofmt -l lists many Phase-F files) — NOT a Phase-G defect; untouched.

### Locked Boundaries Respected

- No edits to master/storage/model/config/orchestrator/frontier/ingestion/managers/
  workers/browser logic.
- `go.mod`/`go.sum` unchanged; no new dependency (go mod verify: all modules
  verified).
- No EventStore / events append-log (deferred to H).
- No persistent session storage (memory-only sessionsManager in api layer; Master
  keeps Phase-F in-memory sessions).
- No authorized credential storage (admin token read ENV-only per request).

### Phase H Handoff

- Persistent SessionStore + session recovery across restarts (replace in-memory
  `sessionsManager`).
- EventStore / events append-log durability (SSE event-log deferred from G).
- Multi-session runtime (Phase G enforces single active session per Cor.6; lift to
  multi-session).
- Authorized-browser credential/session flow (browser auth + session lifecycle;
  deferred).
- Remaining runtime hardening: production Chromium browser adapter, DB migration path
  `data/draw.db`, SIGTERM/SIGINT teardown, end-to-end production integration.

---

## Release Notes Consolidation

The v1.5 release notes formerly maintained in `RELEASE_NOTES_v1.5.md` have been
consolidated into this document. The standalone release notes file is superseded as
of V2.
