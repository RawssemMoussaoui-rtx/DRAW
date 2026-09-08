package evidence

import (
	"encoding/json"
	"encoding/xml"
	"sort"
	"strconv"
	"strings"
	"time"

	"draw/internal/model"
	"golang.org/x/net/html"
)

// Extract inspects a TaskResult's payload and produces a list of Evidence
// items for JSON, HTML, RSS/Atom, and similar structured content. Text
// results and unsupported/non-structured content yield no evidence.
//
// The function is fully deterministic: for any given (Task, TaskResult,
// topics) triple the emitted evidence is stable, with JSON keys sorted and
// HTML elements collected in document order.
func Extract(t model.Task, r model.TaskResult, topics []string) []model.Evidence {
	if r.Status != model.RetrievalStatusSuccess && r.Status != model.RetrievalStatusPartial {
		return nil
	}
	if len(r.Data) == 0 {
		return nil
	}

	statusBase := 1.0
	if r.Status == model.RetrievalStatusPartial {
		statusBase = 0.5
	}

	qualityEst := 0.3
	if t.SourceTarget != "" {
		qualityEst = 1.0
	}
	confidence := statusBase * qualityEst

	sourceID := model.NewSourceID(t.SourceTarget)
	if t.SourceTarget == "" {
		if t.URL != nil {
			sourceID = model.NewSourceID(t.URL.Hostname())
		} else {
			sourceID = model.NewSourceID("")
		}
	}

	topic := "research"
	switch {
	case len(topics) > 0:
		topic = topics[0]
	case t.URL != nil:
		if h := t.URL.Hostname(); h != "" {
			topic = h
		}
	}

	raw := strings.TrimSpace(string(r.Data))
	ct := strings.ToLower(r.Headers.Get("Content-Type"))
	if !strings.Contains(ct, "json") && !strings.Contains(ct, "html") && !strings.Contains(ct, "xml") && !strings.Contains(ct, "text") && !strings.HasPrefix(raw, "<") {
		if len(raw) > 0 && raw[0] == '{' || len(raw) > 0 && raw[0] == '[' {
			ct = "application/json"
		}
	}

	var items []model.Evidence
	switch {
	case strings.Contains(ct, "json") || strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "["):
		items = extractJSON(raw, t, r, statusBase, qualityEst)
	case strings.Contains(ct, "html") || strings.HasPrefix(raw, "<html") || strings.HasPrefix(raw, "<body") || strings.HasPrefix(raw, "<title") || tagsHasPrefix(raw, "<a"):
		items = extractHTML(raw, t, r, statusBase, qualityEst)
	case strings.Contains(ct, "xml") || strings.Contains(raw, "<feed") || strings.Contains(raw, "<rss") || strings.Contains(raw, "<channel"):
		items = extractFeed(raw, t, r, statusBase, qualityEst)
	default:
		return nil
	}

	originURLStr := ""
	if t.URL != nil {
		originURLStr = t.URL.String()
	}

	base := buildEvidenceBase(t, r, topic, sourceID, confidence)
	seq := 0
	for i := range items {
		s := seq
		items[i].SessionID = base.SessionID
		items[i].TaskID = base.TaskID
		items[i].OriginURL = &originURLStr
		items[i].ExtractionSeq = &s
		items[i].SourceID = base.SourceID
		items[i].Topic = base.Topic
		items[i].Confidence = base.Confidence
		items[i].Verification = base.Verification
		items[i].CollectedAt = base.CollectedAt
		items[i].ID = ""
		seq++
	}
	return items
}

func buildEvidenceBase(t model.Task, r model.TaskResult, topic string, sourceID model.SourceID, confidence float64) model.Evidence {
	return model.Evidence{
		SessionID:    t.SessionID,
		TaskID:       t.ID,
		SourceID:     sourceID,
		Topic:        topic,
		Confidence:   confidence,
		Verification: model.VerificationUnverified,
		CollectedAt:  time.Now().UTC(),
	}
}

// tagsHasPrefix reports whether s begins (case-insensitively, after a single
// optional '<') with the literal tag such as `<a`, `<a>`, `<a `.
func tagsHasPrefix(s, tag string) bool {
	if !strings.HasPrefix(s, "<") {
		return false
	}
	s = s[1:]
	if !strings.HasPrefix(s, tag[1:]) {
		return false
	}
	s = s[len(tag)-1:]
	if len(s) == 0 {
		return true
	}
	c := s[0]
	return c == '>' || c == ' ' || c == '/'
}

func extractJSON(raw string, t model.Task, r model.TaskResult, statusBase, qualityEst float64) []model.Evidence {
	var nodes []jsonNode
	if err := json.Unmarshal([]byte(raw), &nodes); err != nil {
		return nil
	}
	if len(nodes) == 0 {
		return nil
	}

	paths := make([]string, 0, len(nodes))
	seen := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		walkJSON(n, "", 0, &paths, &seen)
	}
	sort.Strings(paths)

	base := buildEvidenceBase(t, r, "", model.NewSourceID(""), statusBase*qualityEst)
	result := make([]model.Evidence, 0, min(len(paths), 20))
	for i, p := range paths {
		if i >= 20 {
			break
		}
		result = append(result, model.Evidence{
			Claim: p,
			Value: jsonPathValue(nthNode(nodes, p)),
		})
	}
	for i := range result {
		result[i].SessionID = base.SessionID
		result[i].TaskID = base.TaskID
		result[i].Verification = base.Verification
		result[i].CollectedAt = base.CollectedAt
	}
	return result
}

func jsonPathValue(n jsonNode) string {
	switch n := n.(type) {
	case string:
		return n
	case bool:
		return strconv.FormatBool(n)
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	case nil:
		return ""
	default:
		b, _ := json.Marshal(n)
		return string(b)
	}
}

func nthNode(nodes []jsonNode, path string) jsonNode {
	for _, n := range nodes {
		var found jsonNode
		if walkJSONFind(n, "", 0, path, &found) {
			return found
		}
	}
	return nil
}

func walkJSONFind(n jsonNode, prefix string, depth int, target string, found *jsonNode) bool {
	cur := prefix
	if cur == "" {
		cur = target
	} else {
		cur = prefix
	}
	if depth <= 2 {
		switch v := n.(type) {
		case map[string]any:
			for k, child := range v {
				if k == "" {
					continue
				}
				p := joinPath(prefix, k)
				if p == target {
					*found = child
					return true
				}
				if depth < 2 {
					if walkJSONFind(child, p, depth+1, target, found) {
						return true
					}
				}
			}
		case []any:
			for i, child := range v {
				p := joinPath(prefix, "["+strconv.Itoa(i)+"]")
				if p == target {
					*found = child
					return true
				}
				if depth < 2 {
					if walkJSONFind(child, p, depth+1, target, found) {
						return true
					}
				}
			}
		}
	}
	return false
}

type jsonNode any

func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func walkJSON(n jsonNode, prefix string, depth int, paths *[]string, seen *map[string]bool) {
	if depth > 2 {
		return
	}
	switch v := n.(type) {
	case map[string]any:
		for _, k := range sortedKeys(v) {
			child := v[k]
			p := joinPath(prefix, k)
			if !(*seen)[p] {
				(*seen)[p] = true
				*paths = append(*paths, p)
			}
			if depth < 2 {
				walkJSON(child, p, depth+1, paths, seen)
			}
		}
	case []any:
		for i, child := range v {
			p := joinPath(prefix, "["+strconv.Itoa(i)+"]")
			if !(*seen)[p] {
				(*seen)[p] = true
				*paths = append(*paths, p)
			}
			if depth < 2 {
				walkJSON(child, p, depth+1, paths, seen)
			}
		}
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func extractHTML(raw string, t model.Task, r model.TaskResult, statusBase, qualityEst float64) []model.Evidence {
	doc, err := html.Parse(strings.NewReader(raw))
	if err != nil {
		return nil
	}

	base := buildEvidenceBase(t, r, "", model.NewSourceID(""), statusBase*qualityEst)
	var result []model.Evidence
	want := map[string]bool{
		"page_title":       false,
		"page_description": false,
		"og_title":         false,
		"og_description":   false,
	}
	seen := map[string]bool{}

	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n == nil {
			return
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "title":
				if !want["page_title"] {
					text := textOf(n)
					if text != "" {
						result = append(result, model.Evidence{Claim: "page_title", Value: text})
						want["page_title"] = true
						seen["page_title"] = true
					}
				}
			case "meta":
				name := attr(n, "name")
				prop := attr(n, "property")
				switch {
				case strings.EqualFold(name, "description") && !seen["page_description"]:
					if v := attr(n, "content"); v != "" {
						result = append(result, model.Evidence{Claim: "page_description", Value: v})
						seen["page_description"] = true
					}
				case strings.EqualFold(prop, "og:title") && !seen["og_title"]:
					if v := attr(n, "content"); v != "" {
						result = append(result, model.Evidence{Claim: "og_title", Value: v})
						seen["og_title"] = true
					}
				case strings.EqualFold(prop, "og:description") && !seen["og_description"]:
					if v := attr(n, "content"); v != "" {
						result = append(result, model.Evidence{Claim: "og_description", Value: v})
						seen["og_description"] = true
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	for i := range result {
		result[i].SessionID = base.SessionID
		result[i].TaskID = base.TaskID
		result[i].Verification = base.Verification
		result[i].CollectedAt = base.CollectedAt
	}
	return result
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func textOf(n *html.Node) string {
	if n == nil {
		return ""
	}
	if n.Type == html.TextNode {
		return n.Data
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
	}
	return strings.TrimSpace(b.String())
}

func extractFeed(raw string, t model.Task, r model.TaskResult, statusBase, qualityEst float64) []model.Evidence {
	var feed struct {
		Title       string     `xml:"title"`
		Description string     `xml:"description"`
		Link        feedLink   `xml:"link"`
		Items       []feedItem `xml:"item"`
		Entries     []feedItem `xml:"entry"`
		Channel     struct {
			Title       string     `xml:"title"`
			Description string     `xml:"description"`
			Link        feedLink   `xml:"link"`
			Items       []feedItem `xml:"item"`
			Entries     []feedItem `xml:"entry"`
		} `xml:"channel"`
	}
	if err := xml.NewDecoder(strings.NewReader(raw)).Decode(&feed); err != nil {
		return nil
	}

	base := buildEvidenceBase(t, r, "", model.NewSourceID(""), statusBase*qualityEst)

	var title, description, link string
	var items []feedItem
	if feed.Channel.Title != "" {
		title = feed.Channel.Title
		description = feed.Channel.Description
		link = feed.Channel.Link.Href
		items = append(items, feed.Channel.Items...)
		items = append(items, feed.Channel.Entries...)
	} else {
		title = feed.Title
		description = feed.Description
		link = feed.Link.Href
	}
	items = append(items, feed.Items...)
	items = append(items, feed.Entries...)

	var result []model.Evidence
	if title != "" {
		result = append(result, model.Evidence{Claim: "feed_title", Value: title})
	}
	if description != "" {
		result = append(result, model.Evidence{Claim: "feed_description", Value: description})
	}
	if link != "" {
		result = append(result, model.Evidence{Claim: "feed_link", Value: link})
	}
	for i, item := range items {
		if i >= 10 {
			break
		}
		if item.Title != "" {
			result = append(result, model.Evidence{Claim: "item_" + strconv.Itoa(i) + "_title", Value: item.Title})
		}
		if item.Link != "" {
			result = append(result, model.Evidence{Claim: "item_" + strconv.Itoa(i) + "_link", Value: item.Link})
		}
	}

	for i := range result {
		result[i].SessionID = base.SessionID
		result[i].TaskID = base.TaskID
		result[i].Verification = base.Verification
		result[i].CollectedAt = base.CollectedAt
	}
	return result
}

type feedItem struct {
	Title   string `xml:"title"`
	Link    string `xml:"link"`
	GUID    string `xml:"guid"`
	PubDate string `xml:"pubDate"`
}

type feedLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}
