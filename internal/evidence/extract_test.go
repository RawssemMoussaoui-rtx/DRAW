package evidence

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"draw/internal/model"
)

func mustParseURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

func testTask(status model.RetrievalStatus, data []byte, headers http.Header) (model.Task, model.TaskResult) {
	return model.Task{
			ID:           model.NewTaskID(),
			SessionID:    model.NewSessionID(),
			Type:         model.TaskTypeFetchHTTP,
			SourceTarget: "example.com",
			URL:          mustParseURL("https://example.com"),
			SourceClass:  model.SourceClassUnknown,
			CreatedAt:    time.Now().UTC(),
		}, model.TaskResult{
			Status:  status,
			Data:    data,
			Headers: headers,
		}
}

func TestExtract_HTMLTitle(t *testing.T) {
	tk, r := testTask(model.RetrievalStatusSuccess, []byte("<title>X</title>"), nil)
	items := Extract(tk, r, nil)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Claim != "page_title" || items[0].Value != "X" {
		t.Errorf("expected Claim=page_title Value=X, got Claim=%q Value=%q", items[0].Claim, items[0].Value)
	}
}

func TestExtract_HTMLMetaDescription(t *testing.T) {
	tk, r := testTask(model.RetrievalStatusSuccess,
		[]byte(`<meta name="description" content="d">`),
		http.Header{"Content-Type": []string{"text/html"}})
	items := Extract(tk, r, nil)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Claim != "page_description" || items[0].Value != "d" {
		t.Errorf("expected Claim=page_description Value=d, got Claim=%q Value=%q", items[0].Claim, items[0].Value)
	}
}

func TestExtract_JSONKeyValue(t *testing.T) {
	tk, r := testTask(model.RetrievalStatusSuccess,
		[]byte(`[{"revenue":1230000,"name":"Co"}]`), nil)
	items := Extract(tk, r, nil)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	claims := map[string]bool{}
	values := map[string]bool{}
	for _, it := range items {
		claims[it.Claim] = true
		values[it.Value] = true
	}
	if !claims["name"] || !claims["revenue"] {
		t.Errorf("expected claims name and revenue, got %v", claims)
	}
	if !values["Co"] || !values["1230000"] {
		t.Errorf("expected values Co and 1230000, got %v", values)
	}
}

func TestExtract_JSONDeterministic(t *testing.T) {
	tk, r := testTask(model.RetrievalStatusSuccess,
		[]byte(`[{"revenue":1230000,"name":"Co"}]`), nil)
	a := Extract(tk, r, nil)
	b := Extract(tk, r, nil)
	c := Extract(tk, r, nil)
	if len(a) == 0 {
		t.Fatal("expected items")
	}
	for i := range a {
		if a[i].Claim != b[i].Claim || a[i].Claim != c[i].Claim {
			t.Errorf("Claim not deterministic at %d: %q %q %q", i, a[i].Claim, b[i].Claim, c[i].Claim)
		}
		if a[i].Value != b[i].Value || a[i].Value != c[i].Value {
			t.Errorf("Value not deterministic at %d: %q %q %q", i, a[i].Value, b[i].Value, c[i].Value)
		}
		if a[i].Confidence != b[i].Confidence || a[i].Confidence != c[i].Confidence {
			t.Errorf("Confidence not deterministic at %d: %v %v %v", i, a[i].Confidence, b[i].Confidence, c[i].Confidence)
		}
	}
}

func TestExtract_JSONNested(t *testing.T) {
	tk, r := testTask(model.RetrievalStatusSuccess,
		[]byte(`[{"data":{"revenue":500}}]`), nil)
	items := Extract(tk, r, nil)
	if len(items) < 2 {
		t.Fatalf("expected >=2 items, got %d", len(items))
	}
	found := false
	for _, it := range items {
		if it.Claim == "data.revenue" && it.Value == "500" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected nested claim data.revenue Value=500, got %v", items)
	}
}

func TestExtract_RSSFeed(t *testing.T) {
	rss := `<?xml version="1.0"?><rss version="2.0"><channel><title>FeedTitle</title><item><title>One</title><link>l1</link></item><item><title>Two</title><link>l2</link></item></channel></rss>`
	tk, r := testTask(model.RetrievalStatusSuccess, []byte(rss), nil)
	items := Extract(tk, r, nil)
	if len(items) < 3 {
		t.Fatalf("expected >=3 items, got %d", len(items))
	}
	hasTitle := false
	for _, it := range items {
		if it.Claim == "feed_title" && it.Value == "FeedTitle" {
			hasTitle = true
		}
	}
	if !hasTitle {
		t.Errorf("expected feed_title=FeedTitle, got %v", items)
	}
}

func TestExtract_RSSItemLinks(t *testing.T) {
	rss := `<?xml version="1.0"?><rss version="2.0"><channel><title>T</title><item><title>One</title><link>https://one.example</link></item><item><title>Two</title><link>https://two.example</link></item></channel></rss>`
	tk, r := testTask(model.RetrievalStatusSuccess, []byte(rss), nil)
	items := Extract(tk, r, nil)
	got := map[string]string{}
	for _, it := range items {
		got[it.Claim] = it.Value
	}
	if got["item_0_link"] != "https://one.example" || got["item_1_link"] != "https://two.example" {
		t.Errorf("expected item_0_link and item_1_link, got %v", got)
	}
}

func TestExtract_EmptyDataReturnsNil(t *testing.T) {
	tk, r := testTask(model.RetrievalStatusSuccess, nil, nil)
	items := Extract(tk, r, nil)
	if items != nil {
		t.Errorf("expected nil for empty data, got %v", items)
	}
}

func TestExtract_NonSuccessStatusReturnsNil(t *testing.T) {
	statuses := []model.RetrievalStatus{
		model.RetrievalStatusEmpty,
		model.RetrievalStatusJavascriptRequired,
		model.RetrievalStatusAuthRequired,
		model.RetrievalStatusTimeout,
		model.RetrievalStatusBlocked,
		model.RetrievalStatusInvalidContent,
	}
	for _, st := range statuses {
		tk, r := testTask(st, []byte("<title>X</title>"), nil)
		items := Extract(tk, r, nil)
		if items != nil {
			t.Errorf("expected nil for status %s, got %v", st, items)
		}
	}
}

func TestExtract_SourceIDFromTask(t *testing.T) {
	tk, r := testTask(model.RetrievalStatusSuccess, []byte("<title>X</title>"), nil)
	items := Extract(tk, r, nil)
	if len(items) == 0 {
		t.Fatal("expected items")
	}
	if string(items[0].SourceID) != "example.com" {
		t.Errorf("expected SourceID=example.com, got %s", items[0].SourceID)
	}
}

func TestExtract_TopicFromInput(t *testing.T) {
	tk, r := testTask(model.RetrievalStatusSuccess, []byte("<title>X</title>"), nil)
	items := Extract(tk, r, []string{"revenue"})
	if len(items) == 0 {
		t.Fatal("expected items")
	}
	if items[0].Topic != "revenue" {
		t.Errorf("expected Topic=revenue, got %q", items[0].Topic)
	}
}

func TestExtract_ConfidenceRange(t *testing.T) {
	tk, r := testTask(model.RetrievalStatusSuccess,
		[]byte(`[{"revenue":1230000,"name":"Co"}]`), nil)
	items := Extract(tk, r, nil)
	if len(items) == 0 {
		t.Fatal("expected items")
	}
	for i, it := range items {
		if it.Confidence < 0 || it.Confidence > 1 {
			t.Errorf("item %d confidence %v out of [0,1]", i, it.Confidence)
		}
	}
}

func TestExtract_VerificationUnverified(t *testing.T) {
	tk, r := testTask(model.RetrievalStatusSuccess,
		[]byte(`[{"revenue":1230000,"name":"Co"}]`), nil)
	items := Extract(tk, r, nil)
	if len(items) == 0 {
		t.Fatal("expected items")
	}
	for i, it := range items {
		if it.Verification != model.VerificationUnverified {
			t.Errorf("item %d verification = %q, want %q", i, it.Verification, model.VerificationUnverified)
		}
	}
}

func TestExtract_HTMLSniffNoContentType(t *testing.T) {
	tk, r := testTask(model.RetrievalStatusSuccess,
		[]byte("<html><head><title>X</title></head><body></body></html>"), nil)
	items := Extract(tk, r, nil)
	if len(items) == 0 {
		t.Fatal("expected items from <html> sniff with no content-type")
	}
	found := false
	for _, it := range items {
		if it.Claim == "page_title" && it.Value == "X" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected page_title=X, got %v", items)
	}
}

func TestExtract_TextNoEvidence(t *testing.T) {
	tk, r := testTask(model.RetrievalStatusSuccess, []byte("hello"),
		http.Header{"Content-Type": []string{"text/plain"}})
	items := Extract(tk, r, nil)
	if items != nil {
		t.Errorf("expected nil for text/plain, got %v", items)
	}
}

func TestExtract_OriginURLSet(t *testing.T) {
	tk, r := testTask(model.RetrievalStatusSuccess, []byte("<title>X</title>"), nil)
	items := Extract(tk, r, nil)
	if len(items) == 0 {
		t.Fatal("expected items")
	}
	for i, it := range items {
		if it.OriginURL == nil {
			t.Fatalf("item %d: expected OriginURL set, got nil", i)
		}
		if *it.OriginURL != "https://example.com" {
			t.Errorf("item %d: expected OriginURL=https://example.com, got %q", i, *it.OriginURL)
		}
	}
}

func TestExtract_ExtractionSeqOrdering(t *testing.T) {
	tk, r := testTask(model.RetrievalStatusSuccess,
		[]byte(`[{"revenue":1230000,"name":"Co"}]`), nil)
	items := Extract(tk, r, nil)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	for i, it := range items {
		if it.ExtractionSeq == nil {
			t.Fatalf("item %d: expected ExtractionSeq set, got nil", i)
		}
		if *it.ExtractionSeq != i {
			t.Errorf("item %d: expected ExtractionSeq=%d, got %d", i, i, *it.ExtractionSeq)
		}
	}
}

func TestExtract_ExtractionSeqResetsPerCall(t *testing.T) {
	tk, r := testTask(model.RetrievalStatusSuccess,
		[]byte(`[{"a":1},{"b":2}]`), nil)
	items := Extract(tk, r, nil)
	if len(items) == 0 {
		t.Fatal("expected items")
	}
	if *items[0].ExtractionSeq != 0 {
		t.Errorf("first call item 0: expected ExtractionSeq=0, got %d", *items[0].ExtractionSeq)
	}
	if *items[len(items)-1].ExtractionSeq != len(items)-1 {
		t.Errorf("first call last item: expected ExtractionSeq=%d, got %d", len(items)-1, *items[len(items)-1].ExtractionSeq)
	}

	items2 := Extract(tk, r, nil)
	if len(items2) == 0 {
		t.Fatal("expected items on second call")
	}
	if *items2[0].ExtractionSeq != 0 {
		t.Errorf("second call item 0: expected ExtractionSeq=0, got %d", *items2[0].ExtractionSeq)
	}
}
