package frontier

import (
	"testing"

	"draw/internal/model"
)

func TestSessionScopedDedup_SVE1(t *testing.T) {
	f := newTestFrontier(nil)
	u := "https://example.com/a?z=1&a=2"
	c1 := candidate("example.com", u, 500)
	c1.SessionID = "ses_1"
	c2 := candidate("example.com", u, 500)
	c2.SessionID = "ses_2"

	if err := f.Push([]URLCandidate{c1, c2}); err != nil {
		t.Fatal(err)
	}
	if f.Len() != 2 {
		t.Fatalf("same URL across sessions: got %d want 2 (both admitted)", f.Len())
	}

	if err := f.ResetSession("ses_1"); err != nil {
		t.Fatal(err)
	}
	if f.Len() != 1 {
		t.Fatalf("after ResetSession(ses_1): got %d want 1 (only ses_2 remains)", f.Len())
	}

	if !f.Has("example.com", u) {
		t.Error("Has should still find URL from ses_2 after resetting ses_1")
	}
}

func TestSessionScopedDedup_ResetMissingSession(t *testing.T) {
	f := newTestFrontier(nil)
	c := candidate("example.com", "https://example.com/x", 500)
	c.SessionID = "ses_1"

	if err := f.Push([]URLCandidate{c}); err != nil {
		t.Fatal(err)
	}
	if f.Len() != 1 {
		t.Fatalf("got %d want 1", f.Len())
	}

	if err := f.ResetSession("ses_999"); err != nil {
		t.Fatal(err)
	}
	if f.Len() != 1 {
		t.Fatalf("ResetSession on unknown session should be a no-op: got %d want 1", f.Len())
	}
}

// TestHas_SessionIsolation exercises the full Has() lifecycle across two
// sessions that share the same URL. Because byKey is keyed by the composite
// session-scoped key (sessionID:canonicalURL), both sessions retain their own
// entry; resetting one session must not evict the other, and Has (which scans
// all sessions) reflects the surviving entries.
func TestHas_SessionIsolation(t *testing.T) {
	f := newTestFrontier(nil)
	s1 := model.SessionID("ses_test_1")
	s2 := model.SessionID("ses_test_2")
	const domain = "example.com"
	const rawurl = "http://example.com/page"

	c1 := candidate(domain, rawurl, 500)
	c1.SessionID = s1
	c2 := candidate(domain, rawurl, 500)
	c2.SessionID = s2

	if err := f.Push([]URLCandidate{c1, c2}); err != nil {
		t.Fatal(err)
	}
	if f.Len() != 2 {
		t.Fatalf("same URL across sessions must both be admitted: got %d want 2", f.Len())
	}

	if !f.Has(domain, rawurl) {
		t.Error("Has should return true while either session holds the URL")
	}

	if err := f.ResetSession(s1); err != nil {
		t.Fatal(err)
	}
	if f.Len() != 1 {
		t.Fatalf("after ResetSession(s1): got %d want 1 (only s2 remains)", f.Len())
	}
	if !f.Has(domain, rawurl) {
		t.Error("Has should remain true after ResetSession(s1): s2's entry must stay intact")
	}

	if err := f.ResetSession(s2); err != nil {
		t.Fatal(err)
	}
	if f.Len() != 0 {
		t.Fatalf("after ResetSession(s2): got %d want 0", f.Len())
	}
	if f.Has(domain, rawurl) {
		t.Error("Has should return false after both sessions have been reset")
	}
}
