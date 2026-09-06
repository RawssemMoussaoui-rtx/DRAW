package orchestrator

import (
	"math"
	"sync"
	"time"

	"draw/internal/config"
)

type ResourceSampler interface {
	Load() (cpu, ram float64, err error)
}

type NoopResourceSampler struct{}

func (NoopResourceSampler) Load() (cpu, ram float64, err error) { return 0, 0, nil }

type Caps struct {
	Global            int
	BrowserConfigured int
	Browser           int
	HeavyConfigured   int
	Heavy             int
	CPU               float64
	RAM               float64
}

type laneState struct {
	eff       int
	overSince time.Time
	lastTrans time.Time
}

type ResourceController struct {
	cfg config.SchedulerConfig
	s   ResourceSampler

	mu       sync.Mutex
	browser  laneState
	heavy    laneState
}

func NewResourceController(cfg config.SchedulerConfig, s ResourceSampler) *ResourceController {
	if s == nil {
		s = NoopResourceSampler{}
	}
	return &ResourceController{
		cfg:     cfg,
		s:       s,
		browser: laneState{eff: cfg.Upgrade.BrowserMaxConcurrency},
		heavy:   laneState{eff: cfg.MaxGlobalConcurrency},
	}
}

func (rc *ResourceController) EffectiveCaps(now time.Time) Caps {
	cpu, ram, _ := rc.s.Load()
	rc.mu.Lock()
	defer rc.mu.Unlock()

	bCfg := rc.cfg.Upgrade.BrowserMaxConcurrency
	hCfg := rc.cfg.MaxGlobalConcurrency

	heavyEff := rc.transition(rc.heavy, float64(hCfg), cpu, rc.cfg.CPUThreshold, now, &rc.heavy)
	browserRaw := rc.transition(rc.browser, float64(bCfg), ram, rc.cfg.RAMThreshold, now, &rc.browser)
	browserEff := browserRaw
	if browserEff > heavyEff {
		browserEff = heavyEff
	}
	if browserEff < 0 {
		browserEff = 0
	}

	return Caps{
		Global:            hCfg,
		BrowserConfigured: bCfg,
		Browser:           browserEff,
		HeavyConfigured:   hCfg,
		Heavy:             heavyEff,
		CPU:               cpu,
		RAM:               ram,
	}
}

func (rc *ResourceController) transition(st laneState, configured, load, threshold float64, now time.Time, out *laneState) int {
	hyst := rc.cfg.HysteresisWindow
	cool := rc.cfg.Cooldown
	if load > threshold {
		if st.overSince.IsZero() {
			st.overSince = now
		}
		if now.Sub(st.overSince) < hyst || now.Sub(st.lastTrans) < cool {
			out.eff, out.overSince, out.lastTrans = st.eff, st.overSince, st.lastTrans
			return st.eff
		}
		desired := reduce(int(configured), load, threshold)
		if desired < st.eff {
			out.eff, out.overSince, out.lastTrans = desired, st.overSince, now
			return desired
		}
		out.eff, out.overSince, out.lastTrans = st.eff, st.overSince, st.lastTrans
		return st.eff
	}
	if now.Sub(st.lastTrans) < cool {
		out.eff, out.overSince, out.lastTrans = st.eff, time.Time{}, st.lastTrans
		return st.eff
	}
	if st.eff < int(configured) {
		out.eff, out.overSince, out.lastTrans = int(configured), time.Time{}, now
		return int(configured)
	}
	out.eff, out.overSince, out.lastTrans = st.eff, time.Time{}, st.lastTrans
	return st.eff
}

func reduce(configured int, load, threshold float64) int {
	span := 1.0 - threshold
	if span <= 0 {
		return configured
	}
	pressure := (load - threshold) / span
	if pressure < 0 {
		pressure = 0
	}
	if pressure > 1 {
		pressure = 1
	}
	return int(math.Floor(float64(configured) * (1.0 - pressure)))
}

type staticSampler struct {
	cpu, ram float64
}

func (s staticSampler) Load() (cpu, ram float64, err error) { return s.cpu, s.ram, nil }

func StaticSampler(cpu, ram float64) ResourceSampler {
	return staticSampler{cpu: cpu, ram: ram}
}
