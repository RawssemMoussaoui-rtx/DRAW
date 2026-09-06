# Phase G Report

## 1. Phase Status

Phase G is **complete**. Phase A–F are complete and locked (master/storage/model/config/auth/orchestrator/frontier/ingestion/managers/workers/browser not modified by Phase G). Go 1.26.6, module `draw`, CGO_ENABLED=0, Windows/amd64, no gcc. go.mod/go.sum unchanged; `go mod verify` → "all modules verified"; no new dependencies. No git commits (untracked repo). Ownership verified via import-graph inspection: `internal/api` and `internal/result` are imported ONLY by {internal/api/*, internal/integration/api_test.go, cmd/rd-engine/main.go}.

## 2. What Phase G Implemented

Phase G delivered the HTTP API server layer and deterministic terminal-reporting result builder that surface Phase-F master state over a web API. Concretely it added:

- A thin HTTP server (`internal/api`) exposing a `POST /sessions` → `{"session_id":"..."}` intent-submit endpoint, `GET /sessions/{id}` snapshot, `GET /sessions/{id}/result` full `model.ResultEnvelope`, and `GET /sessions/{id}/events` SSE stream.
- An admin gate (`internal/auth`, Phase-F, untouched) applied via `RequireAdmin` in `routes.go`, gating `GET /admin/sessions/{id}` and `GET /admin/sessions/{id}/events`. Token is ENV-only (`DRAW_ADMIN_TOKEN`), fail-closed, constant-time compare, no credential storage.
- A `cmd/rd-engine` entrypoint that wires `signal.NotifyContext` → `api.Serve(ctx, …)`.
- A deterministic report assembler (`internal/result`) plus a static browser front-end under `internal/api/static`.
- **Context-Fix (production path):** `Submit` derives a Run via `context.WithCancel(sm.srvCtx)` (server-lifetime, NOT `r.Context()`) so `go master.Run(runCtx)` survives the `POST /sessions` handler returning. SSE polls `Master.State()` every 250ms; on terminal state emits `report` (full `model.ResultEnvelope` via `ReportBuilder.Build`) then closes the stream, bounded by `r.Context().Done()` (client disconnect exits cleanly, no goroutine leak).
- **Single-active-session** enforced in `sessionsManager` (Cor.6): a new Submit stops the prior in-flight Run.

JSON contract: POST `/sessions` → `{"session_id":"..."}`; `GET /sessions/{id}` snapshot → `stateResponse` snake_case; `GET /sessions/{id}/result` → `model.ResultEnvelope` (PascalCase fields, no json tags — relies on Go field casing); SSE `phase`/`progress` snake_case; SSE `report` uses model struct casing.

## 3. Exact Phase-G Files Added/Modified

ADDED:
- `internal/result/result.go`
- `internal/result/result_test.go`
- `internal/api/deps.go`
- `internal/api/server.go`
- `internal/api/routes.go`
- `internal/api/handlers_sessions.go`
- `internal/api/handlers_data.go`
- `internal/api/handlers_admin.go`
- `internal/api/handlers_test.go`
- `internal/api/sse.go`
- `internal/api/sse_test.go`
- `internal/api/chatbox.go`
- `internal/api/static/index.html`
- `internal/api/static/app.js`
- `internal/api/static/app.css`
- `internal/integration/api_test.go`
- `cmd/rd-engine/main.go`
- `.gitignore` (repo-init artifact)

MODIFIED (Context-Fix only; the only source files touched):
- `internal/api/server.go`
- `internal/api/handlers_sessions.go`
- `internal/api/handlers_test.go`
- `internal/integration/api_test.go`

## 4. Approved Architectural Decisions

- Master stays a concrete struct; Phase G consumes `SubmitIntent` / `Run` / `State` / `Stop` only — no master refactor.
- Server-lifetime context for Run derivation (`sm.srvCtx`) rather than per-request `r.Context()`, so a session's Run outlives the HTTP handler.
- SSE polls state every 250ms (default `NewSSEHandler` interval) instead of push-driven events — no EventStore append-log needed in G.
- Memory-only `sessionsManager` in the api layer; no persistent session storage.
- Admin token read from ENV per request; ENV-only, fail-closed, `crypto/subtle.ConstantTimeCompare`; no credential storage.
- Single-active-session rule (Cor.6) enforced at the api orchestrator layer.
- Phase G scope is the authorized-browser/admin gate only; full browser auth + session lifecycle deferred to H.
- `go.mod`/`go.sum` unchanged — zero new dependencies.

## 5. Verification

All T4 verbatim results:

- `go build ./...` — PASS
- `go vet ./...` — PASS
- `go test -count=1 ./...` — PASS (all packages; full A–F regression + Phase-G)
- API tests — PASS (`internal/api`, 15 tests: SSE 4 + admin 3 + handlers 8)
- Result tests — PASS (`internal/result`, 6/6)
- Integration tests — PASS (`internal/integration`, incl. `TestPhaseF_EvidenceContradictionCircuit`, `TestPhaseG_APIE2E_ContradictionCircuit`, `TestPhaseG_APIE2E_ReportDeterministic`)
- Context-Fix E2E — PASS (real master, POST `/sessions` → Run survives handler → SSE emits terminal report)
- SSE terminal/disconnect tests — PASS (`TestSSE_TerminalClosesStream`, `TestSSE_ClientDisconnect`; `runtime.NumGoroutine` delta = 0)
- Admin auth tests — PASS (`TestAdmin_NoToken` 401, `TestAdmin_WrongToken` 403, `TestAdmin_RightToken` 200)
- `TestPhaseG_APIE2E_ReportDeterministic` — PASS (run twice; structural invariants equal)
- `go test -race` — FAIL only because `gcc not found` (environment limitation, not a code defect); attempted twice.
- `gofmt -l` on Phase-G files — clean. Pre-existing Phase-F files unformatted (gofmt -l lists many Phase-F files) — NOT a Phase-G defect; untouched.

## 6. Locked Boundaries Respected

- No edits to master/storage/model/config/orchestrator/frontier/ingestion/managers/workers/browser logic.
- `go.mod`/`go.sum` unchanged; no new dependency (go mod verify: all modules verified).
- No EventStore / events append-log (deferred to H).
- No persistent session storage (memory-only sessionsManager in api layer; Master keeps Phase-F in-memory sessions).
- No authorized credential storage (admin token read ENV-only per request).

## 7. Phase H Handoff

- Persistent SessionStore + session recovery across restarts (replace in-memory `sessionsManager`).
- EventStore / events append-log durability (SSE event-log deferred from G).
- Multi-session runtime (Phase G enforces single active session per Cor.6; lift to multi-session).
- Authorized-browser credential/session flow (browser auth + session lifecycle; deferred from Q3=A scope).
- Remaining runtime hardening: production Chromium browser adapter, DB migration path `data/draw.db`, SIGTERM/SIGINT teardown, end-to-end production integration.
