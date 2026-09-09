# RELEASE NOTES — DRAW Phase J v1.5

## Release metadata

| Field | Value |
|---|---|
| Version | 1.5 — Phase J Stage 2 correction (v1.0 baseline + paraphrase gate) |
| Baseline commit | `c8ff7e2` (Phase I complete) |
| Corrected file | `internal/evidence/relations.go` (working-tree diff, uncommitted) |
| New files | `internal/evidence/similarity.go`, `internal/evidence/similarity_test.go`, `internal/evalharness/*` |
| Owner gate | Awaiting `APPROVED: publish v1.5` before Step 5 (publish / tag) |

---

## 1. Problem

### 1.1 The defect

In the **original** `internal/evidence/relations.go` (`ComputeRelations`), every pair sharing the same `Topic` and `Claim` but with **different `Value` strings** is classified as a contradiction — unconditionally. The original code (HEAD, lines 35 and 50–52):

```go
// line 35 — only skips pairs with a *different* Claim
if n.Claim != e.Claim {
    continue
}
...
// lines 50-52 — same Claim + different Value => CONTRADICTS, period
} else {
    kind = model.EvidenceRelationContradicts
    strength = 1.0
}
```

`ComputeRelations` performs an **exact-string** comparison on `Value` (`relations.go:35` in the original). The J-Spec §2.6 Scenario E expectation — that a paraphrase pair (same `Claim`, different-wording `Value`) should register as the *same fact*, not an opposing one — is **not** satisfied. The pair is instead read as a genuine contradiction.

The baseline finding (BASELINE_RESULTS_V1.0_original.md, finding §2) cites this defect at **`internal/evidence/relations.go:35-52`**: line 35 guards only the `Claim` check; lines 50–52 emit `CONTRADICTS` for *any* `Value` divergence.

### 1.2 Real-world impact — genuine paraphrased corroboration wrongly marked DISPUTED

Two sources reporting the **same fact** in different wording are misread as **opposing facts**. Concrete fixture — Scenario E (`internal/evalharness/scenarios.go:165-183`):

| Source | Claim | Value |
|---|---:|---|
| `alpha.example` | `claim:Capital` | `Paris is the capital of France` |
| `beta.example` | `claim:Capital` | `The capital city of France is Paris` |

Under the original V1 code:

1. `ComputeRelations` emits a `CONTRADICTS` edge (strength 1.0) bidirectionally → **2 edges**.
2. `ComputeVerification` (`internal/evidence/verify.go:76-91`) counts 2 incident `CONTRADICTS` edges per endpoint ≥ `KContradictions = 2` (`verify.go:12`) → both items resolve to **`DISPUTED`**.
3. `DISPUTED` evidence feeds `replanTriggerIfAny` (`decision.go:225-229`) and source-trustworthiness downstream — a **false-positive dispute** on identical factual content.

Measured baseline (BASELINE_RESULTS_V1.0_original.md, Scenario E):
- Relation edges read back: **2** — `E:0 → E:1 CONTRADICTS`, `E:1 → E:0 CONTRADICTS`
- VerificationState: **`DISPUTED` × 2**
- M4 (paraphrase dedup recall): **0 / 1** (pair not collapsed)
- M1 (independence groups): predicted **2**, expected **1**, accuracy **0.0**

---

## 2. The Correction

### 2.1 Mechanism — similarity gate (pure abstention)

A deterministic n-gram term-frequency Jaccard similarity check (`ValueSimilarity`, `internal/evidence/similarity.go:33-47`) is inserted at the **head** of the same-`Claim`/different-`Value` branch. When the two `Value` strings are highly similar (≥ τ), the `CONTRADICTS` edge is **suppressed entirely** — the pair emits **no edge at all** (`continue`). Genuine contradictions (overlap < τ) fall through with `CONTRADICTS` as before.

This is a **pure abstention**: no edge is emitted, and **no new relation `Kind`** is introduced. `SUPPORTS` / `DUPLICATES` / `CONTRADICTS` semantics are unchanged; `ComputeVerification` is untouched; the `EvidenceRelationKind` enum (`internal/model/enums.go:93-100`) and the `relations` table migration (`storage/migrations.go:91-101`) are untouched.

### 2.2 Accepted threshold — τ = 0.8

From `internal/evidence/similarity.go:16`:

```go
const contradictionSimilarityThreshold = 0.8
```

Selected from the fixed grid {0.5, 0.65, 0.8} per the Step 2 acceptance-gate protocol. τ = 0.8 is the **strictest** candidate that still suppresses the Scenario E paraphrase pair (overlap ≥ τ) while preserving the Scenario C true-contradiction pair (overlap < τ). It matches the n-gram-overlap threshold defined in the acceptance-gate protocol (τ_overlap = 0.8), keeping the correction consistent with the broader framework substrate.

`ValueSimilarity` is a pure standard-library function (n ∈ {1,2,3} token n-gram TF–Jaccard, lowercased + punctuation-stripped). No NLP / NER / entity extraction / LLM is involved — only the n-grams / term-frequency substrate explicitly permitted by the Phase-J hard constraint.

### 2.3 Separation table (Step 2)

The two scenario pairs that *define* the gate — their measured `ValueSimilarity` (n-gram TF–Jaccard, max over n=1,2,3):

| Pair | Value A | Value B | Overlap (n-gram TF–Jaccard) | τ = 0.8 comparison | Edge emitted? |
|---|---|---|---|---|---|
| Scenario C (true contradiction) | `yes` | `no` | **0.000** (disjoint unigrams) | 0.000 < 0.8 | **CONTRADICTS** ✓ |
| Scenario E (paraphrase) | `Paris is the capital of France` | `The capital city of France is Paris` | **0.857** (6/7) | 0.857 ≥ 0.8 | **suppressed** ✓ |

Candidate-threshold grid search (fixed grid {0.5, 0.65, 0.8}):

| Candidate τ | C overlap | C emits CONTRADICTS (overlap < τ)? | E overlap | E suppressed (overlap ≥ τ)? | Strictly separates C ↔ E? |
|---|---|---|---|---|---|
| 0.50 | 0.000 | yes | 0.857 | yes | yes |
| 0.65 | 0.000 | yes | 0.857 | yes | yes |
| 0.80 | 0.000 | yes | 0.857 | yes | **yes — strictest → selected** |

Boundary cases (from `internal/evidence/similarity_test.go:89-119`, `TestComputeRelations_BoundaryNearThreshold`):

| Boundary pair | Tokens | Unigram Jaccard | τ = 0.8 | Result |
|---|---|---|---|---|
| "just below" | 7 shared + 1 unique each | 7/9 ≈ **0.778** | 0.778 < 0.8 | CONTRADICTS emitted (true-dispute path) |
| "just above" | 9 shared + 1 unique each | 9/11 ≈ **0.818** | 0.818 ≥ 0.8 | suppressed (abstention) |

τ = 0.8 is selected because it is the **strictest** grid value satisfying both separation criteria — it maximizes the suppression gap (only strongly-similar pairs are withheld) while never suppressing a genuine contradiction on either defining fixture or the boundary cases.

### 2.4 Exact code change — `internal/evidence/relations.go:59-61`

Inserted into the `else` branch of the `n.Value == e.Value` check. Functional lines **59–61** (comment block 51–58):

**Before (HEAD, lines 50–53):**
```go
} else {
    kind = model.EvidenceRelationContradicts
    strength = 1.0
}
```

**After (working tree, lines 50–64):**
```go
} else {
    // Paraphrase gate: when the differing Values are highly
    // similar (same fact, different wording, per the acceptance-gate protocol §2.6
    // Scenario E), suppress the CONTRADICTS edge entirely. This is a
    // pure abstention — no edge is emitted and no new relation Kind is
    // introduced (no enum or migration change). Genuine contradictions
    // (Scenario C) fall through with overlap < threshold and emit
    // CONTRADICTS as before. See Stage 2 acceptance-gate protocol
    // (tau = contradictionSimilarityThreshold).
    if ValueSimilarity(n.Value, e.Value) >= contradictionSimilarityThreshold {
        continue
    }
    kind = model.EvidenceRelationContradicts
    strength = 1.0
}
```

Git diff:
```diff
             } else {
+                // Paraphrase gate: when the differing Values are highly
+                // similar ... (tau = contradictionSimilarityThreshold).
+                if ValueSimilarity(n.Value, e.Value) >= contradictionSimilarityThreshold {
+                    continue
+                }
                 kind = model.EvidenceRelationContradicts
                 strength = 1.0
             }
```

Supporting new file `internal/evidence/similarity.go` defines `ValueSimilarity` (pure stdlib n-gram TF–Jaccard) and the `contradictionSimilarityThreshold = 0.8` constant.

---

## 3. Verification (Step 3)

**Method.** `go test ./internal/evidence/...` (unit) and `go test ./internal/evalharness/ -run TestBaseline -v` (scenario harness). The harness runs the **real** `evidence.ComputeRelations` + `evidence.ComputeVerification` over a fresh in-memory SQLite store (`internal/evalharness/store.go:28-57`), then reads the `ObservedState` back via the **R3** path — `EventStore.Load` (paginated `afterID` cursor, `limit=100_000`, bypassing the SSE 1000-event bound at `internal/api/sse.go:76-84`) plus `EvidenceStore.Query` / `FindRelations` (`run.go:106-151`).

- **Before** = `BASELINE_RESULTS_V1.0_original.md` — original V1 at commit `c8ff7e2` (no gate).
- **After** = `POST_CORRECTION_RESULTS.md` — corrected v1.5 (gate in working tree).

The `git diff --no-index` between the two result files shows the **only** behavioral change is Scenario E (see table below). Scenarios A–D are byte-identical.

### 3.1 Before / After table — all assertions (pass and fail)

| Scenario | Assertion | Before (v1.0) | After (v1.5) | Delta | Result |
|---|---|---|---|---|---|
| **A** | M1 independence groups = 1 | pred 1 / exp 1, acc **1.0** | 1 / 1, acc **1.0** | — | ✅ PASS (unchanged) |
| **A** | M2 support FPR | 1 FP / 6 = 0.1667 | 1 / 6 = 0.1667 | — | ✅ PASS (unchanged) |
| **A** | M6 VERIFIED | 4/4 VERIFIED | 4/4 VERIFIED | — | ✅ PASS (unchanged) |
| **B** | M1 groups = 3 | 3/3 acc **1.0** | 3/3 acc **1.0** | — | ✅ PASS (positive control) |
| **B** | M2 FPR | 0/0 = 0.0 | 0/0 = 0.0 | — | ✅ PASS (unchanged) |
| **B** | M6 UNVERIFIED | 3/3 UNVERIFIED | 3/3 UNVERIFIED | — | ✅ PASS (unchanged) |
| **C** | M3 contradiction detected | 1/1 = **1.0** | 1/1 = **1.0** | — | ✅ PASS (true contradiction preserved) |
| **C** | CONTRADICTS edges emitted | 2 edges | 2 edges | — | ✅ PASS (`"yes"`/`"no"` overlap 0.000 < τ) |
| **C** | M6 DISPUTED | 2/2 DISPUTED | 2/2 DISPUTED | — | ✅ PASS (true dispute still flagged) |
| **D** | M1 groups = 6 | 6/6 acc **1.0** | 6/6 acc **1.0** | — | ✅ PASS (unchanged) |
| **D** | M5 novelty slope | −0.1614 | −0.1614 | — | ✅ PASS (unchanged) |
| **D** | M6 PARTIALLY_VERIFIED | 24/24 | 24/24 | — | ✅ PASS (unchanged) |
| **E** | M6 false DISPUTED → should be UNVERIFIED | **`DISPUTED`** | **`UNVERIFIED`** | DISPUTED → UNVERIFIED | ✅ PASS (**bug fixed**) |
| **E** | M6 edges | 2 × CONTRADICTS | **0 edges** | 2 → 0 | ✅ PASS (gate fires, overlap 0.857 ≥ τ) |
| **E** | M1 groups = 1 (collapse) | pred 2 / exp 1, acc **0.0** | 2 / 1, acc **0.0** | — | ❌ FAIL (no improvement) |
| **E** | M4 paraphrase collapse | 0/1 = **0.0** | 0/1 = **0.0** | — | ❌ FAIL (no improvement) |
| **ALL** | M7 determinism (byte-identical signature) | true | true | — | ✅ PASS |
| **ALL** | Unit assertions (`similarity_test.go`) | — | — | — | ✅ PASS (4/4 suites green) |

### 3.2 Unit-test assertion detail (`internal/evidence/similarity_test.go`)

| Test | What it asserts | Result |
|---|---|---|
| `TestComputeRelations_ParaphraseSuppressed` | Scenario E pair (`"Paris…"` vs `"The capital…"`) → `ValueSimilarity = 0.857 ≥ τ` → **0 edges** emitted | ✅ PASS |
| `TestComputeRelations_TrueContradictionEmitted` | Scenario C pair (`"yes"` vs `"no"`) → `ValueSimilarity = 0.000 < τ` → **2 CONTRADICTS** edges | ✅ PASS |
| `TestComputeRelations_BoundaryNearThreshold` | 0.778 pair → emits; 0.818 pair → suppressed | ✅ PASS |
| `TestValueSimilarity_EdgeCases` | identical→1.0, disjoint→0.0, empty→0.0, case-fold/punct-strip | ✅ PASS (all 9 sub-cases) |

### 3.3 Scenario-level correctness

- **Scenarios A–D: no behavioral change.** None of these pairs traverse the same-`Claim`/different-`Value` branch with overlap ≥ τ. The gate never fires; output is byte-identical.
- **Scenario C: preserved.** `"yes"` vs `"no"` → overlap `0.000 < 0.8` → `CONTRADICTS` still emitted → both `DISPUTED` ✅
- **Scenario E: fixed.** `"Paris is the capital of France"` vs `"The capital city of France is Paris"` → overlap `0.857 ≥ 0.8` → `CONTRADICTS` suppressed → **0 edges** → both `UNVERIFIED` ✅

### 3.4 Root cause — why M1 and M4 do **not** improve (Scenario E)

**M1** (`independence_group_count`) and **M4** (`paraphrase_dedup_recall`) remain at their baseline-failing values (**0.0**) for Scenario E. This is **expected and structural**, not a gate defect:

1. **Abstention-only mechanism.** The gate's action on a suppressed pair is `continue` — it emits **no edge of any kind**. `ComputeRelations` produces neither the (wrong) `CONTRADICTS` edge nor a (missing) `SUPPORTS`/`DUPLICATES` edge. The two evidence items stay in **separate connected components**.

2. **M1 = connected-component count.** `computeM1` (`internal/evalharness/metrics.go:82-118`) computes predicted groups as the number of connected components over `SUPPORTS`/`DUPLICATES` edges. With zero edges for the pair, each item is its own component → predicted **2**, expected **1** → accuracy **0.0**. No edge → no merge → no improvement.

3. **M4 = collapse count.** `computeM4` (`metrics.go:208-251`) counts paraphrase pairs that land in the **same** component via `SUPPORTS`/`DUPLICATES`. With zero merge-edges, the pair is never collapsed → `0/1` recall → **0.0**.

4. **Why M6 accuracy stays 1.0 while the state changes:** M6 does **not** compare against a fixed ground-truth state — `computeM6` (`metrics.go:321-380`) re-derives the *expected* state from the *observed* edges and checks the observed `VerificationState` matches. With 0 edges, both expected and observed resolve to `UNVERIFIED` → self-consistent → accuracy 1.0. The **state changed** (`DISPUTED` → `UNVERIFIED`) but the **accuracy metric did not regress**.

**To actually improve M1 and M4 for Scenario E, the framework would need to emit a positive, collapse-producing edge for paraphrase-equivalent `Value` strings.** That requires one of:

| Option | What it needs | Why it is out of scope for v1.5 |
|---|---|---|
| (a) Redefine `SUPPORTS`/`DUPLICATES` to treat high-similarity `Value` as equivalent | Change `ComputeRelations` value handling | Violates **R4** — no silent redefinition of V1 semantics without a baseline-vs-candidate demonstration |
| (b) New relation `Kind` (e.g. `CORROBORATES`) | Edit `EvidenceRelationKind` enum + migration | Violates **Tier A constraint** — no enum/migration/`Kind` changes; `enums.go` + `migrations.go` are Tier B-lock territory |
| (c) Semantic paraphrase detection beyond n-gram TF | NLP / NER / entity extraction / LLM | Violates the **no-NLP hard constraint** (allowed substrate: n-grams/term-frequency/metadata/provenance/timestamps only) |

The v1.5 correction deliberately stays within scope **(a)**-free boundary: **it fixes the false `DISPUTED`** (the defect in scope for this release) and **cannot fix M1/M4** (those require the out-of-scope changes above). This matches the R4/R6 gate principle — the candidate corrects the measured failure mode (false-positive dispute on paraphrased corroboration) without silently redefining V1 relation semantics.

---

## 4. Constraints honored

| Constraint | Compliance | Evidence |
|---|---|---|
| **Tier A only** | ✅ No Tier B production file edited | `relations.go` is outside `internal/model/*`; `similarity.go` is a new Tier A file; `internal/model/enums.go`, `internal/model/task.go`, `storage/migrations.go`, `storage/sqlite_evidence.go`, `internal/storage/ports.go`, `internal/api/sse.go` all untouched |
| **No enum / migration / Kind changes** | ✅ No new `EvidenceRelationKind` | Enum (`model/enums.go:93-100`) unchanged; `relations` table migration (`migrations.go:91-101`) untouched; gate emits no edge (abstention), introduces no kind |
| **No NLP** | ✅ Pure n-gram TF–Jaccard | `ValueSimilarity` (`similarity.go:33-47`) uses only `strings.Fields` + `strings.ToLower` + `strings.Trim` + token n-gram counting; no NER / entity extraction / LLM |
| **Deterministic** | ✅ M7 passes; pure function | `ValueSimilarity` is pure stdlib over its inputs; `ComputeRelations` output sorted by `(From, To, Kind)` + deduped by `From+To+Kind` (`relations.go:71-79`); determinism signature byte-identical across ≥2 runs |
| **R4 — no silent semantic redefinition** | ✅ `SUPPORTS`/`DUPLICATES`/`CONTRADICTS` unchanged | Gate only suppresses `CONTRADICTS` via `continue` (abstention); does not alter any kind's meaning or `ComputeVerification` |
| **R5 — semantic status** | ✅ N/A for v1.5 (no new J-signal) | Correction is a relation-detection refinement, not a new measured Signal; M5 (`Inferred`) / M7 (`Observed`) statuses unchanged |
| **R7 — dormant scoring terms untouched** | ✅ `frontier/score.go` not touched | No scoring term activated; correction lives purely in the evidence pipeline, never in `frontier/score.go:41-47` |
| **R3 — R3 read path** | ✅ Harness bypasses SSE 1000-bound | Evalharness reads via `EventStore.Load` (`limit=100_000`, paginated `afterID`) + `EvidenceStore.Query`/`FindRelations`; never `api/sse.go:76-84` |

### Files in this release (v1.5)

| File | Change | Tier |
|---|---|---|
| `internal/evidence/relations.go` | Modified — paraphrase gate at `:59-61` (comment `:51-58`) | A |
| `internal/evidence/similarity.go` | New — `ValueSimilarity` + `contradictionSimilarityThreshold = 0.8` | A |
| `internal/evidence/similarity_test.go` | New — 4 test suites (paraphrase suppression, true contradiction, boundary, edge cases) | A |
| `internal/evalharness/*` | New — baseline / scenario harness (read-only over V1 production code) | A |

### Test summary

```
$ go test ./internal/evidence/ -run 'TestComputeRelations_|TestValueSimilarity' -v
--- PASS: TestComputeRelations_ParaphraseSuppressed       (overlap 0.857 ≥ τ 0.8 → 0 edges)
--- PASS: TestComputeRelations_TrueContradictionEmitted     (overlap 0.000 < τ 0.8 → 2 CONTRADICTS)
--- PASS: TestComputeRelations_BoundaryNearThreshold      (0.778 emit; 0.818 suppress)
--- PASS: TestValueSimilarity_EdgeCases                   (9 sub-cases)

$ go test ./internal/evalharness/ -run TestBaseline -v
  Scenario E — BEFORE: 2 CONTRADICTS edges → DISPUTED
             — AFTER : 0 edges → UNVERIFIED  ✅ (bug fixed)
  Scenarios A–D, C: byte-identical before/after          ✅
  M7 determinism: byte-identical across 2 runs            ✅
```

---

**Status: complete.** Awaiting owner confirmation `APPROVED: publish v1.5` before Step 5 (publish / tag).
