package ingestion

import (
	"net/url"
	"testing"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return u
}

func assertNoPanic(t *testing.T, f func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	f()
}

func TestResolveRelativeHref(t *testing.T) {
	page := mustURL(t, "https://example.com/page")
	got := Resolve(page, "", []ParsedLink{{Href: "/a"}})
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	want := "https://example.com/a"
	if got[0].String() != want {
		t.Fatalf("expected %q, got %q", want, got[0].String())
	}
	if got[0].Host != "example.com" {
		t.Fatalf("expected lowercased host without port, got %q", got[0].Host)
	}
	if got[0].Fragment != "" {
		t.Fatalf("expected no fragment, got %q", got[0].Fragment)
	}
}

func TestResolveBaseHrefOverridesPageURL(t *testing.T) {
	page := mustURL(t, "https://page.example.com/p")
	got := Resolve(page, "https://base.example.org/", []ParsedLink{{Href: "c"}})
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	want := "https://base.example.org/c"
	if got[0].String() != want {
		t.Fatalf("expected %q (resolved against base href), got %q", want, got[0].String())
	}
	if got[0].Host != "base.example.org" {
		t.Fatalf("expected resolution against base href host, got %q", got[0].Host)
	}
}

func TestResolveDefaultPortsStripped(t *testing.T) {
	page := mustURL(t, "https://example.com/page")
	got := Resolve(page, "", []ParsedLink{
		{Href: "http://example.com:80/a"},
		{Href: "https://example.com:443/b"},
	})
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got))
	}
	wants := []string{"http://example.com/a", "https://example.com/b"}
	for i, w := range wants {
		if got[i].String() != w {
			t.Fatalf("result %d: expected %q, got %q", i, w, got[i].String())
		}
		if got[i].Port() != "" {
			t.Fatalf("result %d: expected default port stripped, got host %q", i, got[i].Host)
		}
	}
}

func TestResolveDedupPreservesFirstOccurrence(t *testing.T) {
	page := mustURL(t, "https://example.com/p")
	got := Resolve(page, "", []ParsedLink{
		{Href: "/x"},
		{Href: "x"},
		{Href: "/y"},
	})
	if len(got) != 2 {
		t.Fatalf("expected 2 results after dedup, got %d", len(got))
	}
	wants := []string{"https://example.com/x", "https://example.com/y"}
	for i, w := range wants {
		if got[i].String() != w {
			t.Fatalf("result %d: expected %q, got %q", i, w, got[i].String())
		}
	}
}

func TestResolveNonHTTPSchemeDropped(t *testing.T) {
	page := mustURL(t, "https://example.com/page")
	got := Resolve(page, "", []ParsedLink{
		{Href: "ftp://example.com/a"},
		{Href: "mailto:b@example.com"},
		{Href: "https://good.example.com/x"},
	})
	if len(got) != 1 {
		t.Fatalf("expected 1 result (non-http dropped), got %d", len(got))
	}
	if got[0].String() != "https://good.example.com/x" {
		t.Fatalf("expected %q, got %q", "https://good.example.com/x", got[0].String())
	}
}

func TestResolveNilPageEmptyBaseNoPanic(t *testing.T) {
	var got []*url.URL
	assertNoPanic(t, func() {
		got = Resolve(nil, "", []ParsedLink{{Href: "/a"}, {Href: "b"}})
	})
	if len(got) != 0 {
		t.Fatalf("expected empty result, got %d", len(got))
	}
}

func TestResolveFragmentStripped(t *testing.T) {
	page := mustURL(t, "https://e.com/page")
	got := Resolve(page, "", []ParsedLink{{Href: "https://e.com/x#frag"}})
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if got[0].String() != "https://e.com/x" {
		t.Fatalf("expected fragment stripped %q, got %q", "https://e.com/x", got[0].String())
	}
	if got[0].Fragment != "" || got[0].RawFragment != "" {
		t.Fatalf("expected empty fragment, got Fragment=%q RawFragment=%q",
			got[0].Fragment, got[0].RawFragment)
	}
}
