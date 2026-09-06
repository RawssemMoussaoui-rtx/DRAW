package managers

import (
	"draw/internal/manager"
	"draw/internal/model"
)

// RouterManager is the single manager.Manager registered with the scheduler.
// It claims the union of all sub-manager capabilities and applies the Q7
// hybrid routing decision inside Execute:
//
//   - FETCH_BROWSER                                -> BrowserAdapter
//   - FETCH_HTTP + SourceClass==NEWS               -> NewsManager
//   - FETCH_HTTP + SourceClass in {OFFICIAL,...}   -> WebManager
//   - DISCOVER, VERIFY, RECONCILE                  -> WebManager
//   - default/unrecognized                         -> WebManager
//
// CapBrowserAuth is advertised by BrowserAdapter but never implicitly selected
// (no TaskType routes to it).
type RouterManager struct {
	web         *WebManager
	news        *NewsManager
	browser     *BrowserAdapter
	social      *SocialManager
	specialized *SpecializedManager
}

func NewRouterManager(web *WebManager, news *NewsManager, b *BrowserAdapter, social *SocialManager, specialized *SpecializedManager) *RouterManager {
	return &RouterManager{
		web:         web,
		news:        news,
		browser:     b,
		social:      social,
		specialized: specialized,
	}
}

func (r *RouterManager) Capabilities() []manager.Capability {
	combined := r.web.Capabilities()
	combined = append(combined, r.news.Capabilities()...)
	combined = append(combined, r.browser.Capabilities()...)
	combined = append(combined, r.social.Capabilities()...)
	combined = append(combined, r.specialized.Capabilities()...)

	seen := make(map[manager.Capability]struct{}, len(combined))
	out := make([]manager.Capability, 0, len(combined))
	for _, c := range combined {
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out
}

func (r *RouterManager) Execute(t model.Task) (model.TaskResult, error) {
	switch t.Type {
	case model.TaskTypeFetchBrowser:
		return r.browser.Execute(t)
	case model.TaskTypeFetchHTTP:
		if t.SourceClass == model.SourceClassNews {
			return r.news.Execute(t)
		}
		return r.web.Execute(t)
	case model.TaskTypeDiscover, model.TaskTypeVerify, model.TaskTypeReconcile:
		return r.web.Execute(t)
	default:
		return r.web.Execute(t)
	}
}
