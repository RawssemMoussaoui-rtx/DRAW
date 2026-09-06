package workers

import (
	"net/url"
	"strings"
	"testing"

	"draw/internal/model"
)

// fakeSourceRegistry is a minimal storage.SourceRegistry for testing the
// fetch-boundary denylist without a real database.
type fakeSourceRegistry struct {
	profiles map[string]*model.SourceProfile
}

func (f *fakeSourceRegistry) Lookup(domain string) (*model.SourceProfile, bool) {
	if f == nil || f.profiles == nil {
		return nil, false
	}
	p, ok := f.profiles[strings.ToLower(domain)]
	return p, ok
}

func (f *fakeSourceRegistry) Upsert(model.SourceProfile) error            { return nil }
func (f *fakeSourceRegistry) Deny(string) error                           { return nil }
func (f *fakeSourceRegistry) ProvisionalUpsert(model.SourceProfile) error { return nil }

func newDeniedSourceRegistry(domain string) *fakeSourceRegistry {
	d := strings.ToLower(domain)
	return &fakeSourceRegistry{profiles: map[string]*model.SourceProfile{
		d: {Domain: d, Denied: true},
	}}
}

func newAllowedSourceRegistry(domain string) *fakeSourceRegistry {
	d := strings.ToLower(domain)
	return &fakeSourceRegistry{profiles: map[string]*model.SourceProfile{
		d: {Domain: d, Denied: false},
	}}
}

func TestShouldDeny_NilSafe(t *testing.T) {
	if ShouldDeny(nil, nil) {
		t.Fatal("expected false for nil url and nil registry")
	}
	u, _ := url.Parse("https://example.com")
	if ShouldDeny(u, nil) {
		t.Fatal("expected false for nil registry")
	}
	if ShouldDeny(nil, newDeniedSourceRegistry("example.com")) {
		t.Fatal("expected false for nil url")
	}
}

func TestShouldDeny_DeniedDomain(t *testing.T) {
	reg := newDeniedSourceRegistry("denied.example")
	u, _ := url.Parse("https://denied.example/path")
	if !ShouldDeny(u, reg) {
		t.Fatal("expected denied.example to be denied")
	}
}

func TestShouldDeny_AllowedDomain(t *testing.T) {
	reg := newAllowedSourceRegistry("ok.example")
	u, _ := url.Parse("https://ok.example/path")
	if ShouldDeny(u, reg) {
		t.Fatal("expected ok.example to be allowed")
	}
}

func TestShouldDeny_CaseInsensitive(t *testing.T) {
	reg := newDeniedSourceRegistry("denied.example")
	u, _ := url.Parse("https://DENIED.EXAMPLE/path")
	if !ShouldDeny(u, reg) {
		t.Fatal("expected case-insensitive deny")
	}
}

func TestShouldDeny_UnknownDomain(t *testing.T) {
	reg := &fakeSourceRegistry{profiles: map[string]*model.SourceProfile{}}
	u, _ := url.Parse("https://unknown.example/path")
	if ShouldDeny(u, reg) {
		t.Fatal("expected unknown domain to be allowed")
	}
}

func TestShouldDeny_EmptyHostname(t *testing.T) {
	reg := newDeniedSourceRegistry("denied.example")
	u, _ := url.Parse("https:///path")
	if ShouldDeny(u, reg) {
		t.Fatal("expected empty hostname to be allowed")
	}
}
