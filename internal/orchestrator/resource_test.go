package orchestrator

import (
	"sync"
	"testing"
	"time"

	"draw/internal/config"
)

type flipSampler struct {
	cpu, ram float64
}

func (f *flipSampler) Load() (cpu, ram float64, err error) { return f.cpu, f.ram, nil }

func rcTestCfg() config.SchedulerConfig {
	c := config.Defaults()
	c.MaxGlobalConcurrency = 10
	c.Upgrade.BrowserMaxConcurrency = 4
	return c
}

func baseTime() time.Time { return time.Unix(1000, 0) }

func TestResourceNoPressureFullCaps(t *testing.T) {
	rc := NewResourceController(rcTestCfg(), NoopResourceSampler{})
	caps := rc.EffectiveCaps(baseTime())
	if caps.Browser != caps.BrowserConfigured {
		t.Errorf("no pressure: browser eff %d != configured %d", caps.Browser, caps.BrowserConfigured)
	}
	if caps.Heavy != caps.HeavyConfigured {
		t.Errorf("no pressure: heavy eff %d != configured %d", caps.Heavy, caps.HeavyConfigured)
	}
	if caps.CPU != 0 || caps.RAM != 0 {
		t.Errorf("noop sampler should report 0 load, got cpu=%f ram=%f", caps.CPU, caps.RAM)
	}
}

func TestResourceRAMReducesBrowserAfterHysteresisCooldown(t *testing.T) {
	rc := NewResourceController(rcTestCfg(), &flipSampler{cpu: 0.1, ram: 0.9})
	base := baseTime()
	if c := rc.EffectiveCaps(base); c.Browser != 4 {
		t.Fatalf("at base: want browser 4, got %d", c.Browser)
	}
	if c := rc.EffectiveCaps(base.Add(5 * time.Second)); c.Browser != 4 {
		t.Fatalf("5s < hysteresis(10s): want browser 4, got %d", c.Browser)
	}
	if c := rc.EffectiveCaps(base.Add(10 * time.Second)); c.Browser != 2 {
		t.Fatalf("after hysteresis+cooldown at 0.9 load: want browser 2, got %d", c.Browser)
	}
}

func TestResourceRAMRestoreAfterCooldown(t *testing.T) {
	rc := NewResourceController(rcTestCfg(), &flipSampler{cpu: 0.1, ram: 0.9})
	base := baseTime()
	rc.EffectiveCaps(base)
	rc.EffectiveCaps(base.Add(10 * time.Second))
	if c := rc.EffectiveCaps(base.Add(12 * time.Second)); c.Browser != 2 {
		t.Fatalf("within cooldown (load still high): want browser 2, got %d", c.Browser)
	}
	rc.s.(*flipSampler).ram = 0.3
	if c := rc.EffectiveCaps(base.Add(12 * time.Second)); c.Browser != 2 {
		t.Fatalf("load dropped but cooldown not elapsed: want browser 2, got %d", c.Browser)
	}
	if c := rc.EffectiveCaps(base.Add(16 * time.Second)); c.Browser != 4 {
		t.Fatalf("after cooldown with load below threshold: want browser 4 (restored), got %d", c.Browser)
	}
}

func TestResourceCPUReducesHeavyOnly(t *testing.T) {
	rc := NewResourceController(rcTestCfg(), &flipSampler{cpu: 0.95, ram: 0.1})
	base := baseTime()
	rc.EffectiveCaps(base)
	if c := rc.EffectiveCaps(base.Add(10 * time.Second)); c.Heavy >= c.HeavyConfigured {
		t.Fatalf("CPU high should reduce heavy: got %d, configured %d", c.Heavy, c.HeavyConfigured)
	}
	if c := rc.EffectiveCaps(base.Add(10 * time.Second)); c.Browser != c.Heavy {
		t.Fatalf("browser effective should be bounded by heavy cap under CPU pressure: browser=%d heavy=%d", c.Browser, c.Heavy)
	}
}

func TestResourceBrowserCappedByHeavy(t *testing.T) {
	rc := NewResourceController(rcTestCfg(), &flipSampler{cpu: 0.95, ram: 0.1})
	base := baseTime()
	rc.EffectiveCaps(base)
	rc.EffectiveCaps(base.Add(10 * time.Second))
	if c := rc.EffectiveCaps(base.Add(20 * time.Second)); c.Browser > c.Heavy {
		t.Errorf("browser cap must not exceed heavy cap: browser=%d heavy=%d", c.Browser, c.Heavy)
	}
}

func TestResourceEffectiveNeverExceedsConfigured(t *testing.T) {
	cfg := rcTestCfg()
	rc := NewResourceController(cfg, &flipSampler{cpu: 0.1, ram: 0.5})
	base := baseTime()
	for _, now := range []time.Time{base, base.Add(3 * time.Second), base.Add(30 * time.Second)} {
		caps := rc.EffectiveCaps(now)
		if caps.Browser > caps.BrowserConfigured {
			t.Errorf("browser eff %d > configured %d", caps.Browser, caps.BrowserConfigured)
		}
		if caps.Heavy > caps.HeavyConfigured {
			t.Errorf("heavy eff %d > configured %d", caps.Heavy, caps.HeavyConfigured)
		}
		if caps.Global != cfg.MaxGlobalConcurrency {
			t.Errorf("global cap is not absolute: got %d", caps.Global)
		}
	}
}

func TestResourceSustainedBelowThresholdNoSpuriousReduce(t *testing.T) {
	rc := NewResourceController(rcTestCfg(), &flipSampler{cpu: 0.1, ram: 0.5})
	base := baseTime()
	for i := 0; i < 5; i++ {
		c := rc.EffectiveCaps(base.Add(time.Duration(i) * 10 * time.Second))
		if c.Browser != 4 {
			t.Fatalf("below threshold should not reduce: got browser %d at iter %d", c.Browser, i)
		}
	}
}

func TestResourceDeterminismSameSamplerSameNow(t *testing.T) {
	base := baseTime()
	rc1 := NewResourceController(rcTestCfg(), &flipSampler{cpu: 0.1, ram: 0.9})
	rc1.EffectiveCaps(base)
	rc1.EffectiveCaps(base.Add(10 * time.Second))
	a := rc1.EffectiveCaps(base.Add(10 * time.Second))
	rc2 := NewResourceController(rcTestCfg(), &flipSampler{cpu: 0.1, ram: 0.9})
	rc2.EffectiveCaps(base)
	rc2.EffectiveCaps(base.Add(10 * time.Second))
	b := rc2.EffectiveCaps(base.Add(10 * time.Second))
	if a.Browser != b.Browser || a.Heavy != b.Heavy {
		t.Errorf("non-deterministic: a=%+v b=%+v", a, b)
	}
}

func TestSysSamplerLoadReturnsValidRange(t *testing.T) {
	s := NewSysSampler()
	cpu, ram, err := s.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cpu < 0 || cpu > 1 {
		t.Errorf("cpu out of [0,1]: %v", cpu)
	}
	if ram < 0 || ram > 1 {
		t.Errorf("ram out of [0,1]: %v", ram)
	}
}

func TestSysSamplerIsConcurrentSafe(t *testing.T) {
	s := NewSysSampler()
	if _, _, err := s.Load(); err != nil {
		t.Fatalf("warm-up Load failed: %v", err)
	}

	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			cpu, ram, err := s.Load()
			if err != nil {
				t.Errorf("Load error: %v", err)
				return
			}
			if cpu < 0 || cpu > 1 || ram < 0 || ram > 1 {
				t.Errorf("out of range: cpu=%v ram=%v", cpu, ram)
			}
		}()
	}
	wg.Wait()
}

func TestSysSamplerCPUMonotonicOnIdle(t *testing.T) {
	// Best-effort: on a quiet/idle machine the sampled CPU fraction should
	// remain low. This is environment-dependent; skipped when the host is
	// busy or the syscall is unavailable so the test never produces noise.
	s := NewSysSampler()
	cpu, _, err := s.Load()
	if err != nil {
		t.Skipf("syscall unavailable, skipping idle check: %v", err)
	}
	if cpu > 0.3 {
		t.Skipf("cpu %.3f > 0.3 on this machine; environment is not idle enough", cpu)
	}
}
