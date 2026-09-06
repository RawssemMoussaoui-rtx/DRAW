# EXPERIMENT_0_PREPUBLICATION_VERIFICATION.md

## 1. Overall Gate

**PASS**

All ten verification checks (V1–V10) are VERIFIED against independent repo evidence. Build, vet, and tests pass. No prohibited changes found. No contradictions between the release notes and the actual code/result files.

---

## 2. Verification Matrix

| Check | Status | Evidence |
|---|---|---|
| V1 Release Notes | VERIFIED | git diff HEAD confirms original relations.go has no similarity gate (else branch at HEAD lines 50–52 emits CONTRADICTS unconditionally); working-tree adds gate at lines 59–61; similarity.go:16 defines `contradictionSimilarityThreshold = 0.8`; before/after result files + test run match; changed-file list matches git state |
| V2 Fixtures | VERIFIED | scenarios.go:81-99 (Scenario C: claim:Q, "yes"/"no", eta/theta, expected CONTRADICTS, overlap 0.000); scenarios.go:165-183 (Scenario E: claim:Capital, "Paris is the capital of France"/"The capital city of France is Paris", alpha/beta, expected DUPLICATE, overlap 0.857) |
| V3 Threshold Separation | VERIFIED | τ ∈ {0.50, 0.65, 0.80} independently confirmed; C overlap 0.000 < all τ → emits; E overlap 0.857 ≥ all τ → suppressed; acceptance condition (E suppressed AND C not suppressed) met for all; τ=0.80 is strictest → selected; no contradiction with V1 table |
| V4 Relation Logic | VERIFIED | Gate inserted at relations.go:59 inside the same-Claim/different-Value else branch only; uses `continue` (no edge emitted); no new Kind (enums.go unchanged); pre-existing relations_test.go tests all pass unchanged |
| V5 Similarity Utility | VERIFIED | similarity.go:33-47 `ValueSimilarity` is pure stdlib (`strings` only); deterministic; n ∈ {1,2,3} via loop at line 40; multiset TF–Jaccard via `ngramTFJaccard` (min for intersection, sum−inter for union); threshold defined once at similarity.go:16; no NLP/NER; reusable by future J1 |
| V6 Unit Tests | VERIFIED | 4 new suites in similarity_test.go: `TestComputeRelations_ParaphraseSuppressed` (reproduces Scenario E bug → 0 edges), `TestComputeRelations_TrueContradictionEmitted` (Scenario C → 2 CONTRADICTS), `TestComputeRelations_BoundaryNearThreshold` (0.778 emits, 0.818 suppresses), `TestValueSimilarity_EdgeCases` (9 sub-tests). All pre-existing relations_test.go tests still pass. All 16 functions + 9 sub-tests PASS |
| V7 Before/After | VERIFIED | git diff --no-index BASELINE_RESULTS_V1.0_original.md POST_CORRECTION_RESULTS.md shows only Scenario E behavioral change (M6 DISPUTED→UNVERIFIED, edges 2→0); Scenarios A–D byte-identical; M1/M4 no improvement for E (consistent with abstention-only — no merge edge emitted) |
| V8 Build/Test | VERIFIED | `go build ./...` PASS; `go vet ./internal/evidence/... ./internal/evalharness/...` PASS; `go test ./internal/evidence/...` PASS; `go test ./internal/evalharness/... -run TestBaseline` PASS |
| V9 Diff Purity | VERIFIED | git diff HEAD shows only EXPERIMENT_PROTOCOL.md (staged, unrelated) + internal/evidence/relations.go (modified). New untracked Experiment 0 files: similarity.go, similarity_test.go, internal/evalharness/* (6 files), RELEASE_NOTES_v1.5.md. All other untracked files are pre-existing Phase-J artifacts |
| V10 Governance | VERIFIED | git diff HEAD confirms no Tier B file modified (enums.go, task.go, migrations.go, ports.go, sse.go, sqlite_evidence.go, score.go, master.go all show zero diff); no new EvidenceRelationKind; no migration change; no NLP/NER; J1 not implemented; evalharness is new read-only package calling real V1 code; BASELINE_RESULTS_V1.0_original.md preserved |

---

## 3. Scenario C Evidence

**Source:** `internal/evalharness/scenarios.go:81-99`

| Field | Value |
|---|---|
| Scenario ID | C |
| Name | Known contradiction pair |
| Topic | research |
| Claim | `claim:Q` |
| Source 1 | Domain: `eta.example`, Value: `yes`, Quality: 1.0 |
| Source 2 | Domain: `theta.example`, Value: `no`, Quality: 1.0 |
| Expected relation | CONTRADICTS (ground truth: `CONTRADICT`, Independent=true) |
| Expected groups | 2 |

**Similarity (verified independently by hand + by test):**

- `tokenize("yes") = ["yes"]`, `tokenize("no") = ["no"]`
- n=1 Jaccard: intersection=0, union=2, Jaccard=0.0
- n=2: `len(tokens) < n` → nil → Jaccard=0.0
- n=3: `len(tokens) < n` → nil → Jaccard=0.0
- **ValueSimilarity("yes","no") = max(0.0, 0.0, 0.0) = 0.000**

This is confirmed by `TestValueSimilarity_EdgeCases` sub-test `completely different single` (asserts `ValueSimilarity("yes","no") == 0.0`) and `TestComputeRelations_TrueContradictionEmitted` (asserts overlap < τ, expects 2 CONTRADICTS edges).

Because 0.000 < τ (0.8), the paraphrase gate does NOT fire for Scenario C. The `CONTRADICTS` edge is still emitted (bidirectional → 2 edges). Both items see ≥ KContradictions=2 → `DISPUTED` (via verify.go:76-91, KContradictions=2 at verify.go:12). This matches `BASELINE_RESULTS_V1.0_original.md` Scenario C (2 CONTRADICTS edges, DISPUTED×2, M3 detected=1/1 rate=1.0) and `POST_CORRECTION_RESULTS.md` Scenario C (identical).

---

## 4. Scenario E Evidence

**Source:** `internal/evalharness/scenarios.go:165-183`

| Field | Value |
|---|---|
| Scenario ID | E |
| Name | Paraphrase dedup bottleneck |
| Topic | research |
| Claim | `claim:Capital` |
| Source 1 | Domain: `alpha.example`, Value: `Paris is the capital of France`, Quality: 0.3 |
| Source 2 | Domain: `beta.example`, Value: `The capital city of France is Paris`, Quality: 0.3 |
| Expected relation | DUPLICATE (ground truth: `DUPLICATE`, Independent=false) |
| Expected groups | 1 (ground truth: same fact → 1 group) |
| ParaphrasePairs (Expect) | 1 |

**Similarity (verified independently by hand + by test):**

Tokenization:
- A = `"Paris is the capital of France"` → lowercase → `["paris","is","the","capital","of","france"]` (6 tokens)
- B = `"The capital city of France is Paris"` → lowercase → `["the","capital","city","of","france","is","paris"]` (7 tokens)

n=1 (unigram TF–Jaccard):
- Shared tokens (min count per shared): paris(1), is(1), the(1), capital(1), of(1), france(1) → intersection = 6
- Union = 6 + 7 − 6 = 7
- Jaccard = 6/7 ≈ **0.857143**

n=2 (bigram TF–Jaccard):
- A bigrams (5): "paris is", "is the", "the capital", "capital of", "of france"
- B bigrams (6): "the capital", "capital city", "city of", "of france", "france is", "is paris"
- Shared bigrams: "the capital", "of france" → intersection = 2
- Union = 5 + 6 − 2 = 9
- Jaccard = 2/9 ≈ 0.222

n=3 (trigram TF–Jaccard):
- A trigrams (4), B trigrams (5); shared = 0
- Jaccard = 0/9 = 0.0

**ValueSimilarity(A, B) = max(0.857, 0.222, 0.0) = 0.857**

This is ≥ τ (0.8), so the paraphrase gate fires: `continue` — no edge is emitted at all.

This is confirmed by:
- `TestComputeRelations_ParaphraseSuppressed` (asserts overlap ≥ τ and `len(rels) == 0`)
- `TestValueSimilarity_EdgeCases` (asserts `ValueSimilarity("Paris is the capital of France", "The capital city of France is Paris") >= 0.8`)
- `POST_CORRECTION_RESULTS.md` Scenario E: 0 relation edges read back, both items `UNVERIFIED`

**Before (original V1)**: 2 CONTRADICTS edges emitted → both `DISPUTED` (BASELINE_RESULTS_V1.0_original.md Scenario E: 2 edges, DISPUTED×2, M6 Expected=DISPUTED:2 Observed=DISPUTED:2).

**After (corrected v1.5)**: 0 edges emitted → both `UNVERIFIED` (POST_CORRECTION_RESULTS.md Scenario E: 0 edges, UNVERIFIED×2, M6 Expected=UNVERIFIED:2 Observed=UNVERIFIED:2).

---

## 5. Threshold Separation Table

Threshold constant: `contradictionSimilarityThreshold = 0.8` (similarity.go:16).

| Candidate τ | C overlap (0.000) | C emits CONTRADICTS? (overlap < τ) | E overlap (0.857) | E suppressed? (overlap ≥ τ) | Acceptance (E suppressed AND C not) | Strictest? |
|---|---|---|---|---|---|---|
| 0.50 | 0.000 | yes | 0.857 | yes | yes | no |
| 0.65 | 0.000 | yes | 0.857 | yes | yes | no |
| 0.80 | 0.000 | yes | 0.857 | yes | yes | **yes — selected** |

All three grid values strictly separate C ↔ E. τ=0.80 is the strictest (maximizes the suppression gap). Selected. No contradiction with the release notes' V1 table.

**Boundary verification (from similarity_test.go:89-119, TestComputeRelations_BoundaryNearThreshold):**

| Boundary pair | Tokens | Computed overlap | τ=0.8 | Result |
|---|---|---|---|---|
| "just below" (7 shared + 1 unique each) | "w1 w2 w3 w4 w5 w6 w7 uniqA" vs "...uniqB" | 7/9 ≈ 0.7778 (n=1 max) | 0.7778 < 0.8 | CONTRADICTS emitted (2 edges) ✓ |
| "just above" (9 shared + 1 unique each) | "w1 w2 w3 w4 w5 w6 w7 w8 w9 uniqA" vs "...uniqB" | 9/11 ≈ 0.8182 (n=1 max) | 0.8182 ≥ 0.8 | suppressed (0 edges) ✓ |

Both boundary cases confirmed by test results.

---

## 6. Before/After Results

Comparison of `BASELINE_RESULTS_V1.0_original.md` (BEFORE) vs `POST_CORRECTION_RESULTS.md` (AFTER), verified via `git diff --no-index` (27 insertions, 6 deletions — all confined to Scenario E + Run metadata format).

| Scenario | Metric | Before (v1.0) | After (v1.5) | Delta | Status |
|---|---|---|---|---|---|
| A | M1 | 1/1 acc=1.0 | 1/1 acc=1.0 | — | identical ✓ |
| A | M2 | FP=1/6 FPR=0.1667 | 0.1667 | — | identical ✓ |
| A | M6 | 4/4 VERIFIED | 4/4 VERIFIED | — | identical ✓ |
| B | M1 | 3/3 acc=1.0 | 3/3 acc=1.0 | — | identical ✓ |
| B | M6 | 3/3 UNVERIFIED | 3/3 UNVERIFIED | — | identical ✓ |
| C | M3 | detected=1/1 rate=1.0 | detected=1/1 rate=1.0 | — | identical ✓ |
| C | edges | 2 × CONTRADICTS | 2 × CONTRADICTS | — | identical ✓ |
| C | M6 | 2/2 DISPUTED | 2/2 DISPUTED | — | identical ✓ |
| D | M1 | 6/6 acc=1.0 | 6/6 acc=1.0 | — | identical ✓ |
| D | M5 | slope=-0.161429 | -0.161429 | — | identical ✓ |
| D | M6 | 24/24 PARTIALLY_VERIFIED | 24/24 | — | identical ✓ |
| **E** | **M6** | **2/2 DISPUTED** | **2/2 UNVERIFIED** | **DISPUTED→UNVERIFIED** | **bug fixed ✓** |
| **E** | **edges** | **2 × CONTRADICTS** | **0 edges** | **2→0** | **gate fires ✓** |
| E | M1 | predicted=2/expected=1 acc=0.0 | same | — | no improvement (expected) |
| E | M4 | collapsed=0/1 recall=0.0 | same | — | no improvement (expected) |
| ALL | M7 | true | true | — | identical ✓ |

**Key confirmation:** Scenario E's M1 and M4 did NOT improve (both remain 0.0). This is **consistent** with the abstention-only correction: the gate's action is `continue` — it emits *no edge of any kind*. M1 (connected components over SUPPORTS/DUPLICATES edges) and M4 (paraphrase pairs collapsed via SUPPORTS/DUPLICATES) both depend on a *positive* merge edge. With zero edges emitted, the pair stays in separate components → predicted 2 groups, 0 collapsed. No merge edge → no improvement. The release notes' §3.4 explanation is accurate.

**M6 nuance:** M6 accuracy stays 1.0 because `computeM6` (metrics.go:321-380) re-derives the *expected* state from *observed* edges. With 0 edges, both expected and observed resolve to `UNVERIFIED` → self-consistent → accuracy 1.0. The *state* changed (DISPUTED→UNVERIFIED) but the *accuracy metric* did not regress. This matches the release notes.

**M7 determinism:** All scenarios show `{"Equal":true}` — byte-identical structural signature across 2 runs (including Scenario E). Confirmed independently by test run.

---

## 7. Changed Files

### Experiment 0 files (vs HEAD `c8ff7e2`)

| File | Status | Change |
|---|---|---|
| `internal/evidence/relations.go` | Modified (unstaged) | Added 11 lines: paraphrase gate (`if ValueSimilarity(...) >= contradictionSimilarityThreshold { continue }`) + comment in the same-Claim/different-Value else branch |
| `internal/evidence/similarity.go` | New (untracked) | `ValueSimilarity` n-gram TF–Jaccard utility + `contradictionSimilarityThreshold = 0.8` constant |
| `internal/evidence/similarity_test.go` | New (untracked) | 4 test suites (paraphrase suppression, true contradiction, boundary, edge cases) |
| `internal/evalharness/*` | New (untracked) | 6 files: baseline_test.go, metrics.go, run.go, scenarios.go, store.go, types.go — read-only harness calling real V1 code |
| `RELEASE_NOTES_v1.5.md` | New (untracked) | Release notes document |

Confirmed via:
- `git diff HEAD --name-only` → only `EXPERIMENT_PROTOCOL.md` and `internal/evidence/relations.go` differ from HEAD
- `git ls-tree HEAD -- internal/evidence/ internal/evalharness/` → evalharness/ has zero tracked files at HEAD; similarity.go and similarity_test.go are absent from HEAD
- `git diff HEAD -- internal/evidence/relations.go` → shows exactly 11 insertions (the gate + comment)

### Pre-existing / unrelated Phase-J artifacts (not Experiment 0)

| File | Status | Classification |
|---|---|---|
| `EXPERIMENT_PROTOCOL.md` | Staged new (`A`) | Phase-J protocol for a **different** experiment (J1 Independence Graph, §"Experiment 1 Scope Proposal"); not listed in release notes' changed-file table; not part of the paraphrase-gate correction |
| `BASELINE_RESULTS.md` | Untracked (overwritten by test run) | Dynamically generated by `TestBaseline`; the `baseline_test.go` always writes to this path regardless of code state |
| `BASELINE_RESULTS_V1.0_original.md` | Untracked | Preserved "before" results snapshot (unchanged by Experiment 0) |
| `POST_CORRECTION_RESULTS.md` | Untracked | Preserved "after" results snapshot |
| `ALGORITHM_DESIGN.md` | Untracked | Phase-J design doc |
| `ARCHITECTURE_BASELINE_REPORT.md` | Untracked | Phase-J architecture doc |
| `EVALUATION_DESIGN.md` | Untracked | Phase-J evaluation doc |
| `INTEGRATION_DESIGN.md` | Untracked | Phase-J integration doc |
| `PHASE_J_RULINGS.md` | Untracked | Phase-J governance rulings |
| `PHASE_J_SPEC.md` | Untracked | Phase-J specification |
| `data/draw.db` | Untracked | Pre-existing SQLite database (163,840 bytes); development artifact, unrelated to Experiment 0 |

---

## 8. Findings

### Confirmed correct (no issues):

1. **Similarity values verified independently by hand-calculation and by passing tests:** Scenario C overlap = 0.000, Scenario E overlap = 0.857 (6/7 unigram Jaccard; max across n∈{1,2,3}). Boundary cases: 0.778 (below, emits) and 0.818 (above, suppresses). All four confirmed by test assertions.

2. **Original code confirmed:** `git diff HEAD -- internal/evidence/relations.go` shows the only change is the insertion of the paraphrase gate (11 lines) in the same-Claim/different-Value else branch. The original HEAD code (lines 50–52) unconditionally emits `CONTRADICTS` for any Value divergence.

3. **Before/after results confirmed:** `git diff --no-index BASELINE_RESULTS_V1.0_original.md POST_CORRECTION_RESULTS.md` shows 27 insertions and 6 deletions — all confined to Scenario E (M6 state DISPUTED→UNVERIFIED, edges 2→0) plus the "Run metadata" format section. Scenarios A–D are byte-identical. The release notes' claim that "the only behavioral change is Scenario E" is accurate.

4. **M1/M4 no-improvement explanation verified:** The gate uses `continue` (emits no edge). M1 = connected components over SUPPORTS/DUPLICATES edges; M4 = paraphrase pairs collapsed via SUPPORTS/DUPLICATES. With zero edges, the pair is never merged → predicted 2 groups, 0 collapsed. This is structurally consistent with an abstention-only correction, as the release notes explain in §3.4.

5. **No prohibited changes:** `git diff HEAD` confirms only `internal/evidence/relations.go` (modified) differs from HEAD among tracked files. All Tier B files (enums.go, task.go, migrations.go, ports.go, sse.go, sqlite_evidence.go, score.go, master.go) show zero diff. The EvidenceRelationKind enum (enums.go:93-100) retains exactly 4 values. The relations table migration (migrations.go:91-97) is unchanged. No NLP/NER (only `strings` package imported in similarity.go).

6. **Test coverage verified:** All 4 new test suites in similarity_test.go pass. All 12 pre-existing tests in relations_test.go still pass (including `TestComputeRelations_SameClaimDifferentValue` which uses "val1"/"val2" — disjoint tokens, overlap 0.0, CONTRADICTS still emitted). Tests assert specific outcomes (use `t.Fatalf` with explicit conditions), not just execution.

### Minor discrepancies / limitations (do not affect gate):

1. **`BASELINE_RESULTS_V1.0_original.md` lacks "Run metadata" section:** The current `baseline_test.go` always generates a "## Run metadata" section (line 143), but `BASELINE_RESULTS_V1.0_original.md` does not have one. This suggests the file was either generated by an older test version or manually constructed. The actual metric data (Scenario E: DISPUTED, 2 CONTRADICTS edges) is consistent with original V1 behavior, so this does not affect verification.

2. **`POST_CORRECTION_RESULTS.md` metadata references `BASELINE_RESULTS.md.bak`:** The "Run metadata" section in POST_CORRECTION_RESULTS.md lists `?? BASELINE_RESULTS.md.bak` in the working-tree state, but this file does not exist in the current working tree (not shown in `git status --porcelain`). The file was likely present at generation time but later removed. Does not affect result validity.

3. **`POST_CORRECTION_RESULTS.md` header text inconsistency:** The header still reads "Phase J — Stage 2. Baseline measurement of **unmodified V1**" (hardcoded in `baseline_test.go` line 134) even though it contains the corrected (v1.5) results. This is a cosmetic artifact of the test generator, not a data error.

4. **EXPERIMENT_PROTOCOL.md is staged but unrelated:** `EXPERIMENT_PROTOCOL.md` is staged (`A` in `git status`) but is not listed in the release notes' changed-file table. Its content describes a *different* Phase-J experiment (J1 Independence Graph, §"Experiment 1 Scope Proposal"), not the paraphrase-gate correction. It is correctly classified here as a pre-existing Phase-J artifact, not an Experiment 0 change.

5. **`ValueSimilarity` computes overlap over Value only, not Claim+Value:** PHASE_J_SPEC.md:308 defines `NGramOverlap` as "max n-gram TF Jaccard over (Claim,Value)". The Experiment 0 implementation computes overlap over Value only. This is correct for the gate's purpose because the gate is only reached when `n.Claim == e.Claim` (checked at relations.go:35), so Value-only overlap is equivalent to Claim+Value overlap for same-Claim pairs. However, it does not match the spec's literal definition of NGramOverlap. This is a minor semantic distinction, not a bug.

6. **`data/draw.db` is a pre-existing untracked binary:** A 163,840-byte SQLite database at `data/draw.db`. It is untracked and unrelated to Experiment 0. Its presence in the working tree is noted for completeness.

7. **Threshold constant `0.8` also appears in `internal/config/config_test.go`** as `CPUThreshold` and `RAMThreshold` values (lines 15–16), but these are unrelated configuration constants in a different package. No actual duplication of `contradictionSimilarityThreshold` exists — it is defined exactly once at `similarity.go:16`.

---

## 9. Publication Recommendation

**READY FOR OWNER APPROVAL**

All verification checks V1–V10 pass with no contradictions. The only behavioral change between baseline (v1.0) and correction (v1.5) is Scenario E: the false `DISPUTED` state is corrected to `UNVERIFIED` via suppression of 2 spurious `CONTRADICTS` edges. Scenarios A–D are byte-identical. M1/M4 for Scenario E remain at 0.0 — this is structurally expected for a pure-abstention gate and is correctly explained in the release notes. No Tier B files, enums, migrations, or NLP components were introduced or modified. Build, vet, and all tests pass.

The owner must still issue the explicit `APPROVED: publish v1.5` command before any publish/tag action (per RELEASE_NOTES_v1.5.md line 11).
