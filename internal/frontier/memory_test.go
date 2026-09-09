package frontier

import (
	"testing"
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
