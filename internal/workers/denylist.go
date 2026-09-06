package workers

import (
	"net/url"
	"strings"

	"draw/internal/storage"
)

// ShouldDeny reports whether the given URL's hostname domain is denied by the
// provided source registry. It is nil-safe: a nil URL or nil registry never
// denies. The domain is normalized to lower case before lookup, mirroring the
// normalization performed by the underlying registry.
func ShouldDeny(u *url.URL, reg storage.SourceRegistry) bool {
	if u == nil || reg == nil {
		return false
	}
	domain := strings.ToLower(u.Hostname())
	p, _ := reg.Lookup(domain)
	return p != nil && p.Denied
}
