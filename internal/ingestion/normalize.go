package ingestion

import (
	"net/url"
	"strings"

	"draw/internal/frontier"
)

// Resolve resolves links against the page URL and any <base href>, strips
// fragments, lowercases host, removes default ports, and deduplicates
// identical resolved URLs. Returns absolute *url.URL values in deterministic
// document/resolved order.
func Resolve(pageURL *url.URL, baseHref string, links []ParsedLink) []*url.URL {
	var base *url.URL
	if baseHref != "" {
		if p, err := url.Parse(baseHref); err == nil && p.IsAbs() {
			base = p
		}
	}
	if base == nil {
		base = pageURL
	}

	result := make([]*url.URL, 0, len(links))
	seen := make(map[string]struct{}, len(links))

	for _, link := range links {
		ref, err := url.Parse(link.Href)
		if err != nil {
			continue
		}

		var resolved *url.URL
		if base != nil {
			resolved = base.ResolveReference(ref)
		} else if ref.IsAbs() {
			resolved = ref
		} else {
			continue
		}

		if !resolved.IsAbs() {
			continue
		}

		scheme := strings.ToLower(resolved.Scheme)
		if scheme != "http" && scheme != "https" {
			continue
		}

		normalize(resolved)

		key := frontier.CanonicalKey(resolved)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, resolved)
	}

	return result
}

// normalize lowercases the scheme and host, strips the fragment, and drops
// default ports (80 for http, 443 for https) from u in place.
func normalize(u *url.URL) {
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	u.RawFragment = ""
	switch u.Scheme {
	case "http":
		if u.Port() == "80" {
			u.Host = u.Hostname()
		}
	case "https":
		if u.Port() == "443" {
			u.Host = u.Hostname()
		}
	}
}
