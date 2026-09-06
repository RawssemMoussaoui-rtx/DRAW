package ingestion

import (
	"reflect"
	"testing"
)

// hrefs returns just the Href values from a slice of ParsedLink, for easier
// comparison in tests.
func hrefs(links []ParsedLink) []string {
	out := make([]string, 0, len(links))
	for _, l := range links {
		out = append(out, l.Href)
	}
	return out
}

func TestParseHTMLAllAttrTypesDocumentOrder(t *testing.T) {
	htmlDoc := `<html><head>
<base href="https://base.example/">
<link href="/style.css">
<script src="/js/app.js"></script>
</head><body>
<a href="/page1">p1</a>
<iframe src="/frame1"></iframe>
<frame src="/frame2">
<source src="/media/a.mp4">
<img src="/img.png" srcset="a.jpg 1x, b.jpg 2x">
<a href="/page2">p2</a>
</body></html>`

	base, links, err := ExtractURLs("text/html; charset=utf-8", []byte(htmlDoc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if base != "https://base.example/" {
		t.Errorf("baseHref = %q, want %q", base, "https://base.example/")
	}
	want := []string{
		"/style.css",   // <link href>
		"/js/app.js",   // <script src>
		"/page1",       // <a href>
		"/frame1",      // <iframe src>
		"/frame2",      // <frame src>
		"/media/a.mp4", // <source src>
		"a.jpg",        // srcset part 1
		"b.jpg",        // srcset part 2
		"/page2",       // <a href>
	}
	got := hrefs(links)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("document order mismatch:\n got: %v\nwant: %v", got, want)
	}
}

func TestParseImgSrcExcludedButSrcsetIncluded(t *testing.T) {
	htmlDoc := `<img src="/img.png" srcset="/thumb/1.jpg 100w, /thumb/2.jpg 200w">`
	base, links, err := ExtractURLs("text/html", []byte(htmlDoc))
	if err != nil {
		t.Fatal(err)
	}
	if base != "" {
		t.Errorf("baseHref = %q, want empty", base)
	}
	got := hrefs(links)
	want := []string{"/thumb/1.jpg", "/thumb/2.jpg"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("img src must be excluded, srcset kept:\n got: %v\nwant: %v", got, want)
	}
}

func TestParseBaseHrefDiscovery(t *testing.T) {
	t.Run("absolute_base_returned_raw", func(t *testing.T) {
		htmlDoc := `<html><head><base href="https://example.com/root/"></head><body><a href="x"></a></body></html>`
		base, _, err := ExtractURLs("text/html", []byte(htmlDoc))
		if err != nil {
			t.Fatal(err)
		}
		if base != "https://example.com/root/" {
			t.Errorf("baseHref = %q, want raw value", base)
		}
	})
	t.Run("relative_base_returned_raw", func(t *testing.T) {
		htmlDoc := `<html><head><base href="/root/"></head><body></body></html>`
		base, _, err := ExtractURLs("text/html", []byte(htmlDoc))
		if err != nil {
			t.Fatal(err)
		}
		if base != "/root/" {
			t.Errorf("relative base must be returned raw, got %q", base)
		}
	})
	t.Run("no_base_returns_empty", func(t *testing.T) {
		htmlDoc := `<html><body><a href="/x"></a></body></html>`
		base, _, err := ExtractURLs("text/html", []byte(htmlDoc))
		if err != nil {
			t.Fatal(err)
		}
		if base != "" {
			t.Errorf("baseHref = %q, want empty", base)
		}
	})
}

func TestParseRelativeHrefsPassedThroughUnresolved(t *testing.T) {
	htmlDoc := `<html><body><a href="/path">a</a><a href="rel/page">b</a><a href="../up">c</a></body></html>`
	_, links, err := ExtractURLs("text/html", []byte(htmlDoc))
	if err != nil {
		t.Fatal(err)
	}
	got := hrefs(links)
	want := []string{"/path", "rel/page", "../up"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("relative hrefs must pass through unresolved:\n got: %v\nwant: %v", got, want)
	}
}

func TestParseContentTypeDispatch(t *testing.T) {
	t.Run("text_html_routes_to_html", func(t *testing.T) {
		base, links, err := ExtractURLs("text/html", []byte(`<html><body><a href="https://x.com/p">x</a></body></html>`))
		if err != nil || base != "" || len(links) != 1 || links[0].Href != "https://x.com/p" {
			t.Errorf("text/html dispatch failed: base=%q links=%v err=%v", base, links, err)
		}
	})
	t.Run("rss_xml_routes_to_xml", func(t *testing.T) {
		xmlDoc := `<?xml version="1.0"?><rss version="2.0"><channel><link>http://x.com/feed</link><item><link>http://x.com/a</link></item><item><link>http://x.com/b</link></item></channel></rss>`
		base, links, err := ExtractURLs("application/rss+xml", []byte(xmlDoc))
		if err != nil {
			t.Fatal(err)
		}
		if base != "" {
			t.Errorf("rss baseHref = %q, want empty", base)
		}
		got := hrefs(links)
		want := []string{"http://x.com/feed", "http://x.com/a", "http://x.com/b"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("rss dispatch:\n got: %v\nwant: %v", got, want)
		}
	})
	t.Run("atom_xml_routes_to_xml", func(t *testing.T) {
		xmlDoc := `<feed xmlns="http://www.w3.org/2005/Atom"><link href="http://x.com/atom" rel="self"/><entry><link href="http://x.com/1"/></entry></feed>`
		base, links, err := ExtractURLs("application/atom+xml", []byte(xmlDoc))
		if err != nil {
			t.Fatal(err)
		}
		if base != "" {
			t.Errorf("atom baseHref = %q, want empty", base)
		}
		got := hrefs(links)
		want := []string{"http://x.com/atom", "http://x.com/1"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("atom dispatch:\n got: %v\nwant: %v", got, want)
		}
	})
	t.Run("application_json_routes_to_json", func(t *testing.T) {
		jsonDoc := `{"items":["http://a.com/x","http://b.com/y"],"meta":{"ref":"https://c.com/z"}}`
		base, links, err := ExtractURLs("application/json", []byte(jsonDoc))
		if err != nil {
			t.Fatal(err)
		}
		if base != "" {
			t.Errorf("json baseHref = %q, want empty", base)
		}
		got := hrefs(links)
		want := []string{"http://a.com/x", "http://b.com/y", "https://c.com/z"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("json dispatch:\n got: %v\nwant: %v", got, want)
		}
	})
	t.Run("empty_ctype_sniffs_html", func(t *testing.T) {
		htmlDoc := `<html><body><a href="https://sniff.example/p">p</a></body></html>`
		base, links, err := ExtractURLs("", []byte(htmlDoc))
		if err != nil || base != "" || len(links) != 1 || links[0].Href != "https://sniff.example/p" {
			t.Errorf("empty ctype sniff->html failed: base=%q links=%v err=%v", base, links, err)
		}
	})
	t.Run("empty_ctype_sniffs_text", func(t *testing.T) {
		textDoc := `visit http://txt.example/a and https://txt.example/b now`
		base, links, err := ExtractURLs("", []byte(textDoc))
		if err != nil || base != "" {
			t.Errorf("empty ctype sniff->text failed: base=%q err=%v", base, err)
		}
		got := hrefs(links)
		want := []string{"http://txt.example/a", "https://txt.example/b"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("sniff text:\n got: %v\nwant: %v", got, want)
		}
	})
	t.Run("empty_ctype_plain_text_no_urls", func(t *testing.T) {
		_, links, err := ExtractURLs("", []byte("just plain text, no urls"))
		if err != nil || len(links) != 0 {
			t.Errorf("plain text should yield no links: links=%v err=%v", links, err)
		}
	})
}

func TestParseMalformedOrTruncatedHTMLNoPanic(t *testing.T) {
	t.Run("truncated_tag_does_not_panic", func(t *testing.T) {
		// Deliberately truncated/malformed; must not panic and must return a
		// result (possibly partial).
		in := []byte(`<a href="https://x.com/one"><img src=`)
		_, _, _ = ExtractURLs("text/html", in)
	})

	t.Run("returns_partial_links", func(t *testing.T) {
		in := []byte(`<a href="https://x.com/one"><b>bad <a href="https://x.com/two">`)
		_, links, err := ExtractURLs("text/html", in)
		if err != nil {
			t.Fatalf("must not error on malformed html: %v", err)
		}
		got := hrefs(links)
		want := []string{"https://x.com/one", "https://x.com/two"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("partial extraction:\n got: %v\nwant: %v", got, want)
		}
	})
}

func TestParseDeterminism(t *testing.T) {
	cases := []struct {
		name  string
		ctype string
		data  string
	}{
		{"html_document_order", "text/html",
			`<html><body><a href="https://a.com/1">1</a><img srcset="s1 1x, s2 2x"><a href="https://a.com/2">2</a></body></html>`},
		{"html_base", "text/html", `<base href="/b/"><a href="x"></a>`},
		{"rss", "application/rss+xml",
			`<?xml version="1.0"?><rss version="2.0"><channel><link>http://r.com/f</link><item><link>http://r.com/1</link></item></channel></rss>`},
		{"atom", "application/atom+xml",
			`<feed xmlns="http://www.w3.org/2005/Atom"><link href="http://a.com/s"/><entry><link href="http://a.com/1"/></entry></feed>`},
		{"json_nested", "application/json", `{"z":["https://z.com/1","https://z.com/2"],"a":{"k":"https://a.com/v"}}`},
		{"text", "", `see http://t.com/x and https://t.com/y`},
		{"sniff_html", "", `<html><a href="https://s.com"></a></html>`},
		{"malformed", "text/html", `<a href="https://x.com/one"><b>bad <a href="https://x.com/two">`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b1, l1, err1 := ExtractURLs(c.ctype, []byte(c.data))
			b2, l2, err2 := ExtractURLs(c.ctype, []byte(c.data))
			if err1 != nil || err2 != nil {
				t.Fatalf("unexpected errors: %v / %v", err1, err2)
			}
			if b1 != b2 {
				t.Errorf("baseHref differs across calls: %q vs %q", b1, b2)
			}
			if !reflect.DeepEqual(l1, l2) {
				t.Errorf("links differ across calls:\n first:  %v\n second: %v", hrefs(l1), hrefs(l2))
			}
		})
	}
}

func TestParseSrcsetTokenization(t *testing.T) {
	t.Run("two_candidates", func(t *testing.T) {
		_, links, err := ExtractURLs("text/html", []byte(`<img srcset="a 1x, b 2x">`))
		if err != nil {
			t.Fatal(err)
		}
		got := hrefs(links)
		want := []string{"a", "b"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("srcset tokenization:\n got: %v\nwant: %v", got, want)
		}
	})
	t.Run("quoted_urls_and_descriptors", func(t *testing.T) {
		_, links, err := ExtractURLs("text/html", []byte(`<source srcset=' "a,x" 100w, b 2x '>`))
		if err != nil {
			t.Fatal(err)
		}
		got := hrefs(links)
		want := []string{"a,x", "b"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("quoted srcset tokenization:\n got: %v\nwant: %v", got, want)
		}
	})
	t.Run("empty_and_single", func(t *testing.T) {
		_, links, err := ExtractURLs("text/html", []byte(`<img srcset="a">`))
		if err != nil {
			t.Fatal(err)
		}
		got := hrefs(links)
		want := []string{"a"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("single srcset candidate:\n got: %v\nwant: %v", got, want)
		}
	})
}

func TestParseJSONSortsObjectKeys(t *testing.T) {
	// Insertion order is reversed from sorted order; output must be sorted by
	// key so document "z..a" JSON still yields ["a" url, "z" url].
	jsonDoc := `{"z":"https://z.com","a":"https://a.com"}`
	_, links, err := ExtractURLs("application/json", []byte(jsonDoc))
	if err != nil {
		t.Fatal(err)
	}
	got := hrefs(links)
	want := []string{"https://a.com", "https://z.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("json key ordering:\n got: %v\nwant: %v", got, want)
	}
}

func TestParseNilSafeEmpty(t *testing.T) {
	_, links, err := ExtractURLs("text/html", []byte(""))
	if err != nil {
		t.Fatalf("empty html must not error: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("empty html should yield no links: %v", links)
	}
}

func TestParseContentTypeCaseInsensitive(t *testing.T) {
	_, links, err := ExtractURLs("TEXT/HTML; CHARSET=UTF-8", []byte(`<a href="https://x.com"></a>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].Href != "https://x.com" {
		t.Errorf("uppercased content-type must dispatch to html: %v", links)
	}
}
