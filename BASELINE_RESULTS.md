# Baseline measurement of unmodified V1

Reproducer: internal/evalharness TestBaseline (P13: deterministic, no time.Now).

Timestamp (UTC): 2026-08-23T16:06:51Z
Git commit: a4f93bd67dc27f522246abb386e1b253e52b187d

## Scenario A

M1: {"Predicted":1,"Expected":1,"Accuracy":1}
M2: {"FP":1,"TP":5,"Total":6,"Rate":0.16666666666666666}
M3: {"Detected":0,"Total":0,"Rate":0}
M4: {"Collapsed":0,"Total":0,"Recall":0}
M5: {"Slope":0,"Window":1,"Novelty":[1]}
M6: {"Matches":4,"Total":4,"Accuracy":1,"Expected":{"VERIFIED":4},"Observed":{"VERIFIED":4}}
M7: {"Equal":true}

## Scenario B

M1: {"Predicted":3,"Expected":3,"Accuracy":1}
M2: {"FP":0,"TP":0,"Total":0,"Rate":0}
M3: {"Detected":0,"Total":0,"Rate":0}
M4: {"Collapsed":0,"Total":0,"Recall":0}
M5: {"Slope":0,"Window":1,"Novelty":[1]}
M6: {"Matches":3,"Total":3,"Accuracy":1,"Expected":{"UNVERIFIED":3},"Observed":{"UNVERIFIED":3}}
M7: {"Equal":true}

## Scenario C

M1: {"Predicted":2,"Expected":2,"Accuracy":1}
M2: {"FP":0,"TP":0,"Total":0,"Rate":0}
M3: {"Detected":1,"Total":1,"Rate":1}
M4: {"Collapsed":0,"Total":0,"Recall":0}
M5: {"Slope":0,"Window":1,"Novelty":[1]}
M6: {"Matches":2,"Total":2,"Accuracy":1,"Expected":{"DISPUTED":2},"Observed":{"DISPUTED":2}}
M7: {"Equal":true}

## Scenario D

M1: {"Predicted":6,"Expected":6,"Accuracy":1}
M2: {"FP":0,"TP":24,"Total":24,"Rate":0}
M3: {"Detected":0,"Total":0,"Rate":0}
M4: {"Collapsed":0,"Total":0,"Recall":0}
M5: {"Slope":-0.16142857142857145,"Window":6,"Novelty":[1,0.6666666666666666,0.3333333333333333,0.25,0.2,0.16666666666666666]}
M6: {"Matches":24,"Total":24,"Accuracy":1,"Expected":{"PARTIALLY_VERIFIED":24},"Observed":{"PARTIALLY_VERIFIED":24}}
M7: {"Equal":true}

## Scenario E

M1: {"Predicted":2,"Expected":1,"Accuracy":0}
M2: {"FP":0,"TP":0,"Total":0,"Rate":0}
M3: {"Detected":0,"Total":0,"Rate":0}
M4: {"Collapsed":0,"Total":1,"Recall":0}
M5: {"Slope":0,"Window":1,"Novelty":[1]}
M6: {"Matches":2,"Total":2,"Accuracy":1,"Expected":{"UNVERIFIED":2},"Observed":{"UNVERIFIED":2}}
M7: {"Equal":true}

## Findings

- Scenario A: chain of duplicated/derived sources. 12 SUPPORTS edges, 4 VERIFIED. M1 accuracy=1, M2 FP=1/TP=5.
- Scenario B: genuinely independent sources. 0 edges, 3 UNVERIFIED. M1=3.
- Scenario C: known contradiction. 2 CONTRADICTS edges (incident count=2 >= K=2), 2 DISPUTED. M3 detected.
- Scenario D: saturation. 80 SUPPORTS edges across 6 independent groups; declining novelty; 24 PARTIALLY_VERIFIED. M2 not asserted (see note).
- Scenario E: paraphrase. ValueSimilarity=0.857 >= tau=0.8 suppresses the CONTRADICTS edge -> 0 edges -> UNVERIFIED (corrected per P12; the stale V1.0 prose that said DISPUTED has been reconciled to UNVERIFIED).

## Performance & Resource Measurements

Per-scenario measurements taken around `RunV1OnScenario` (the single scenario entry point). Timing uses time.Since; memory uses runtime.ReadMemStats before/after; DB queries are counted via a counting SQLite driver wrapper (see perf.go) whose only behaviour is incrementing a counter — it is wired purely within this package and touches no Tier B / production file. Migrations applied in setupTestStore are excluded (the counter is reset at the start of RunV1OnScenario).

Time and memory are inherently non-deterministic and are expected to differ between runs; the M1-M7 section above is deterministic (no time.Now) and should be byte-identical across runs. The DB query count is structural and is likewise stable across runs.

### Scenario A
- Elapsed: 2.629 ms
- Allocated (TotalAlloc delta): 36544 bytes
- Live heap (Alloc): 418480 bytes
- DB queries: 27

### Scenario B
- Elapsed: 1.046 ms
- Allocated (TotalAlloc delta): 21648 bytes
- Live heap (Alloc): 434216 bytes
- DB queries: 13

### Scenario C
- Elapsed: 1.032 ms
- Allocated (TotalAlloc delta): 22368 bytes
- Live heap (Alloc): 441752 bytes
- DB queries: 13

### Scenario D
- Elapsed: 7.589 ms
- Allocated (TotalAlloc delta): 180344 bytes
- Live heap (Alloc): 615016 bytes
- DB queries: 135

### Scenario E
- Elapsed: 0s
- Allocated (TotalAlloc delta): 22976 bytes
- Live heap (Alloc): 468344 bytes
- DB queries: 11

## Performance measurement methodology

- Elapsed: time.Since(start) around the full RunV1OnScenario body (inject evidence, ComputeRelations, persist edges, ComputeVerification, persist verification, append synthetic events, R3 read-back).
- Allocated (TotalAlloc delta): runtime.MemStats.TotalAlloc difference measured after a runtime.GC baseline, capturing total bytes allocated during the scenario.
- Live heap (Alloc): runtime.MemStats.Alloc immediately after the scenario (live heap).
- DB queries: atomic counter incremented on each Exec/Query dispatched through the counting driver connection during the scenario.
