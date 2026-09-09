Okay, okay — finally, after two weeks away, I'm done fixing the infrastructure
completely. Now I can actually focus on the intelligence of the engine itself.
But before I get into anything architecture-related (covered further down), I
want to talk to people in plain language for a second.

Nobody has opened this repository yet, but if you do, here's at least what you
should know: DRAW is a side project I decided to publish. My actual main
project is something completely separate from DRAW — but it needs DRAW. Since
I'd already built every tool that project uses myself, and since I'm also
broke (ha), still a minor, no bank account, can't even afford the API costs
that would let an agent browse the internet — I badly needed a data source. So
I built DRAW.

Unlike the other tools I've written, which are built entirely for my main
project's specific use, I realized DRAW might actually be useful to other
broke people like me who need a service that's relatively cheap on hardware
resources, runs fully locally, and has stable infrastructure — with the ability,
later (not yet, but in future updates), to plug any model on top of it. It's
also the only project out of everything I've built that I think is genuinely
useful to a regular user, or even to systems that rely on data sources.
Everything else I've written is entirely dedicated to my main project and
wouldn't really be useful to anyone else — and it's all written in Python
anyway, so it's not hard to build.

I'm sorry in advance, because this project and its updates are entirely tied
to my main project. If you see DRAW getting updates, that means my main
project found an investor and is succeeding. If it stops, I apologize in
advance for that too.

Go might seem like an unconventional choice of language for a project like
this, but I found it's genuinely the best fit for this kind of system — and I
happened to already be learning it, so, well, that's how it happened (ha).
My actual specialty is Python, but Go is undoubtedly the right call for this
project. So if development stops, you're welcome to keep building on this
repository yourself.

One important warning though: this is NOT open source. If I do open-source it
eventually, I still need to retain the rights, because it's part of another
project. If I license it in a way that's too permissive, it could hurt me and
my main project, which is aimed at companies, not individual users.

This is me — Rawssem Moussaoui, for context. I was racing against time to
finish DRAW before school starts back up. Wish me luck — I'm a junior this
year (math track). I'll keep developing DRAW, but I'm going to slow down to
focus on school, so don't expect any big updates soon.

And now, presenting: DRAW V2.

---

## What is DRAW?

DRAW is a local, deterministic, resource-efficient web research and
evidence-gathering engine. It takes a research intent (a query plus an entity
and optional time range) and autonomously gathers web evidence, extracts
claims, de-duplicates and scores sources, detects contradictions, and
produces a structured result envelope — all running fully locally on commodity
hardware, with no external model API dependencies. It is built for operators
who need stable, reproducible intelligence at low hardware cost.

The engine is written in Go and exposes a small HTTP API plus a static browser
front-end. Storage is backed by SQLite (pure-Go driver, no CGO required).

---

## DRAW V2 — Closure of the Initial Correctness/Infrastructure Pass

V2 closes the initial correctness and infrastructure phase of the project.
Every item below was resolved and verified — the engine now builds cleanly,
passes `go vet`, and the full test suite is green. Below is a plain-language
summary of what was closed out. Future work moves to engine *capability* and
*intelligence*, not infrastructure.

### Resolved items

- **Evaluation harness (deterministic N=5).** A deterministic regression
  harness was built under `internal/evalharness` that runs five fixed
  scenarios (A–E) end-to-end against the real evidence pipeline, reading
  results back through the SQLite event/evidence stores rather than any
  in-memory shortcut. This is the single source of truth for correctness
  before any change ships.

- **Determinism verification (byte-identical output).** The full pipeline —
  planning, scheduling, evidence extraction, relation scoring, and result
  assembly — produces byte-identical output across repeated runs. All slices
  are explicitly sorted before serialization; no `time.Now()` value
  participates in ordering or scoring; map iteration is always funneled into
  a sorted slice first. The harness's determinism check (`M7`) passes on
  every green build.

- **Concurrency and storage concurrency fixes (deadlock-free admission).**
  The scheduler and resource controller now admit tasks under concurrent
  access without deadlock. A context-handling bug that could orphan a
  session's master `Run` goroutine when the `POST /sessions` handler
  returned was fixed: the run derives its context from server lifetime
  (`context.WithCancel(sm.srvCtx)` at `internal/api/server.go`), so the session survives the HTTP request
  without leaking goroutines.

- **Scoring saturation and quota fairness.** The tanh-saturation logic for
  `ContradictionValue` was corrected so that contradiction strength saturates
  cleanly rather than producing runaway edge weights. The exploration-quota
  floor is now enforced deterministically, preventing any single source or
  claim family from consuming the entire exploration budget.

- **Deterministic session termination.** Shutdown now handles stuck workers
  and timeout drains cleanly. A `Cancel-stuck-workers` path drains workers on
  the exhausted-timeout case, and the `StopPending` idempotency guard was
  hardened so concurrent `DecisionStop` calls cannot double-fire or leave the
  session in a half-terminated state.

- **Evidence provenance (origin + extraction sequence).** Each evidence item
  now carries its origin URL and a per-task extraction sequence, recorded
  through the source registry and persisted alongside the evidence row. This
  makes every claim attributable to a concrete retrieval step for audit.

- **Verification scope (precisely-scoped recompute).** Verification recompute is scoped to only {topics touched by new evidence} ∪ {topics whose evidence source quality changed}, tracked via an in-memory dirty-topic set and a lastQualityMap snapshot, batched at a single barrier per observeAndDecide cycle (no full-session recompute).

- **Contradiction-similarity calibration.** The paraphrase/similarity gate
  (`internal/evidence/relations.go`, `internal/evidence/similarity.go`) uses
  deterministic n-gram TF–Jaccard (n ∈ {1,2,3}) with τ = 0.8. The expanded
  test suite in `internal/evidence/similarity_test.go` and
  `internal/evidence/calibration_test.go` covers overlap strata 0.7–0.9
  with near-synonym adversarial pairs, confirming the gate suppresses
  paraphrases (overlap ≥ τ) while still emitting `CONTRADICTS` for true
  disagreements (overlap < τ).

- **Session-scoped URL de-duplication.** A shared, long-lived Frontier and Scheduler have their internal in-memory state rekeyed by a composite (SessionID, canonicalURL) key, with an explicit ResetSession() cleanup path invoked at session start (idempotent pre-cleanup) and at session end/cancellation (defer in Run).

### What's next

With infrastructure and correctness closed out, development shifts to engine
intelligence: improving extraction coverage, tuning the contradiction and
scoring models, and expanding the model plug-in surface. See
`DEVELOPMENT_HISTORY.md` for the full milestone-by-milestone record.

---

## Build & Run

DRAW is a standard Go module (`module draw`, Go 1.26.6). No CGO is required —
the SQLite driver is `modernc.org/sqlite` (pure Go).

```bash
# Build the binary
CGO_ENABLED=0 go build -o rd-engine ./cmd/rd-engine

# Run the engine (serves the HTTP API + static front-end on :8080)
./rd-engine

# Or run directly without producing a binary
CGO_ENABLED=0 go run ./cmd/rd-engine
```

### Configuration

All settings have sane defaults; no config file is required. Override via a
TOML file (path set with `DRAW_CONFIG`) or environment variables:

| Variable | Default | Notes |
|---|---|---|
| `DRAW_CONFIG` | *(none)* | Path to a TOML config file |
| `DRAW_HTTP_ADDR` | `:8080` | HTTP listen address |
| `DRAW_ADMIN_TOKEN` | *(disabled)* | Admin-gate token (ENV-only, fail-closed); omit to disable admin endpoints |
| `DRAW_SQLITE_DSN` | `file:./data/draw.db?mode=rwc` | SQLite DSN |
| `DRAW_CHROMIUM_PATH` | *(none)* | Chromium binary path for browser-fetch tasks; required only when browser escalation is used |

A config file example (TOML):

```toml
max_global_concurrency = 6
domain_limits = { "example.com" = 2 }
cpu_threshold = 0.85
ram_threshold = 0.80
```

### API

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/v1/sessions` | Submit a research intent → `201 { session_id }` |
| `GET` | `/api/v1/sessions/{id}` | Snapshot of current research state |
| `POST` | `/api/v1/sessions/{id}/stop` | Stop a running session |
| `GET` | `/api/v1/sessions/{id}/result` | Full result envelope |
| `GET` | `/api/v1/sessions/{id}/evidence` | Evidence list |
| `GET` | `/api/v1/sessions/{id}/sources` | Source list |
| `GET` | `/api/v1/sessions/{id}/events` | SSE stream (user) |
| `GET` | `/api/v1/admin/sessions/{id}/events` | SSE stream (admin) |
| `GET` | `/` | Browser front-end |

Admin endpoints (prefixed `/api/v1/admin/*`) require a valid `DRAW_ADMIN_TOKEN`.

### Tests

```bash
# Full suite (A–F regression + V2 verification)
go test -count=1 ./...

# Just the evidence/similarity calibration
go test ./internal/evidence/...

# Just the deterministic N=5 harness
go test ./internal/evalharness/...
```

---

## License

This repository is **not open source**. DRAW is part of a larger proprietary
project. The author retains all rights. Do not fork or redistribute. If you
see DRAW get open-sourced in the future, a permissive license is unlikely —
retaining rights is necessary to protect the main project, which targets
companies rather than individual users. See the personal note at the top of
this file for the full explanation.
