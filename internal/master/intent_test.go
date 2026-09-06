package master

import (
	"testing"
	"time"

	"draw/internal/model"
)

func defaultParser() IntentParser {
	return NewIntentParser(DefaultIntentConfig())
}

func TestParseEmptyRequestFails(t *testing.T) {
	p := defaultParser()
	if _, err := p.Parse(model.IntentRequest{}); err == nil {
		t.Fatal("expected error for empty request")
	}
}

func TestParseSeedsOnlyYieldsResearchIntent(t *testing.T) {
	p := defaultParser()
	intent, err := p.Parse(model.IntentRequest{Seeds: []string{"https://example.com/path"}})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if intent.Type != model.IntentTypeResearch {
		t.Errorf("type: got %s want %s", intent.Type, model.IntentTypeResearch)
	}
	if intent.OutputType != model.OutputTypeReport {
		t.Errorf("output: got %s want %s", intent.OutputType, model.OutputTypeReport)
	}
	if intent.Entity != "example.com" {
		t.Errorf("entity from seed domain: got %q want %q", intent.Entity, "example.com")
	}
	if len(intent.Seeds) != 1 {
		t.Errorf("seeds: got %d want 1", len(intent.Seeds))
	}
	assertDefaults(t, intent)
}

func TestParseVerticalSliceExample(t *testing.T) {
	p := defaultParser()
	intent, err := p.Parse(model.IntentRequest{
		UserID: "u1",
		Query:  "research Company X, last 7 days, compare official vs news",
		Seeds:  []string{"https://example.com"},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if intent.Entity != "Company X" {
		t.Errorf("entity: got %q want %q", intent.Entity, "Company X")
	}
	if len(intent.SourceClasses) != 2 {
		t.Fatalf("source classes: got %d want 2", len(intent.SourceClasses))
	}
	classes := map[model.SourceClass]bool{}
	for _, c := range intent.SourceClasses {
		classes[c] = true
	}
	if !classes[model.SourceClassOfficial] || !classes[model.SourceClassNews] {
		t.Errorf("source classes wrong: %v", intent.SourceClasses)
	}
	if intent.TimeRange == nil {
		t.Fatal("expected non-nil time range")
	}
	dur := intent.TimeRange.To.Sub(intent.TimeRange.From)
	if dur != 7*24*time.Hour {
		t.Errorf("time range duration: got %s want %s", dur, 7*24*time.Hour)
	}
	hasCompare := false
	for _, op := range intent.Operations {
		if op == model.OperationCompare {
			hasCompare = true
		}
	}
	if !hasCompare {
		t.Errorf("expected COMPARE operation in %v", intent.Operations)
	}
	if len(intent.Seeds) != 1 {
		t.Errorf("seeds: got %d want 1", len(intent.Seeds))
	}
	assertDefaults(t, intent)
}

func TestParseDeterminismAcrossRuns(t *testing.T) {
	req := model.IntentRequest{
		Query: "research Company X, last 7 days, compare official vs news",
		Seeds: []string{"https://example.com"},
	}
	p := defaultParser()
	i0, _ := p.Parse(req)
	for i := 1; i < 5; i++ {
		pi, err := p.Parse(req)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if pi.Entity != i0.Entity ||
			len(pi.SourceClasses) != len(i0.SourceClasses) ||
			pi.OutputType != i0.OutputType ||
			pi.CrawlDepth != i0.CrawlDepth ||
			pi.EffortBudget != i0.EffortBudget {
			t.Fatalf("run %d not deterministic", i)
		}
		if pi.TimeRange == nil || i0.TimeRange == nil {
			t.Fatalf("time range nil")
		}
		if pi.TimeRange.To.Sub(pi.TimeRange.From) != i0.TimeRange.To.Sub(i0.TimeRange.From) {
			t.Fatalf("time range duration not deterministic")
		}
	}
}

func TestParseSourceClassesOrderPreserved(t *testing.T) {
	p := defaultParser()
	intent, err := p.Parse(model.IntentRequest{Query: "research Company X, social vs official"})
	if err != nil {
		t.Fatal(err)
	}
	if len(intent.SourceClasses) != 2 {
		t.Fatalf("got %d classes", len(intent.SourceClasses))
	}
	if intent.SourceClasses[0] != model.SourceClassSocial ||
		intent.SourceClasses[1] != model.SourceClassOfficial {
		t.Errorf("order not preserved: %v", intent.SourceClasses)
	}
}

func TestParseTimeWindowSize(t *testing.T) {
	p := defaultParser()
	for _, c := range []struct {
		frag string
		want time.Duration
	}{
		{"last 30 days", 30 * 24 * time.Hour},
		{"past 6 hours", 6 * time.Hour},
		{"in 2 weeks", 2 * 7 * 24 * time.Hour},
	} {
		intent, err := p.Parse(model.IntentRequest{Query: "research Company X, " + c.frag})
		if err != nil {
			t.Fatal(err)
		}
		if intent.TimeRange == nil {
			t.Fatalf("expected time range for %q", c.frag)
		}
		got := intent.TimeRange.To.Sub(intent.TimeRange.From)
		if got != c.want {
			t.Errorf("%q duration: got %s want %s", c.frag, got, c.want)
		}
	}
}

func TestParseNoKeywordsDefaults(t *testing.T) {
	p := defaultParser()
	intent, err := p.Parse(model.IntentRequest{Query: "research Company X"})
	if err != nil {
		t.Fatal(err)
	}
	if intent.Entity != "Company X" {
		t.Errorf("entity: got %q", intent.Entity)
	}
	if intent.TimeRange != nil {
		t.Errorf("expected nil time range, got %+v", intent.TimeRange)
	}
	if len(intent.SourceClasses) != 0 {
		t.Errorf("expected no source classes, got %v", intent.SourceClasses)
	}
	for _, op := range intent.Operations {
		if op == model.OperationCompare {
			t.Error("unexpected COMPARE op when no compare keyword")
		}
	}
	assertDefaults(t, intent)
}

func assertDefaults(t *testing.T, i model.Intent) {
	t.Helper()
	def := DefaultIntentConfig()
	if i.CrawlDepth != def.DefaultCrawlDepth {
		t.Errorf("CrawlDepth: got %d want %d", i.CrawlDepth, def.DefaultCrawlDepth)
	}
	if i.TaskDepth != def.DefaultTaskDepth {
		t.Errorf("TaskDepth: got %d want %d", i.TaskDepth, def.DefaultTaskDepth)
	}
	if i.EffortBudget != def.DefaultEffortBudget {
		t.Errorf("EffortBudget: got %d want %d", i.EffortBudget, def.DefaultEffortBudget)
	}
	if i.MaxReplans != def.DefaultMaxReplans {
		t.Errorf("MaxReplans: got %d want %d", i.MaxReplans, def.DefaultMaxReplans)
	}
	if i.ID == "" {
		t.Error("IntentID must be set")
	}
}
