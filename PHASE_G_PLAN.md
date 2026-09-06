# PHASE G — FINAL IMPLEMENTATION PLAN
*Result + API + UI. Coordinator-only.*
*Approved: Q1=A (Phase G owns Result/Report assembly via decoupled `internal/result`), Q2=A (SSE polls `Master.State()`; SQLite events/EventStore deferred to H/J), Q3=A (authorized-browser auth-gate surface only; credential handling deferred to H).*
*Repo: `C:\Users\SkyMil\DRAW`, Go `1.26.6`, module `draw`. Git initialized on `main`; `.gitignore` written during initial setup (ignores `rd-engine.exe`, `*.exe`, `*.test`, `go.work`, `.env`, `.vscode`, `.idea`, OS junk). No remote. No source committed yet.*

---

## 1. Objectives
Implement the **Interface/API Layer + Result Layer** of V1 over the locked, fully-tested Phase-A–F engine: a Go-served Chatbox SPA backed by a REST API, an SSE progress stream, a deterministic `ResultEnvelope`/`Report` assembler, and an admin auth-gate — **with zero modifications to locked A–F internals.**

## 2. Verified Current Repository State
- `go build/vet/test -count=1 ./...` → **13 packages pass**, 251 test funcs. Phases A–F COMPLETE / LOCKED.
- `cmd/rd-engine/main.go:115` `_ = m` — Master fully constructed (`WithMemorySessions`, `WithIngestion`, `WithEvidenceStore(es)`, `WithSourceRegistry(src)`); **no HTTP server, no `Master.Run()`**. **Sole existing file Phase G edits.**
- Master public seams (concrete `*master.Master`, **not** modified): `NewMaster(cfg, orch, opts...) *Master`, `SubmitIntent(model.IntentRequest) (model.SessionID,error)` (m.go:104), `Run(ctx) error` (m.go:186, single-threaded admit→Execute→observeAndDecide), `State() ResearchState` (m.go:468, mutex+deep-clone), `Stop()` (m.go:477).
- `ResearchState` (master/state.go:48): `Session *model.Session`, `Plan *model.Plan`, `BudgetTotal/BudgetUsed int`, `EvidenceCount int`, `ReplanCount int`, `MaxReplans int`, `PhaseCompleted map[string]bool`, `PhaseIndex int`, `Terminal bool`, `TerminalReason string`, `Evidence EvidenceCounts`, `UpdatedAt time.Time`. `PlanPhaseOrder` = 8 phases; `PhaseForTaskType`; `MarkPhaseCompleted`/`SetTerminal` mutate under master mutex; `Clone()` is thread-safe.
- Storage seams (read-only): `EvidenceStore.Query(EvidenceFilter{SessionID,Topic,Verification}) []Evidence` returns all-session rows `ORDER BY collected_at ASC` (empty `Verification`/`Topic` = no filter → all). `EvidenceStore.FindRelations(topic) []EvidenceRelation` (all kinds, ordered). `SourceRegistry.Lookup(domain) (*SourceProfile,bool)`. `SourceID == domain string`; `model.NewSourceID(d)=SourceID(d)`.
- `model.ResultEnvelope/Report/ReportSource/Finding/ContradictionView` exist (model/result.go:15-56), **never instantiated**. `ResultEnvelope.Errors []error` → API layer must convert to stable `[]string` for JSON.
- `auth.RequireAdmin`, `IsAdmin`, `AdminEnabled`, `Redact` exist (auth.go) — fail-closed bearer, env-only `DRAW_ADMIN_TOKEN`.
- `SchedulerStats` (orch.Stats()) exposes at least `Queued`, `Active` — used only for admin telemetry.
- `IntentRequest{Query string; Seeds []string; UserID string}` (model/intent.go). `Intent{Entity,Topics,SourceClasses,Operations,OutputType,CrawlDepth,TaskDepth,EffortBudget,MaxReplans,Seeds}`. `Plan{Phases []Phase{Name,Tasks,Completed}}`.
- Empty placeholder dirs: `internal/api/`, `internal/session/`. New: `internal/result/`.

## 3. Exact Phase-G Scope
1. `internal/result/` — deterministic `ReportBuilder`: `ResearchState` + `EvidenceStore` + `SourceRegistry` → `model.ResultEnvelope` (read-only).
2. `internal/api/` — REST + SSE + Chatbox SPA. `server.go,deps.go,routes.go,handlers_sessions.go,handlers_evidence.go,handlers_admin.go`, `sse.go`, `chatbox.go`, `static/{index.html,app.js,app.css}`.
3. `cmd/rd-engine/main.go` — replace `_ = m` with HTTP server bootstrap + deps injection + `go m.Run(ctx)` per session.
4. Tests for each new package + `internal/integration/api_test.go` E2E + Verification + `PHASE_G_REPORT.md`.

## 4. Exact Non-Goals (Deferred)
- Persistent `SQLiteSessionStore` (sessions/sessions_recovery) → **Phase H** (prototype uses `MemorySessionStore`).
- Authorization of users / user-login session auth → **Phase H** (only admin bearer in Phase G).
- Authorized-browser credential capture/storage/real chromedp authed sessions → **Phase H** (`Decision.CanAuthorizeBrowser` stays `false`; `AUTH_REQUIRED` surfaced as terminal).
- `EventStore` port + SQLite `events` append-log from Master → **Phase H/J** (SSE polls `State()`).
- Multi-session runtime / switching → **Phase J** (one ACTIVE session, Cor.6).
- Report *generation* depth beyond the single-shot assembler (no LLM; missing-primary/stale-source counting already dormant).
- Production hardening (TLS/CSP/HSTS/rate-limiting at app layer, real recovery persistence) → **Phase J**.
- Microservices; SERP integration; AI/LLM; paid APIs.

## 5. Architecture
```
Chatbox SPA (internal/api/static)  ←HTTP→  internal/api (REST+SSE)  ←calls→  internal/master (SubmitIntent/Run/State/Stop)
                                          └──→ internal/result (ReportBuilder) ← reads → internal/storage (EvidenceStore/SourceRegistry)
                                          └──→ internal/auth (RequireAdmin) gated admin routes
                                          main.go wires deps + spawns go m.Run(ctx)
```
Master Run loop, Decision, Planning, State, Evidence, Frontier, Scheduler, Ingestion, Storage schema, model, config, auth, browser, workers, managers — **all LOCKED, consumed read-only.**

## 6. Packages/Files to CREATE
| File | Owner | Purpose |
|---|---|---|
| `internal/result/result.go` | Result | `ReportBuilder` + `Build(sid, state) (*model.ResultEnvelope, error)` |
| `internal/result/result_test.go` | Result | determinism + field-coverage unit tests |
| `internal/api/sse.go` | SSE | `SSEHandler` (polls `State`, emits phase/progress/report/error) |
| `internal/api/sse_test.go` | SSE | event derivation + admin gate |
| `internal/api/deps.go` | API | `Deps{Master, EvidenceStore, SourceRegistry, Auth}` |
| `internal/api/routes.go` | API | `RegisterRoutes(mux, deps)` |
| `internal/api/server.go` | API | `NewServer(deps)` / HTTP bootstrap helpers |
| `internal/api/handlers_sessions.go` | API | `POST /sessions`, `GET /sessions/{id}`, `POST /sessions/{id}/stop` |
| `internal/api/handlers_evidence.go` | API | `GET /sessions/{id}/evidence`, `GET /sessions/{id}/sources`, `GET /sessions/{id}/result` |
| `internal/api/handlers_admin.go` | API | `GET /admin/...` (RequireAdmin) |
| `internal/api/handlers_test.go` | API | httptest REST handler tests |
| `internal/api/chatbox.go` | Chatbox | `//go:embed static/*`; serves SPA at `/` |
| `internal/api/static/index.html` | Chatbox | minimal HTML shell |
| `internal/api/static/app.js` | Chatbox | fetch POST /sessions + EventSource /events; render |
| `internal/api/static/app.css` | Chatbox | minimal styles |
| `internal/integration/api_test.go` | Integration | E2E: submit→Run→SSE phases+report |

## 7. Packages/Files ALLOWED to Modify
- `cmd/rd-engine/main.go` — **only** existing file edited (replace `_ = m`, keep all Phase-F construction; add HTTP server + `go m.Run(ctx)` + deps). No other A–F source file edited.

## 8. LOCKED Files (must NOT be touched)
`internal/model/*`, `internal/config/*`, `internal/orchestrator/*`, `internal/frontier/*`, `internal/ingestion/*`, `internal/managers/*`, `internal/workers/*`, `internal/browser/*`, `internal/auth/*` (consume only), `internal/storage/{ports,sqlite,migrations,sqlite_test,sqlite_evidence,sqlite_source,sqlite_evidence_test,sqlite_source_test}.go`, `internal/master/{master,evidence,decision,planning,intent,state,memory_store,control}.go` + all A–F tests. `go.mod` unchanged (stdlib `//go:embed` only; no new deps).

## 9. API Contracts
**REST:**
- `POST /api/v1/sessions` {IntentRequest} → `201 {session_id}`; server calls `Master.SubmitIntent` then starts `go m.Run(ctx)`.
- `GET /api/v1/sessions/{id}` → `ResearchState` snapshot JSON.
- `POST /api/v1/sessions/{id}/stop` → `Master.Stop()`.
- `GET /api/v1/sessions/{id}/result` → `ResultEnvelope` JSON (via `ReportBuilder`).
- `GET /api/v1/sessions/{id}/evidence` → `[]Evidence`.
- `GET /api/v1/sessions/{id}/sources` → `[]ReportSource`.
- `GET /api/v1/sessions/{id}/events` → SSE (user).
- `GET /api/v1/admin/sessions/{id}/events` + `GET /api/v1/admin/sessions/{id}` → SSE/State (require `auth.RequireAdmin`).
- `GET /` → Chatbox SPA.
**SSE (`text/event-stream`):** `phase {phase,status∈started|running|completed}`, `progress {progress%,phase,evidence,contradictions,budget_used,budget_total}` (derived from `State()`; progress% = `PhaseIndex/len(PlanPhaseOrder)`), `report <ResultEnvelope JSON>` (on Terminal), `error {error}`. Single active session (Cor.6).

## 10. ResultEnvelope/Report Design (ReportBuilder)
`result.NewBuilder(es storage.EvidenceStore, src storage.SourceRegistry)`. `Build(sid model.SessionID, st master.ResearchState) (*model.ResultEnvelope, error)` — read-only:
- `Status`=`st.TerminalReason`; `SessionID`=`st.Session.ID`; `CompletedAt`=wall-clock (not scoring); `BudgetUsed`=`st.BudgetUsed`; `PlanPhases`=`st.Session.Plan.Phases` (Completed via `st.Plan.Phases[i].Completed`); `Errors`=`[]string` (sorted; `[]error` not JSON-stable).
- `Report.Intent`=`st.Session.Intent`; `Entities`=`[Intent.Entity]` (+Topics if helpful); `TimeRange`=`*Intent.TimeRange` (deref to value).
- `Sources`: unique `evidence.SourceID`(domain) → `SourceRegistry.Lookup` → `{Name:domain, URL:"https://"+domain, Class, Quality}`; **sorted by domain**.
- `Findings`: evidence grouped by `Topic+Claim` → consensus `Value` (max-confidence among shared Claim within topic); `{Claim,Value,EvidenceIDs[] sorted,Status=verification string}`; **sorted by (Topic,Claim,Value)**.
- `Contradictions`: `FindRelations(topic)` (all topics) filtered `Kind==CONTRADICTS` → `{ClaimA=evA.Claim,ClaimB=evB.Claim,EvidenceIDA,EvidenceIDB,Strength}`; **sorted by (ClaimA,ClaimA,EvidenceIDA,EvidenceIDB)**.
- `Completeness`=`float64(completed phases)/len(Plan.Phases)`; `Confidence`=`float64(verified)/float64(total evidence)` (0 if none).
**Determinism:** all slices sorted; no `time.Now()` in ordering/scoring; map iteration only into sorted slices.

## 11. Chatbox/UI Contract
SPA uses **only HTTP** to `internal/api` (never imports `internal/master`). `app.js`: `POST /api/v1/sessions` → `EventSource /api/v1/sessions/{id}/events` → render `phase`/`progress`; on `report` render Findings/Sources/Contradictions with clickable URLs; "New session" button. Served via `//go:embed static/*` at `/` (chatbox.go). Boundary: `Chatbox ↔ REST/SSE API ↔ Master` (reqding §11) preserved.

## 12. Authentication/Admin Boundaries
- Admin routes (`/admin/*`) wrapped with `auth.RequireAdmin`. `auth.AdminEnabled` gates registration.
- Chatbox user: no auth (anonymous research).
- Authorized-browser: Phase G surfaces `AUTH_REQUIRED`/`JAVASCRIPT_REQUIRED` terminal statuses in findings; **no** credential capture (Phase H). No bypass: `BLOCKED` terminal (§0.8.2).

## 13. Persistence Boundaries
- Read existing SQLite `EvidenceStore`/`SourceRegistry` (locked schema, no new tables in Phase G).
- `MemorySessionStore` retained (no persistent `sessions`/`sessions_recovery` writer in Phase G — Phase H).
- Report built from post-`Terminal` SQLite state (consistent reads; `Query` `ORDER BY collected_at`).

## 14. Deterministic Behavior
- `ReportBuilder` sorts all output slices; `Completeness`/`Confidence` are fixed functions of counts.
- SSE phase/progress derived from `State().PhaseIndex/PhaseCompleted` (deterministic data) + `collected_at`-ordered evidence.
- `Errors` → sorted `[]string`. No `time.Now()` in ordering/scoring (only `CompletedAt` is wall-clock).

## 15. Tests (per component)
- `internal/result/result_test.go`: field mapping + deterministic ordering (identical input → identical JSON, ≥2 runs).
- `internal/api/sse_test.go`: `ResearchState`→events derivation; SSE MIME/framing; admin 403 without bearer.
- `internal/api/handlers_test.go`: REST via `httptest` with fake Master/Store; 403 admin-without-token; result endpoint.
- `internal/integration/api_test.go`: real Master + SQLite evidence → `POST /sessions` + `go m.Run` → SSE `phase`/`progress`/`report` + final `ResultEnvelope` correctness + determinism across 2 runs.

## 16. Integration Tests
E2E through the **real** (locked) engine with SQLite evidence store: submit intent → `Master.Run` → Scheduler/Workers/Managers/Ingestion/Evidence circuit → `State().Terminal` → `ReportBuilder.Build` → SSE `report` → Chatbox contract. Assert vertical-slice criteria #1–6 (round-trip, per-domain limit, frontier dedup, SSE phase+report, no goroutine leak).

## 17. Acceptance Criteria (Phase-G, from §0.10)
1. Task round-trips Master→Scheduler→Manager→EvidenceStore (Phase F proven; G exercises via `Run`). 2. SQLite persists session/task/evidence/source_profile (F proven). 3. Per-domain limit enforced (F). 4. Frontier dedups (F). **5. SSE streams `phase=started/running/completed` + final `report`.** 6. No goroutine leak after run (SSE poll stops on `Terminal`; `runtime.NumGoroutine` Δ≈0). 7. `go build/vet/test -count=1 ./...` PASS; Phase A–F regression PASS.


## 19. File-Ownership Matrix (strict, exclusive)
- `internal/api/sse.go`/`sse_test.go` → SSE only
- `internal/result/*` → Result only
- `internal/api/chatbox.go`, `static/*` → Chatbox only
- `internal/api/{deps,routes,server,handlers_*,handlers_test}.go` → API only
- `cmd/rd-engine/main.go` → API only
- `internal/integration/api_test.go` → Integration only
- `.gitignore`, `.git/` → Git only

## 20. Verification Strategy (independent, no repairs)
- `go build ./...`, `go vet ./...`, `go test -count=1 ./...` (13 A–F + 4 new packages).
- `go test -race` attempt (report honestly if CGO/gcc unavailable — environment limit).
- Locked-file audit: `git diff --name-only` ⊆ {`cmd/rd-engine/main.go`} ∪ new files under `internal/{api,result,integration}`; zero edits to locked set.
- Dependency audit: `go.mod`/`go.sum` unchanged.
- SSE progress+report E2E; admin 403 without bearer; no credential capture code; `AUTH_REQUIRED` terminal.
- Determinism: two identical-intent runs → byte-identical `ResultEnvelope` JSON.
- Goroutine leak: `runtime.NumGoroutine` Δ≈0 after SSE end + `Stop()`.

## 21. Phase-G Checkpoint / Report
**PHASE_G_REPORT.md** is written (after Verification green): `PHASE_G_REPORT.md`: status; files created/modified; phase execution history; locked decisions (Q1–Q3=A); verification results; known limitations (in-mem sessions, stubbed authorized-browser, SSE-by-poll, `Errors []error`→string); deferred Phase-H (persistent SessionStore, EventStore, replan-merge end-to-end w/ ≥2 sources+contradiction, authorized-browser runtime); Phase-H handoff instructions.

*End of plan.*
