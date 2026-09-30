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
resources, runs fully locally, and has stable infrastructure — with the ability
to plug an external model on top of it. It's also the only project out of
everything I've built that I think is genuinely useful to a regular user, or
even to systems that rely on data sources. Everything else I've written is
entirely dedicated to my main project and wouldn't really be useful to anyone
else — and it's all written in Python anyway, so it's not hard to build.

I'm sorry in advance, because this project and its updates are entirely tied
to my main project. If you see DRAW getting updates, that means my main
project found an investor and is succeeding. If it stops, I apologize in
advance for that too.

Go might seem like an unconventional choice of language for a project like
this, but I found it's genuinely the best fit for this kind of system — and I
happened to already been learning it, so, well, that's how it happened (ha).
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

And now, presenting: DRAW V3.0 — Prototype (D1 Public Release).

---

## What is DRAW?

DRAW is a local, fully deterministic web research and evidence-gathering engine.

**Permanently by design — no LLM, no ML, nowhere inside.** The engine gathers
web evidence, extracts claims, de-duplicates and scores sources, detects
contradictions, classifies evidence relations, and produces a structured
result envelope — all running on deterministic character-level and token-level
comparisons alone. There is no statistical model, no neural network, no
external API dependency, not now and not ever. The engine's decision path is
inviolable by any external influence, period. Any machine-learning or
LLM-powered analysis lives *outside* the engine, consuming its output as a
constrained analyst — it never participates in gathering or verifying
evidence.

It is written in Go (`module draw`, Go 1.26.6) and exposes a small HTTP API
plus a static browser front-end. Storage is backed by SQLite (pure-Go driver,
no CGO required).

---

## The LLM-agent feature journey

DRAW itself does not browse, does not reason, does not call models. That is the
point. But an external LLM — or any agent that cannot browse — still needs a
path to grounded web data. V3 delivers that path as a thin protocol layer at
`/api/v2/agent/*`.

The layer does five things: it lets an external agent create a session and
receive a deterministic metal-prompt briefing (the engine's exact data schema,
budget counts, and endpoint map, in JSON); it exposes the engine's live,
sourced evidence state as a snapshot the agent can read; it streams the same
events back over SSE with agent-specific replan-budget and inactivity
metrics; it exposes a bounded replan endpoint so the agent can request the
engine re-explore a gap; and it returns a full, exclusion-reason-annotated
result envelope.

The design tension this solves is straightforward and non-negotiable in
spirit:

```
deterministic core
+
LLM-facing layer
```

The LLM-facing layer sits *alongside* the deterministic core, never *inside*
it. The engine's planning, scheduling, evidence extraction, relation
scoring, and verification assembly are all untouched by the agent protocol.
An external agent consumes the engine's raw, sourced output as a constrained
analyst — it reads what the engine produced, it can request a replan through a
strictly gated channel, but it never browses independently, never decides
independently, and never touches the deterministic decision path. The
external LLM is a consumer of determinism, not a participant in it.

Two concrete problems drove this layer into existence:

- **Token efficiency.** An agent that browses independently burns tokens on
  every page it fetches and parses, then must synthesize claims from raw
  HTML. By contrast, the agent here consumes pre-extracted, de-duplicated,
  contradiction-scored evidence — the engine has already done the expensive
  retrieval and structuring work. The agent's context is evidence, not
  web pages.

- **Agents that cannot browse.** Not every agent has a browsing tool, and not
  every deployment can afford one. The protocol layer gives any agent —
  browserless, key-constrained, cost-locked — a direct, stable path to
  grounded, sourced web data without ever asking it to browse.

The layer is deliberately narrow. It does not extend the engine's internal
API surface. It does not add new decision paths inside the core. It reads
state, streams state, and forwards replan requests through the 8-layer
protection gate — quota, non-refundable deduction, active-session check, 15
second cooldown, enumerated reason-code, validated target-scope, semantic
hash de-duplication, and circuit breaker. Nothing in that gate modifies the
engine's deterministic planning or verification logic. The core is
observably untouched.

---

## Explicit status flag

The agent-protocol layer is

```
an incomplete prototype feature
```

This is not a roadmap placeholder, a beta badge, or a "coming soon." V3
delivers a working set of five endpoints, a deterministic metal-prompt
template, and a full determinism test suite — but it is explicitly, knowingly
incomplete. There is no browser-auth flow, no persistent session store, no
multi-session runtime, and no authorized credential management. The layer
exists to stand in the gap between a browserless external agent and grounded
data, but it does not yet close that gap end to end. Treat it as a prototype:
functional, bounded, and unfinished.

---

## New in this release — relation-classification algorithms

V3 introduces two new deterministic algorithms in the evidence relation
pathway. Both live in `internal/evidence/` and operate on the same principle:
two evidence values can express the same fact using different wording, and a
deterministic character/trigram comparison is sufficient to recognize that.
There is no semantic understanding. There is no ML reasoning. There is no LLM
reasoning in the deterministic relation-path.

**BCNE — Bidirectional Character N-gram Envelope.**

BCNE powers paraphrase-aware evidence relation detection. Where the previous
generation emitted a `CONTRADICTS` edge for any two same-Claim items with
differing Values (or in V1.6, suppressed the edge entirely on high overlap),
BCNE classifies the relationship into one of three deterministic branches:

1. If either Value contains a negation anchor (`HasNegationAnchor` fires) →
   `CONTRADICTS` at strength 1.0. This is ANRB's hard veto.
2. If the character-trigram coverage between the two Values
   (`BcneCoverage`, multiset Jaccard over lowercased length-3 substrings) is
   ≥ 0.6 → `SUPPORTS`. The pair expresses the same fact in different
   wording; it is a paraphrase, not a dispute.
3. If neither condition holds → `CONTRADICTS` at strength 1.0. Low overlap
   and no negation means a genuine factual disagreement.

The shift from suppression to acceptance is the single substantive change.
Scenario E — two independent sources reporting "Paris is the capital of
France" and "The capital city of France is Paris" — now produces two
`SUPPORTS` edges, collapsing into a single corroborated group and resolving
to `PARTIALLY_VERIFIED`. Previously, the suppression gate left the pair
unclassified: zero edges, both `UNVERIFIED`, invisible to the
verification state machine. BCNE closes that deterministic gap.

**ANRB — Anchor-Guard.**

ANRB is the negation-anchor hard veto that protects the BCNE gate from false
positives on surface-similarity deception. Character-trigram coverage alone
cannot distinguish "Paris is the capital of France" from "France is not the
capital of Paris" — the trigrams overlap by more than 0.6, yet the two
statements are diametrically opposed. Without ANRB, BCNE would accept the
negated pair as `SUPPORTS`, a false positive. ANRB prevents this by forcing
a `CONTRADICTS` classification whenever a negation token is present in either
Value, regardless of coverage. The guard matches English negation tokens —
"not", "no", "never", "none", "nothing", "neither", "nor", "cannot",
"without", "hardly", "barely", "scarcely", and their contractions — at the
whole-word, case-insensitive level after stripping surrounding punctuation,
so that lexical look-alikes like "notice" or "nothingness" do not trigger it.

Together, BCNE and ANRB form a two-layer classifier: ANRB first, to catch
negation-based deception, then BCNE, to recognize paraphrased agreement.
Neither layer invokes a model. Neither layer invokes the network. The entire
decision is reproducible, auditable, and byte-identical across runs.

---

## The exploration, stated honestly

Five candidate algorithms were built and rigorously tested for the
relation-classification gap:

```
SCWC
BCNE
CDS
SETM
CWES
```

Of these, three did not hold up under testing and were discarded:

- **CDS** — did not hold up under testing. Discarded.
- **SETM** — did not hold up under testing. Discarded.
- **CWES** — did not hold up under testing. Discarded.

Two succeeded:

- **BCNE** — succeeded.
- **SCWC** — succeeded.

> BCNE is the one shipped in this release.

BCNE was selected because it is deterministic, pure-stdlib, and directly
addresses the paraphrase-vs-contradiction classification without requiring a
semantic model. SCWC is real, tested, and works, but is being held for a
future release. Anyone wanting SCWC should watch for the next update.

---

## The journey / problems solved

The work that shaped V3 resolves around a single, persistent question that
has shadowed DRAW from the start: *how do you reliably tell that two sources
are saying the same thing when they say it differently?*

**The problem that existed.** The original evidence relation engine classified
every same-Claim, different-Value pair as a `CONTRADICTS` edge. Two sources
could report the identical fact — "Paris is the capital of France" and "The
capital city of France is Paris" — and the engine would record their
relationship as a dispute. Downstream, the verification state machine would
see two `CONTRADICTS` edges incident to each item, hit the dispute threshold,
and mark both as `DISPUTED`. The engine had no concept of paraphrase.
Paraphrased corroboration was misread as contradiction, and every
paraphrased source poisoned the trust signal for its claim.

The first fix attempted abstention: when two Values were highly similar, the
edge was suppressed entirely — no relation emitted at all. That closed the
false-contradiction window, but it opened a different one. A suppressed pair
leaves zero edges in the relation graph. The verification state machine sees
no `SUPPORTS`, no `CONTRADICTS`, no `DUPLICATES` — only `UNVERIFIED`. The
paraphrased evidence becomes invisible. It does not corroborate. It does not
dispute. It simply does not exist in the machine's eyes. Two independent
sources confirming the same fact were erased to the same state as two sources
that had never found each other at all.

**What was difficult.** Recognizing that the right move was not suppression
but classification — accepting the paraphrase as a positive `SUPPORTS`
relation rather than erasing it — required the gate to do more than detect
similarity. It had to distinguish "same fact, different wording" from "same
words, opposite meaning." The danger case is negation: "Paris is the capital
of France" and "France is not the capital of Paris" share the overwhelming
majority of their character trigrams, yet they disagree. A coverage gate
alone cannot tell them apart. The engine needs an anchor — a linguistic
signal that transcends character overlap and forces the classification to
flip. Without that anchor, accepting paraphrases opens the door to accepting
negated contradictions as corroboration, which is a false positive the engine
cannot afford.

**What the fixes achieve.** BCNE and ANRB together close the gap in both
directions. ANRB — the Anchor-Guard — detects negation tokens at the
whole-word level and forces a `CONTRADICTS` classification whenever negation
is present, regardless of coverage. BCNE — the Bidirectional Character
N-gram Envelope — then classifies the remaining pairs: high character-trigram
overlap (≥ 0.6) indicates paraphrase and produces a `SUPPORTS` edge; low
overlap with no negation indicates a genuine contradiction and produces a
`CONTRADICTS` edge. A paraphrased pair is no longer erased; it is recorded,
it corroborates, and it participates in the verification state machine as
`PARTIALLY_VERIFIED`. A negated pair is no longer misread; it is caught by
the anchor and classified as `DISPUTED`. The engine gained the ability to
recognize agreement and disagreement through deterministic comparison alone —
no model, no network, no ambiguity.

**Why those problems mattered.** Evidence that agrees but is recorded as
disputing corrupts the downstream trust model: source quality scores decay,
replans fire on phantom contradictions, and consensus collapses into
disputed noise. Evidence that agrees but is recorded as invisible is even
worse — the engine behaves as though the corroborating source never existed,
underweighting claims that two independent sources confirmed. Both failures
distort the only signal the user ultimately receives: the structured result
envelope. The fixes restore a fundamental property of the evidence graph —
that agreement begets `SUPPORTS`, disagreement begets `CONTRADICTS`, and
neither begets silence.

**What principles the fixes embody.**

The first principle is that **deterministic comparison is sufficient for the
relation-classification problem at scope.** Character-level trigram overlap,
combined with a whole-word negation anchor, is not "semantic understanding" —
it does not know what "capital" means — but it is enough to separate
paraphrase from contradiction for the evidence values the engine processes.
There is no call to widen the gate, no learned model, no threshold tuning
beyond the calibrated 0.6. The gate is a fixed function of its inputs, and
that is its strength: a future reader can reproduce every classification by
hand.

The second principle is that **the outer layer must never reach the inner
path.** The agent protocol layer reads the engine's state, streams it, and
forwards replan requests through a gate — but it does not alter how evidence
is scored, how relations are classified, or how verification resolves. The
external LLM/agent is a constrained analyst: it consumes the engine's output
and can ask for more work, but it cannot rewrite the engine's logic. This
distinction is the reason DRAW can advertise "permanently by design"
determinism even as it ships an LLM-facing layer: the layer is adjacent to
the core, not entwined with it. An external model can prompt, but it cannot
decide.

The third principle, inherited from the V2 replan-priority design and stated
not to be revisited without operational evidence, is that **fairness
mechanisms must be bounded by observed necessity, not theoretical
generality.** The replan budget is reserved so that contradiction-driven
replans do not monopolize coverage-expansion opportunities — but that reserve
is fixed at one slot, does not decay, does not age, and is not extended to
broader fairness schemes until real agent-driven sessions demonstrate a
concrete need. DRAW does not preemptively hedge against problems it has not
yet observed. The same restraint applies here: BCNE and ANRB solve the
relation-classification problem as it manifests today, not as it might
evolve, and no further generalization of either gate is authorized until
operational evidence demonstrates a concrete need. This is a deliberate
scope freeze, not an oversight.

---

> It genuinely makes me happy to present to you DRAW V3.0 — Prototype.
> Maybe a little later than planned, but I'm calling this my birthday present
> to myself (September 29). Welcome, and go ahead and try the engine out.

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

V3 adds the external agent protocol layer at `/api/v2/agent/*`:

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/v2/agent/sessions` | Create session + deterministic metal-prompt briefing |
| `GET` | `/api/v2/agent/sessions/{id}/state` | Evidence state snapshot with exclusion reasons |
| `GET` | `/api/v2/agent/sessions/{id}/events` | SSE stream (agent, with replan budget + inactivity) |
| `POST` | `/api/v2/agent/sessions/{id}/replan` | Request external replan (8-layer protection gate) |
| `GET` | `/api/v2/agent/sessions/{id}/result` | Full agent result (evidence + exclusion reasons) |

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