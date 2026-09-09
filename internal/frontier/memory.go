package frontier

import (
	"net/url"
	"sort"
	"strings"
	"sync"

	"draw/internal/config"
	"draw/internal/model"
	"draw/internal/storage"
)

type MemoryFrontier struct {
	cfg         config.SchedulerConfig
	src         storage.SourceRegistry
	s           scorer
	mu          sync.RWMutex
	byKey       map[string]*entry
	sessionKeys map[model.SessionID][]string
}

type entry struct {
	c     URLCandidate
	score float64
}

func NewMemoryFrontier(cfg config.SchedulerConfig, src storage.SourceRegistry) *MemoryFrontier {
	return &MemoryFrontier{
		cfg:         cfg,
		src:         src,
		s:           newScorer(cfg),
		byKey:       map[string]*entry{},
		sessionKeys: map[model.SessionID][]string{},
	}
}

func sessionKey(sessionID model.SessionID, canonicalURL string) string {
	return string(sessionID) + ":" + canonicalURL
}

func (f *MemoryFrontier) Push(candidates []URLCandidate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range candidates {
		c := candidates[i]
		if c.URL == nil || c.Domain == "" {
			continue
		}
		if !passesHardFilter(c, f.cfg, f.src) {
			continue
		}
		canonical := canonicalURL(c)
		if canonical == "" {
			continue
		}
		key := sessionKey(c.SessionID, canonical)
		if _, exists := f.byKey[key]; exists {
			continue
		}
		f.byKey[key] = &entry{c: c, score: f.s.score(c, f.src)}
		f.sessionKeys[c.SessionID] = append(f.sessionKeys[c.SessionID], key)
	}
	return nil
}

func (f *MemoryFrontier) Next(n int) []URLCandidate {
	// Next is session-agnostic by design: it returns candidates from ALL sessions.
	// This relies on the architectural assumption that only one session runs
	// actively at a time, so there is no risk of interleaving results across
	// different sessions.
	if n <= 0 {
		return nil
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	all := make([]*entry, 0, len(f.byKey))
	for _, e := range f.byKey {
		all = append(all, e)
	}
	sort.SliceStable(all, func(i, j int) bool {
		if f.less(all[i], all[j]) {
			return true
		}
		if f.less(all[j], all[i]) {
			return false
		}
		return all[i].c.URL.String() < all[j].c.URL.String()
	})
	out := make([]URLCandidate, 0, min(n, len(all)))
	domainCount := map[string]int{}
	for _, e := range all {
		if len(out) >= n {
			break
		}
		d := e.c.Domain
		if domainCount[d] >= frontierBatchPerDomain {
			continue
		}
		out = append(out, e.c)
		domainCount[d]++
	}
	return out
}

func (f *MemoryFrontier) Has(domain, rawurl string) bool {
	// Has is session-agnostic by design: it checks whether a URL exists in the
	// frontier across ALL sessions. This relies on the architectural assumption
	// that only one session runs actively at a time, so there are no
	// cross-session collision concerns. Because byKey is keyed by composite
	// session-scoped keys (sessionID:canonicalURL), this performs an O(N) scan
	// extracting the canonical-URL suffix from each key.
	if rawurl == "" {
		return false
	}
	u, err := url.Parse(rawurl)
	if err != nil {
		return false
	}
	target := CanonicalKey(u)
	if target == "" {
		return false
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	for key := range f.byKey {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) == 2 && parts[1] == target {
			return true
		}
	}
	return false
}

func (f *MemoryFrontier) Score(c URLCandidate) float64 {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.s.score(c, f.src)
}

func (f *MemoryFrontier) Len() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.byKey)
}

func (f *MemoryFrontier) ResetSession(sessionID model.SessionID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys, ok := f.sessionKeys[sessionID]
	if !ok || len(keys) == 0 {
		return nil
	}
	for _, key := range keys {
		delete(f.byKey, key)
	}
	delete(f.sessionKeys, sessionID)
	return nil
}

func (f *MemoryFrontier) less(a, b *entry) bool {
	if a.score != b.score {
		return a.score > b.score
	}
	pa := safePriority(a.c.PriorityHint)
	pb := safePriority(b.c.PriorityHint)
	if pa != pb {
		return pa > pb
	}
	qa := qualityFor(a.c.Domain, f.src)
	qb := qualityFor(b.c.Domain, f.src)
	if qa != qb {
		return qa > qb
	}
	return a.c.Domain < b.c.Domain
}

var (
	_ Frontier     = (*MemoryFrontier)(nil)
	_ FrontierSink = (*MemoryFrontier)(nil)
	_              = model.TaskTypeFetchHTTP
)
