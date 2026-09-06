package storage

import (
	"context"
	"testing"

	"draw/internal/model"
)

func TestSQLiteSourceRegistry_SatisfiesInterface(t *testing.T) {
	var _ SourceRegistry = (*SQLiteSourceRegistry)(nil)
}

func newTestSourceRegistry(t *testing.T) *SQLiteSourceRegistry {
	t.Helper()
	db := newTestDB(t)
	if err := Apply(context.Background(), db); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	reg, err := NewSQLiteSourceRegistry(db, 0)
	if err != nil {
		t.Fatalf("NewSQLiteSourceRegistry: %v", err)
	}
	return reg
}

func TestSQLiteSourceRegistry_UnknownDomainBaseline03(t *testing.T) {
	reg := newTestSourceRegistry(t)
	p, ok := reg.Lookup("unknown.example")
	if !ok {
		t.Fatal("expected ok=true for unknown domain")
	}
	if p.QualityScore != 0.3 {
		t.Errorf("QualityScore: got %v, want 0.3", p.QualityScore)
	}
	if !p.Provisional {
		t.Errorf("Provisional: got %v, want true", p.Provisional)
	}
	if p.Class != model.SourceClassUnknown {
		t.Errorf("Class: got %v, want %v", p.Class, model.SourceClassUnknown)
	}
}

func TestSQLiteSourceRegistry_PersistentDeny(t *testing.T) {
	reg := newTestSourceRegistry(t)
	if err := reg.Deny("evil.example"); err != nil {
		t.Fatalf("Deny: %v", err)
	}
	p, ok := reg.Lookup("evil.example")
	if !ok || p == nil {
		t.Fatalf("expected denied profile observable (ok=true, non-nil) so callers can reject per H29/Cor.5, got ok=%v p=%v", ok, p)
	}
	if !p.Denied {
		t.Errorf("expected Denied=true for deny-listed domain, got %v", p.Denied)
	}
}

func TestSQLiteSourceRegistry_UpsertThenLookup(t *testing.T) {
	reg := newTestSourceRegistry(t)
	prof := model.SourceProfile{
		Domain:          "example.com",
		Class:           model.SourceClassNews,
		QualityScore:    0.8,
		CrawlDepthLimit: 3,
		PerDomainLimit:  5,
		RateLimitRPM:    60,
		AuthType:        model.AuthTypeNone,
	}
	if err := reg.Upsert(prof); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	p, ok := reg.Lookup("example.com")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if p.QualityScore != 0.8 {
		t.Errorf("QualityScore: got %v, want 0.8", p.QualityScore)
	}
	if p.Provisional {
		t.Errorf("Provisional: got %v, want false", p.Provisional)
	}
}

func TestSQLiteSourceRegistry_ProvisionalUpsert(t *testing.T) {
	reg := newTestSourceRegistry(t)
	prof := model.SourceProfile{
		Domain:          "news.example",
		Class:           model.SourceClassNews,
		QualityScore:    0.5,
		CrawlDepthLimit: 3,
		PerDomainLimit:  5,
		RateLimitRPM:    60,
		AuthType:        model.AuthTypeNone,
	}
	if err := reg.ProvisionalUpsert(prof); err != nil {
		t.Fatalf("ProvisionalUpsert: %v", err)
	}
	p, ok := reg.Lookup("news.example")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !p.Provisional {
		t.Errorf("Provisional: got %v, want true", p.Provisional)
	}
}

func TestSQLiteSourceRegistry_ProvisionalDoesNotDowngrade(t *testing.T) {
	reg := newTestSourceRegistry(t)
	prof := model.SourceProfile{
		Domain:          "known.example",
		Class:           model.SourceClassNews,
		QualityScore:    0.9,
		CrawlDepthLimit: 3,
		PerDomainLimit:  5,
		RateLimitRPM:    60,
		AuthType:        model.AuthTypeNone,
	}
	if err := reg.Upsert(prof); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	degrade := model.SourceProfile{
		Domain:          "known.example",
		Class:           model.SourceClassNews,
		QualityScore:    0.3,
		CrawlDepthLimit: 1,
		PerDomainLimit:  5,
		RateLimitRPM:    60,
		AuthType:        model.AuthTypeNone,
	}
	if err := reg.ProvisionalUpsert(degrade); err != nil {
		t.Fatalf("ProvisionalUpsert: %v", err)
	}
	p, ok := reg.Lookup("known.example")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if p.QualityScore != 0.9 {
		t.Errorf("QualityScore: got %v, want 0.9 (should not be downgraded)", p.QualityScore)
	}
	if p.CrawlDepthLimit != 3 {
		t.Errorf("CrawlDepthLimit: got %v, want 3 (should not be downgraded)", p.CrawlDepthLimit)
	}
}

func TestSQLiteSourceRegistry_DenyOverridesProvisional(t *testing.T) {
	reg := newTestSourceRegistry(t)
	prof := model.SourceProfile{
		Domain:          "deny.example",
		Class:           model.SourceClassNews,
		QualityScore:    0.8,
		CrawlDepthLimit: 3,
		PerDomainLimit:  5,
		RateLimitRPM:    60,
		AuthType:        model.AuthTypeNone,
	}
	if err := reg.Upsert(prof); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := reg.Deny("deny.example"); err != nil {
		t.Fatalf("Deny: %v", err)
	}
	prov := model.SourceProfile{
		Domain:          "deny.example",
		Class:           model.SourceClassSocial,
		QualityScore:    0.1,
		CrawlDepthLimit: 1,
		PerDomainLimit:  5,
		RateLimitRPM:    60,
		AuthType:        model.AuthTypeNone,
	}
	if err := reg.ProvisionalUpsert(prov); err != nil {
		t.Fatalf("ProvisionalUpsert: %v", err)
	}
	p, ok := reg.Lookup("deny.example")
	if !ok || p == nil {
		t.Fatalf("expected denied profile observable (ok=true, non-nil) after ProvisionalUpsert per H29/Cor.5, got ok=%v p=%v", ok, p)
	}
	if !p.Denied {
		t.Errorf("expected Denied=true for deny-listed domain after ProvisionalUpsert, got %v", p.Denied)
	}
}

func TestSQLiteSourceRegistry_CaseInsensitive(t *testing.T) {
	reg := newTestSourceRegistry(t)
	prof := model.SourceProfile{
		Domain:          "Example.COM",
		Class:           model.SourceClassNews,
		QualityScore:    0.7,
		CrawlDepthLimit: 3,
		PerDomainLimit:  5,
		RateLimitRPM:    60,
		AuthType:        model.AuthTypeNone,
	}
	if err := reg.Upsert(prof); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	p, ok := reg.Lookup("example.com")
	if !ok {
		t.Fatal("expected ok=true for case-insensitive lookup")
	}
	if p.Domain != "example.com" {
		t.Errorf("Domain: got %q, want %q", p.Domain, "example.com")
	}
}

func TestSQLiteSourceRegistry_Deterministic(t *testing.T) {
	reg := newTestSourceRegistry(t)
	p1, ok1 := reg.Lookup("deterministic.example")
	p2, ok2 := reg.Lookup("deterministic.example")
	p3, ok3 := reg.Lookup("deterministic.example")

	if !ok1 || !ok2 || !ok3 {
		t.Fatal("expected ok=true for all lookups")
	}
	if p1.QualityScore != p2.QualityScore || p2.QualityScore != p3.QualityScore {
		t.Errorf("QualityScore not deterministic: %v, %v, %v", p1.QualityScore, p2.QualityScore, p3.QualityScore)
	}
	if p1.Provisional != p2.Provisional || p2.Provisional != p3.Provisional {
		t.Errorf("Provisional not deterministic: %v, %v, %v", p1.Provisional, p2.Provisional, p3.Provisional)
	}
}
