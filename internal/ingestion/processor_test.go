package ingestion

import (
	"net/http"
	"net/url"
	"sync"
	"testing"

	"draw/internal/frontier"
	"draw/internal/model"
)

type recordingSink struct {
	mu     sync.Mutex
	pushes int
	last   []frontier.URLCandidate
}

func (r *recordingSink) Push(urls []frontier.URLCandidate) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pushes++
	r.last = urls
	return nil
}

func mustParseURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("parse url %q: %v", s, err)
	}
	return u
}

func TestProcessorBuildsCandidates(t *testing.T) {
	sink := &recordingSink{}
	p := NewProcessor(sink)

	body := []byte(`<!DOCTYPE html><html><body>
<a href="/a">a</a>
<a href="https://other.example/x">x</a>
</body></html>`)

	pageURL := mustParseURL(t, "https://page.example/")
	seedID := model.NewTaskID()
	sessID := model.NewSessionID()
	task := model.Task{
		ID:          seedID,
		SessionID:   sessID,
		SourceClass: model.SourceClassNews,
		URL:         pageURL,
		CrawlDepth:  1,
		TaskDepth:   1,
		Priority:    500,
	}
	res := model.TaskResult{
		Status:  model.RetrievalStatusSuccess,
		Data:    body,
		Headers: http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
	}

	if err := p.Process(t.Context(), task, res); err != nil {
		t.Fatalf("process: %v", err)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.pushes != 1 {
		t.Fatalf("Push calls: got %d want 1", sink.pushes)
	}
	cands := sink.last
	if len(cands) != 2 {
		t.Fatalf("candidates: got %d want 2", len(cands))
	}

	wantDomains := map[string]bool{"page.example": true, "other.example": true}
	gotDomains := map[string]bool{}
	discoveredIDs := map[model.URLID]struct{}{}
	for _, c := range cands {
		gotDomains[c.Domain] = true
		if c.CrawlDepth != 2 {
			t.Errorf("CrawlDepth: got %d want 2", c.CrawlDepth)
		}
		if c.TaskDepth != 2 {
			t.Errorf("TaskDepth: got %d want 2", c.TaskDepth)
		}
		if c.ParentTaskID == nil || *c.ParentTaskID != seedID {
			t.Errorf("ParentTaskID: got %v want %v", c.ParentTaskID, seedID)
		}
		if c.SessionID != sessID {
			t.Errorf("SessionID: got %v want %v", c.SessionID, sessID)
		}
		if c.SourceClass != model.SourceClassNews {
			t.Errorf("SourceClass: got %v want %v", c.SourceClass, model.SourceClassNews)
		}
		if c.PriorityHint != 500 {
			t.Errorf("PriorityHint: got %d want 500", c.PriorityHint)
		}
		if c.DiscoveredFromURL == nil {
			t.Fatal("DiscoveredFromURL is nil")
		}
		discoveredIDs[*c.DiscoveredFromURL] = struct{}{}
	}
	if len(discoveredIDs) != 1 {
		t.Errorf("expected all candidates to share one DiscoveredFromURL, got %d distinct", len(discoveredIDs))
	}
	for d := range wantDomains {
		if !gotDomains[d] {
			t.Errorf("missing domain %q", d)
		}
	}
}

func TestProcessorSkipsNonContentBearing(t *testing.T) {
	sink := &recordingSink{}
	p := NewProcessor(sink)

	pageURL := mustParseURL(t, "https://page.example/")
	task := model.Task{ID: model.NewTaskID(), SessionID: model.NewSessionID(), URL: pageURL}
	body := []byte(`<!DOCTYPE html><html><body><a href="https://other.example/x">x</a></body></html>`)

	for _, status := range []model.RetrievalStatus{
		model.RetrievalStatusTimeout,
		model.RetrievalStatusBlocked,
		model.RetrievalStatusEmpty,
		model.RetrievalStatusJavascriptRequired,
		model.RetrievalStatusAuthRequired,
		model.RetrievalStatusInvalidContent,
	} {
		res := model.TaskResult{Status: status, Data: body}
		if err := p.Process(t.Context(), task, res); err != nil {
			t.Errorf("status %s: unexpected error %v", status, err)
		}
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.pushes != 0 {
		t.Errorf("Push calls: got %d want 0", sink.pushes)
	}
}

func TestProcessorSkipsEmptyData(t *testing.T) {
	sink := &recordingSink{}
	p := NewProcessor(sink)

	pageURL := mustParseURL(t, "https://page.example/")
	task := model.Task{ID: model.NewTaskID(), SessionID: model.NewSessionID(), URL: pageURL}

	for _, data := range [][]byte{nil, {}, {}} {
		for _, status := range []model.RetrievalStatus{model.RetrievalStatusSuccess, model.RetrievalStatusPartial} {
			res := model.TaskResult{Status: status, Data: data}
			if err := p.Process(t.Context(), task, res); err != nil {
				t.Errorf("status %s: unexpected error %v", status, err)
			}
		}
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.pushes != 0 {
		t.Errorf("Push calls: got %d want 0", sink.pushes)
	}
}

func TestProcessorContentFingerprintDedup(t *testing.T) {
	sink := &recordingSink{}
	p := NewProcessor(sink)

	body := []byte(`<!DOCTYPE html><html><body><a href="https://other.example/x">x</a></body></html>`)
	pageURL := mustParseURL(t, "https://page.example/")
	task := model.Task{
		ID:         model.NewTaskID(),
		SessionID:  model.NewSessionID(),
		URL:        pageURL,
		CrawlDepth: 0,
		TaskDepth:  0,
	}
	res := model.TaskResult{
		Status:  model.RetrievalStatusSuccess,
		Data:    body,
		Headers: http.Header{"Content-Type": []string{"text/html"}},
	}

	if err := p.Process(t.Context(), task, res); err != nil {
		t.Fatal(err)
	}
	// Identical body reprocessed must be skipped by the fingerprinter.
	if err := p.Process(t.Context(), task, res); err != nil {
		t.Fatal(err)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.pushes != 1 {
		t.Errorf("Push calls: got %d want 1 (second Process deduped by fingerprint)", sink.pushes)
	}
}

func TestProcessorMaxURLsCap(t *testing.T) {
	sink := &recordingSink{}
	p := NewProcessor(sink, WithMaxURLsPerPage(2))

	body := []byte(`<!DOCTYPE html><html><body>
<a href="https://a.example/1">1</a>
<a href="https://a.example/2">2</a>
<a href="https://a.example/3">3</a>
<a href="https://a.example/4">4</a>
<a href="https://a.example/5">5</a>
</body></html>`)
	pageURL := mustParseURL(t, "https://page.example/")
	task := model.Task{
		ID:         model.NewTaskID(),
		SessionID:  model.NewSessionID(),
		URL:        pageURL,
		CrawlDepth: 0,
		TaskDepth:  0,
	}
	res := model.TaskResult{
		Status:  model.RetrievalStatusSuccess,
		Data:    body,
		Headers: http.Header{"Content-Type": []string{"text/html"}},
	}

	if err := p.Process(t.Context(), task, res); err != nil {
		t.Fatal(err)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.pushes != 1 {
		t.Fatalf("Push calls: got %d want 1", sink.pushes)
	}
	if len(sink.last) > 2 {
		t.Errorf("candidates: got %d want <= 2", len(sink.last))
	}
}
