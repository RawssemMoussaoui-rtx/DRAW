package frontier

import (
	"net"
	"strings"

	"draw/internal/config"
	"draw/internal/storage"
)

const (
	defaultMaxCrawlDepth  = 3
	frontierBatchPerDomain = 2
)

func passesHardFilter(c URLCandidate, cfg config.SchedulerConfig, src storage.SourceRegistry) bool {
	u := c.URL
	if u == nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}
	if isBlockedHost(u.Hostname()) {
		return false
	}
	if int(c.CrawlDepth) > effectiveMaxCrawlDepth(c.Domain, cfg, src) {
		return false
	}
	if isDeniedDomain(c.Domain, src) {
		return false
	}
	return true
}

func isBlockedHost(host string) bool {
	if host == "" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()
	}
	return false
}

func isDeniedDomain(domain string, src storage.SourceRegistry) bool {
	if src == nil {
		return false
	}
	if p, ok := src.Lookup(domain); ok && p != nil {
		return p.Denied
	}
	return false
}

func effectiveMaxCrawlDepth(domain string, cfg config.SchedulerConfig, src storage.SourceRegistry) int {
	maxDepth := defaultMaxCrawlDepth
	if src != nil {
		if p, ok := src.Lookup(domain); ok && p != nil && p.CrawlDepthLimit > 0 {
			maxDepth = p.CrawlDepthLimit
		}
	}
	return maxDepth
}

func qualityFor(domain string, src storage.SourceRegistry) float64 {
	if src == nil {
		return 0.3
	}
	if p, ok := src.Lookup(domain); ok && p != nil {
		return p.QualityScore
	}
	return 0.3
}


