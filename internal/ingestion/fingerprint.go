package ingestion

import (
	"crypto/sha256"
	"sync"
)

// Fingerprinter provides deterministic, in-memory deduplication of raw body
// bytes via SHA-256 fingerprints. It is safe for concurrent use.
type Fingerprinter struct {
	mu   sync.Mutex
	seen map[[32]byte]struct{}
}

// NewFingerprinter creates a Fingerprinter with an empty seen-set.
func NewFingerprinter() *Fingerprinter {
	return &Fingerprinter{
		seen: make(map[[32]byte]struct{}),
	}
}

// Seen reports whether data was already processed in this session; if not,
// marks it as seen and returns false. Identical bytes always produce identical
// SHA-256 digests and thus identical outcomes.
//
// Empty data is NOT special-cased: a zero-length input has a well-defined
// SHA-256 digest and is fingerprinted normally. Callers that wish to skip
// empty bodies should gate on len(data) > 0 before invoking Seen.
func (f *Fingerprinter) Seen(data []byte) bool {
	digest := sha256.Sum256(data)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seen == nil {
		f.seen = make(map[[32]byte]struct{})
	}
	if _, ok := f.seen[digest]; ok {
		return true
	}
	f.seen[digest] = struct{}{}
	return false
}

// Reset clears the seen-set so that previously-processed data is treated as
// unseen again (e.g. for a new session).
func (f *Fingerprinter) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = make(map[[32]byte]struct{})
}
