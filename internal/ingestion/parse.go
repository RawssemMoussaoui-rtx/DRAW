// Package ingestion implements Phase E parsing/dispatch for retrieved content.
//
// This module owns content-type dispatch, body parsing, <base href> discovery,
// and deterministic, document-ordered URL extraction. It intentionally does
// NOT normalize, deduplicate, fingerprint, or attach crawl metadata; that work
// belongs to the normalize/dedup modules which consume ParsedLink values.
package ingestion

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// ParsedLink is a single URL reference discovered in a parsed document.
//
// Href is the raw, unresolved reference exactly as written in the source
// (e.g. a relative "/path" or a bare "page.html"). Resolution against the
// page URL and <base href> is the responsibility of the normalize module.
// ParsedLink is the shared contract type consumed by the rest of Phase E.
type ParsedLink struct {
	Href string
}

// textURLRe matches http(s) URLs in plain-text content. It stops at
// whitespace, angle brackets, double quotes, and closing parentheses.
var textURLRe = regexp.MustCompile(`https?://[^\s<>")]+`)

// ExtractURLs dispatches on content-type (sniffing when the header is empty or
// unrecognized), parses the body, discovers <base href>, and extracts link
// references in DOCUMENT order. It must NEVER panic on malformed input.
//
// ctype is the lower-cased Content-Type header value; charset and other
// parameters are tolerated. baseHref is the raw value of the first <base href>
// encountered in an HTML document ("" when absent or in non-HTML content); it is
// returned unresolved for the normalize module to resolve against the page
// URL. links preserves discovery order. err is non-nil only for a hard parse
// failure; on recoverable errors/malformed input whatever links were extracted
// before the failure are still returned.
func ExtractURLs(ctype string, data []byte) (baseHref string, links []ParsedLink, err error) {
	// Safety net: the underlying parsers are already panic-resistant, but the
	// contract forbids any panic from escaping to the caller.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("ingestion: parse panic: %v", r)
		}
	}()

	ct := strings.ToLower(ctype)
	switch {
	case strings.Contains(ct, "json"):
		links, err = extractJSON(data)
		return
	case strings.Contains(ct, "xml"), strings.Contains(ct, "rss"), strings.Contains(ct, "atom"):
		links, err = extractXML(data)
		return
	case strings.Contains(ct, "html"):
		baseHref, links = extractHTML(data)
		return
	default:
		// Empty or unrecognized content-type: sniff the body.
		if looksLikeHTML(data) {
			baseHref, links = extractHTML(data)
		} else {
			links = extractText(data)
		}
		return
	}
}

// looksLikeHTML sniffs a byte suffix to decide whether it is HTML.
// A document is treated as HTML when its prefix contains an explicit
// "<html"/"<body" marker or any tag-like "<" + ASCII-letter sequence.
func looksLikeHTML(data []byte) bool {
	const sniffLimit = 8192
	n := len(data)
	if n > sniffLimit {
		n = sniffLimit
	}
	sample := bytes.ToLower(data[:n])
	if bytes.Contains(sample, []byte("<html")) || bytes.Contains(sample, []byte("<body")) {
		return true
	}
	for i := 0; i+1 < len(sample); i++ {
		if sample[i] == '<' && isASCIILetter(sample[i+1]) {
			return true
		}
	}
	return false
}

func isASCIILetter(b byte) bool {
	return b >= 'a' && b <= 'z'
}

// extractHTML tokenizes the bytes as HTML in a single forward pass, recording
// the first <base href> and emitting link references in document order.
func extractHTML(data []byte) (baseHref string, links []ParsedLink) {
	links = []ParsedLink{}
	z := html.NewTokenizer(bytes.NewReader(data))
	for {
		if tt := z.Next(); tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			if tt == html.ErrorToken {
				break
			}
			continue
		}
		tok := z.Token()
		name := tok.DataAtom
		if name == atom.Base && baseHref == "" {
			if v := firstAttrValue(tok, "href"); v != "" {
				baseHref = v
			}
		}
		appendTagLinks(&links, name, &tok)
	}
	return baseHref, links
}

// appendTagLinks emits link references for a start/self-closing tag based on
// the tag kind and its attributes, in attribute document order.
func appendTagLinks(links *[]ParsedLink, name atom.Atom, tok *html.Token) {
	switch name {
	case atom.A, atom.Link:
		emitAttr(links, tok, "href")
	case atom.Iframe, atom.Frame, atom.Script, atom.Source:
		emitAttr(links, tok, "src")
	}
	// srcset is shared by picture/<source>/<img> and any element carrying it.
	emitSrcset(links, tok, "srcset")
}

func emitAttr(links *[]ParsedLink, tok *html.Token, key string) {
	for _, a := range tok.Attr {
		if a.Key == key {
			*links = append(*links, ParsedLink{Href: a.Val})
			return
		}
	}
}

func emitSrcset(links *[]ParsedLink, tok *html.Token, key string) {
	for _, a := range tok.Attr {
		if a.Key != key {
			continue
		}
		for _, u := range parseSrcset(a.Val) {
			*links = append(*links, ParsedLink{Href: u})
		}
		return
	}
}

// firstAttrValue returns the value of the first attribute named key on tok.
func firstAttrValue(tok html.Token, key string) string {
	for _, a := range tok.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// parseSrcset tokenizes an HTML srcset attribute into its candidate URLs,
// preserving declaration order. Candidates are comma-separated, but commas
// inside a quoted URL are preserved so URLs like "a,x" stay intact. Each
// candidate contributes at most one URL: the url(...) form (inner content) or
// the first whitespace delimited token (quotes stripped).
func parseSrcset(s string) []string {
	var out []string
	var part strings.Builder
	quote := byte(0)
	flush := func() {
		p := strings.TrimSpace(part.String())
		part.Reset()
		if p == "" {
			return
		}
		if url, ok := srcsetURL(p); ok && url != "" {
			out = append(out, url)
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			part.WriteByte(c)
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
			part.WriteByte(c)
		case ',':
			flush()
		default:
			part.WriteByte(c)
		}
	}
	flush()
	return out
}

func srcsetURL(part string) (string, bool) {
	if strings.HasPrefix(part, "url(") {
		rest := part[len("url("):]
		rest = strings.TrimRight(rest, ")")
		return strings.Trim(rest, `"`+"'"), true
	}
	url := part
	if i := strings.IndexAny(part, " \t\n\r\f"); i >= 0 {
		url = part[:i]
	}
	return strings.Trim(url, `"`+"'"), true
}

// extractXML parses an RSS/Atom/XML document with encoding/xml, emitting URL
// references found in <link>/<atom:link> elements (text content or href
// attribute) in document order. baseHref is always "".
func extractXML(data []byte) (links []ParsedLink, err error) {
	links = []ParsedLink{}
	dec := xml.NewDecoder(bytes.NewReader(data))
	inLink := false
	href := ""
	var text strings.Builder
	flush := func() {
		if href != "" {
			links = append(links, ParsedLink{Href: href})
			return
		}
		if t := strings.TrimSpace(text.String()); t != "" {
			links = append(links, ParsedLink{Href: t})
		}
	}
	for {
		tok, tokErr := dec.Token()
		if tokErr != nil {
			if inLink {
				flush()
			}
			if tokErr == io.EOF {
				return links, nil
			}
			return links, fmt.Errorf("ingestion: xml parse: %w", tokErr)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "link" {
				inLink = true
				href = ""
				text.Reset()
				for _, a := range t.Attr {
					if a.Name.Local == "href" {
						href = a.Value
						break
					}
				}
			} else {
				inLink = false
			}
		case xml.EndElement:
			if t.Name.Local == "link" && inLink {
				flush()
				inLink = false
				href = ""
				text.Reset()
			} else if t.Name.Local != "link" {
				inLink = false
			}
		case xml.CharData:
			if inLink && href == "" {
				text.Write(t)
			}
		}
	}
}

// extractJSON best-effort decodes JSON and collects string values that look
// like http(s) URLs. Objects are walked in sorted-key order so output is
// deterministic; arrays preserve element order.
func extractJSON(data []byte) (links []ParsedLink, err error) {
	var v interface{}
	if err = json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("ingestion: json parse: %w", err)
	}
	links = []ParsedLink{}
	walkJSON(v, &links)
	return links, nil
}

func walkJSON(v interface{}, links *[]ParsedLink) {
	switch x := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			walkJSON(x[k], links)
		}
	case []interface{}:
		for _, e := range x {
			walkJSON(e, links)
		}
	case string:
		if isURL(x) {
			*links = append(*links, ParsedLink{Href: x})
		}
	}
}

func isURL(s string) bool {
	ls := strings.ToLower(s)
	return strings.HasPrefix(ls, "http://") || strings.HasPrefix(ls, "https://")
}

// extractText is the plain-text fallback: it scans for http(s) URL tokens.
func extractText(data []byte) []ParsedLink {
	matches := textURLRe.FindAllString(string(data), -1)
	links := make([]ParsedLink, 0, len(matches))
	for _, m := range matches {
		links = append(links, ParsedLink{Href: m})
	}
	return links
}
