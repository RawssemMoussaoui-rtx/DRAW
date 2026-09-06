package frontier

import (
	"net/url"
	"testing"

	"draw/internal/config"
	"draw/internal/model"
	"draw/internal/storage"
)

type fakeRegistry struct {
	profiles map[string]model.SourceProfile
}

func (f fakeRegistry) Lookup(domain string) (*model.SourceProfile, bool) {
	p, ok := f.profiles[domain]
	return &p, ok
}
func (f fakeRegistry) Upsert(model.SourceProfile) error            { return nil }
func (f fakeRegistry) Deny(string) error                           { return nil }
func (f fakeRegistry) ProvisionalUpsert(model.SourceProfile) error { return nil }

var _ storage.SourceRegistry = fakeRegistry{}

func mustParse(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return u
}

func newTestFrontier(src storage.SourceRegistry) *MemoryFrontier {
	cfg := config.Defaults()
	if src == nil {
		src = fakeRegistry{}
	}
	return NewMemoryFrontier(cfg, src)
}

func candidate(domain, rawurl string, prio int) URLCandidate {
	u, err := url.Parse(rawurl)
	if err != nil {
		panic(err)
	}
	return URLCandidate{
		URL:          u,
		Domain:       domain,
		PriorityHint: prio,
		SessionID:    "ses_test",
	}
}

func TestFrontierDedupIdenticalURL(t *testing.T) {
	f := newTestFrontier(nil)
	u := "https://example.com/a?z=1&a=2"
	c := candidate("example.com", u, 500)
	if err := f.Push([]URLCandidate{c, c}); err != nil {
		t.Fatal(err)
	}
	if f.Len() != 1 {
		t.Fatalf("dedup: got %d want 1", f.Len())
	}
}

func TestFrontierDedupCanonicalizes(t *testing.T) {
	f := newTestFrontier(nil)
	a := candidate("example.com", "https://example.com/a?a=1&b=2", 500)
	b := candidate("example.com", "https://EXAMPLE.com/a?b=2&a=1#frag", 500)
	if err := f.Push([]URLCandidate{a, b}); err != nil {
		t.Fatal(err)
	}
	if f.Len() != 1 {
		t.Fatalf("canonical dedup: got %d want 1", f.Len())
	}
}

func TestFrontierHardFilters(t *testing.T) {
	reg := fakeRegistry{profiles: map[string]model.SourceProfile{
		"denied.com": {Domain: "denied.com", Denied: true, QualityScore: 0.5, PerDomainLimit: 3, CrawlDepthLimit: 5},
	}}
	f := newTestFrontier(reg)
	cases := []struct {
		name string
		c    URLCandidate
	}{
		{"ftp scheme", candidate("example.com", "ftp://example.com/x", 500)},
		{"private ip", candidate("10.0.0.1", "http://10.0.0.1/x", 500)},
		{"loopback ip", candidate("127.0.0.1", "http://127.0.0.1/x", 500)},
		{"denied domain", candidate("denied.com", "https://denied.com/x", 500)},
		{"over depth", URLCandidate{URL: mustParse(t, "https://deep.com/x"), Domain: "deep.com", CrawlDepth: 99}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := f.Push([]URLCandidate{tc.c}); err != nil {
				t.Fatal(err)
			}
			if f.Len() != 0 {
				t.Errorf("expected %s to be hard-filtered, got frontier len %d", tc.name, f.Len())
			}
		})
	}
}

func TestFrontierUnknownDomainAllowedBaseline03(t *testing.T) {
	f := newTestFrontier(nil)
	c := candidate("unknown.example", "https://unknown.example/x", 500)
	if err := f.Push([]URLCandidate{c}); err != nil {
		t.Fatal(err)
	}
	if f.Len() != 1 {
		t.Fatalf("unknown domain should be allowed, got len %d", f.Len())
	}
	if got := f.Score(c); got <= 0 {
		t.Errorf("unknown domain quality baseline 0.3 should yield positive score, got %f", got)
	}
}

func TestFrontierNextPerDomainFairness(t *testing.T) {
	f := newTestFrontier(nil)
	var cs []URLCandidate
	for i := 0; i < 6; i++ {
		cs = append(cs, candidate("a.example", "https://a.example/x"+itoa(i), 900))
		cs = append(cs, candidate("b.example", "https://b.example/x"+itoa(i), 900))
	}
	if err := f.Push(cs); err != nil {
		t.Fatal(err)
	}
	got := f.Next(100)
	counts := map[string]int{}
	for _, c := range got {
		counts[c.Domain]++
	}
	if counts["a.example"] > frontierBatchPerDomain || counts["b.example"] > frontierBatchPerDomain {
		t.Errorf("per-domain fairness cap=%d violated: %v", frontierBatchPerDomain, counts)
	}
}

func TestFrontierScoreHigherPriorityHigherScore(t *testing.T) {
	f := newTestFrontier(fakeRegistry{profiles: map[string]model.SourceProfile{
		"hi.example": {Domain: "hi.example", QualityScore: 0.9},
		"lo.example": {Domain: "lo.example", QualityScore: 0.3},
	}})
	hi := URLCandidate{URL: mustParse(t, "https://hi.example/a"), Domain: "hi.example", PriorityHint: 1000}
	lo := URLCandidate{URL: mustParse(t, "https://lo.example/a"), Domain: "lo.example", PriorityHint: 100}
	if f.Score(hi) <= f.Score(lo) {
		t.Errorf("higher priority + higher quality should score higher: hi=%f lo=%f", f.Score(hi), f.Score(lo))
	}
}

func TestFrontierDeterminismAcrossRuns(t *testing.T) {
	cs := []URLCandidate{
		candidate("a.example", "https://a.example/1", 900),
		candidate("b.example", "https://b.example/1", 800),
		candidate("a.example", "https://a.example/2", 700),
		candidate("c.example", "https://c.example/1", 950),
	}
	var runs [][]URLCandidate
	for i := 0; i < 3; i++ {
		f := newTestFrontier(nil)
		if err := f.Push(cs); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, f.Next(100))
	}
	for i := 1; i < len(runs); i++ {
		if len(runs[i]) != len(runs[0]) {
			t.Fatalf("run %d length differs", i)
		}
		for j := range runs[0] {
			if runs[i][j].URL.String() != runs[0][j].URL.String() {
				t.Errorf("run %d order differs at %d: %q vs %q", i, j, runs[i][j].URL, runs[0][j].URL)
			}
		}
	}
}

func TestFrontierHasAfterPush(t *testing.T) {
	f := newTestFrontier(nil)
	c := candidate("a.example", "https://a.example/x?a=1&b=2", 500)
	if err := f.Push([]URLCandidate{c}); err != nil {
		t.Fatal(err)
	}
	if !f.Has("a.example", "https://a.example/x?a=1&b=2") {
		t.Error("Has should return true for pushed url")
	}
	if !f.Has("a.example", "https://a.example/x?b=2&a=1#frag") {
		t.Error("Has should match canonical form (reordered params, dropped fragment)")
	}
	if f.Has("a.example", "https://a.example/x") {
		t.Error("Has should return false for same path without the query params")
	}
	if f.Has("a.example", "https://other.example/x?a=1&b=2") {
		t.Error("Has should return false for absent url")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
