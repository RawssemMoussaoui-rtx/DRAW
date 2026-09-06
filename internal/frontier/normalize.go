package frontier

import (
	"net/url"
	"sort"
	"strings"
)

func CanonicalKey(u *url.URL) string {
	if u == nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http":
		if port == "80" || port == "" {
			port = ""
		}
	case "https":
		if port == "443" || port == "" {
			port = ""
		}
	}
	if port != "" {
		host = host + ":" + port
	}
	q := u.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.Grow(len(scheme) + len(host) + len(u.Path) + 64)
	b.WriteString(scheme)
	b.WriteByte('/')
	b.WriteString(host)
	b.WriteString(u.EscapedPath())
	if len(keys) > 0 {
		b.WriteByte('?')
		for _, k := range keys {
			vs := q[k]
			sort.Strings(vs)
			for _, v := range vs {
				b.WriteByte('/')
				b.WriteString(k)
				b.WriteByte('=')
				b.WriteString(v)
			}
		}
	}
	return b.String()
}

func canonicalURL(c URLCandidate) string {
	if c.URL == nil {
		return ""
	}
	return CanonicalKey(c.URL)
}

func domainOf(u *url.URL) string {
	if u == nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
