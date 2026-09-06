package model

import "sync/atomic"

type IntentID string
type SessionID string
type TaskID string
type SourceID string
type EvidenceID string
type URLID string

type idKind string

const (
	kindIntent   idKind = "int"
	kindSession  idKind = "ses"
	kindTask     idKind = "tsk"
	kindURL      idKind = "url"
	kindSource   idKind = "src"
	kindEvidence idKind = "evd"
)

var idSeq uint64

func nextID(k idKind) string {
	n := atomic.AddUint64(&idSeq, 1)
	return string(k) + "_" + u64(n)
}

func u64(n uint64) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	return string(buf[i:])
}

func NewIntentID() IntentID    { return IntentID(nextID(kindIntent)) }
func NewSessionID() SessionID  { return SessionID(nextID(kindSession)) }
func NewTaskID() TaskID        { return TaskID(nextID(kindTask)) }
func NewURLID() URLID          { return URLID(nextID(kindURL)) }
func NewSourceID(d string) SourceID { return SourceID(d) }
// P7 — EvidenceStore.Put (upsert) coupling and latent risk.
// EvidenceStore.Put deliberately excludes session_id, task_id, and source_id
// columns from the column set that is updated when a row already exists at the
// given primary key `id` (see the corresponding INSERT ... ON CONFLICT(id)
// UPDATE SET ... statement in internal/storage/sqlite_evidence.go, which sets
// only payload columns and never overwrites identity columns).
//
// This is safe ONLY because model.NewEvidenceID (see below / alongside this
// comment) produces SEQUENTIAL, globally-unique identifiers (evd_1, evd_2, ...)
// that can never collide across sessions, tasks, or sources.
//
// RISK: If NewEvidenceID is ever changed to derive identifiers from content
// (e.g., a hash of payload + context), a collision between two distinct
// evidence objects coming from different sessions/tasks/sources would cause
// EvidenceStore.Put to silently merge them — preserving whichever identity
// was written last while discarding the other's session/task/source identity,
// because those columns are intentionally excluded from the ON CONFLICT
// update. Any future change to identifier generation MUST also revisit
// EvidenceStore.Put's column-exclusion logic before landing, and MUST be
// accompanied by an explicit owner decision, because it directly affects
// cross-session identity integrity (related to P6 behavior).
//
// DO NOT change generation logic here — this is a documentation-only
// preventive note.
func NewEvidenceID() EvidenceID { return EvidenceID(nextID(kindEvidence)) }
