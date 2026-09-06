package orchestrator

import (
	"draw/internal/config"
	"draw/internal/manager"
	"draw/internal/model"
	"draw/internal/storage"
)

type TaskCapabilities map[model.TaskType]manager.Capability

func DefaultTaskCapabilities() TaskCapabilities {
	return TaskCapabilities{
		model.TaskTypeDiscover:     manager.CapHTTP,
		model.TaskTypeFetchHTTP:    manager.CapHTTP,
		model.TaskTypeFetchBrowser: manager.CapBrowserAnon,
		model.TaskTypeVerify:       manager.CapHTTP,
		model.TaskTypeReconcile:    manager.CapHTTP,
	}
}

func (c TaskCapabilities) CapFor(t model.TaskType) (manager.Capability, bool) {
	if c == nil {
		return "", false
	}
	cap, ok := c[t]
	return cap, ok
}

type NoopSourceRegistry struct{}

func (NoopSourceRegistry) Lookup(string) (*model.SourceProfile, bool) { return nil, false }
func (NoopSourceRegistry) Upsert(model.SourceProfile) error            { return nil }
func (NoopSourceRegistry) Deny(string) error                           { return nil }
func (NoopSourceRegistry) ProvisionalUpsert(model.SourceProfile) error { return nil }

const defaultDomainLimitConst = 5

func perDomainLimit(domain string, cfg config.SchedulerConfig, src storage.SourceRegistry) int {
	cfgLim := 0
	if cfg.DomainLimits != nil {
		cfgLim = cfg.DomainLimits[domain]
	}
	profLim := 0
	if src != nil {
		if p, ok := src.Lookup(domain); ok && p != nil && !p.Denied {
			profLim = p.PerDomainLimit
		}
	}
	switch {
	case cfgLim > 0 && profLim > 0:
		return min(cfgLim, profLim)
	case cfgLim > 0:
		return cfgLim
	case profLim > 0:
		return profLim
	default:
		return defaultDomainLimitConst
	}
}
