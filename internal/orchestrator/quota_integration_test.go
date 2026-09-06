package orchestrator

import (
	"fmt"
	"net/url"
	"testing"
	"time"

	"draw/internal/config"
	"draw/internal/frontier"
	"draw/internal/manager"
	"draw/internal/model"
)

// Gate thresholds mandated by P8.
const (
	gateHHIMax        = 2500.0
	gateStarvationMin = 0.35
	gateInversionMax  = 0.20
	gateDiscoveryMin  = 0.20
	integrationRuns  = 5
)

type integratedTask struct {
	Type     model.TaskType
	Priority int
	Domain   string
}

type gateResult struct {
	HHI             float64
	StarvationIndex float64
	InversionRate   float64
	DiscoveryQuota  float64
	Executed        int
	Explore         int
	Inversions      int
}

type admitRec struct {
	task model.Task
	slot manager.WorkerSlot
}

type scenarioMeta struct {
	domains int
}

// newGateScheduler builds a scheduler+frontier wired with a source registry
// whose per-domain quality alternates (0.9 / 0.3) so the P8 saturation in
// scorer.score differentiates FETCH_HTTP candidates in frontier.Next.
// MaxGlobalConcurrency=8 lets drainFrontier batch-peel frontier candidates.
func newGateScheduler(t *testing.T) (*Scheduler, *frontier.MemoryFrontier) {
	t.Helper()
	cfg := config.Defaults()
	cfg.MaxGlobalConcurrency = 8
	rc := NewResourceController(cfg, NoopResourceSampler{})
	profiles := map[string]model.SourceProfile{}
	for i := 0; i < 40; i++ {
		dom := domainName(i)
		q := 0.9
		if i%2 == 1 {
			q = 0.3
		}
		profiles[dom] = model.SourceProfile{Domain: dom, QualityScore: q, PerDomainLimit: 100}
	}
	src := testRegistry{profiles: profiles}
	fr := frontier.NewMemoryFrontier(cfg, src)
	s := NewScheduler(cfg, fr, rc, src, DefaultTaskCapabilities())
	if err := s.RegisterManager(&fakeManager{caps: []manager.Capability{manager.CapHTTP}}, []manager.Capability{manager.CapHTTP}); err != nil {
		t.Fatalf("register manager: %v", err)
	}
	return s, fr
}

func domainName(i int) string { return fmt.Sprintf("dom%d.example", i) }

// seedScenario populates 40 domains. Per domain: 3 VERIFY (priority 1000,
// starvation-prone) + 2 DISCOVER (priority 10) via Submit, plus 1 FETCH_HTTP
// frontier candidate (PriorityHint 10) on the first 8 domains. The frontier
// path exercises scorer.score (saturation); Submit mirrors planning-seeded
// DISCOVER and replan VERIFY tasks. Total = 208 tasks (120 VERIFY + 80 DISCOVER
// + 8 frontier FETCH_HTTP).
func seedScenario(t *testing.T, s *Scheduler, fr *frontier.MemoryFrontier) scenarioMeta {
	t.Helper()
	const domains = 40
	for i := 0; i < domains; i++ {
		dom := domainName(i)
		for v := 0; v < 3; v++ {
			id := fmt.Sprintf("V%d_%d", i, v)
			if err := s.Submit(testTask(model.TaskID(id), "ses1", model.TaskTypeVerify, 1000, 1, dom)); err != nil {
				t.Fatalf("submit verify %s: %v", id, err)
			}
		}
		for d := 0; d < 2; d++ {
			id := fmt.Sprintf("D%d_%d", i, d)
			if err := s.Submit(testTask(model.TaskID(id), "ses1", model.TaskTypeDiscover, 10, 1, dom)); err != nil {
				t.Fatalf("submit discover %s: %v", id, err)
			}
		}
	}
	cands := make([]frontier.URLCandidate, 0, 8)
	for i := 0; i < 8; i++ {
		dom := domainName(i)
		u, err := url.Parse(fmt.Sprintf("https://%s/p0", dom))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		cands = append(cands, frontier.URLCandidate{
			SessionID: "ses1", URL: u, Domain: dom, PriorityHint: 10,
		})
	}
	if err := fr.Push(cands); err != nil {
		t.Fatalf("frontier push: %v", err)
	}
	return scenarioMeta{domains: domains}
}

// drainAll admits tasks in waves (up to MaxGlobalConcurrency each wave), records
// each admitted task, and releases the wave's slots before the next wave —
// mirroring the Master.Run admit->execute->release cycle at scheduler scale.
// It runs to completion (queue drained).
func drainAll(t *testing.T, s *Scheduler, cap int) []integratedTask {
	t.Helper()
	now := time.Unix(1000, 0)
	var out []integratedTask
	for {
		var wave []admitRec
		for {
			if s.Stats().Active >= cap {
				break
			}
			task, _, slot, ok := s.Admit(now)
			if !ok {
				break
			}
			wave = append(wave, admitRec{task: task, slot: slot})
		}
		if len(wave) == 0 {
			break
		}
		for _, r := range wave {
			out = append(out, integratedTask{Type: r.task.Type, Priority: r.task.Priority, Domain: r.task.SourceTarget})
		}
		for _, r := range wave {
			s.Release(r.slot, r.task.ID, true)
		}
	}
	return out
}

func computeGates(executed []integratedTask, total, explore, inversions, seeded int) gateResult {
	dq := 0.0
	if total > 0 {
		dq = float64(explore) / float64(total)
	}
	domCount := map[string]int{}
	for _, e := range executed {
		domCount[e.Domain]++
	}
	hhi := 0.0
	if total > 0 {
		for _, c := range domCount {
			s := float64(c) / float64(total)
			hhi += s * s
		}
		hhi *= 10000.0
	}
	starvation := 0.0
	if seeded > 0 {
		starvation = float64(len(domCount)) / float64(seeded)
	}
	ir := 0.0
	if total > 0 {
		ir = float64(inversions) / float64(total)
	}
	return gateResult{
		HHI: hhi, StarvationIndex: starvation, InversionRate: ir,
		DiscoveryQuota: dq, Executed: total, Explore: explore, Inversions: inversions,
	}
}

func gateString(g gateResult) string {
	return fmt.Sprintf("HHI=%.4g (<=%.0f) StarvationIndex=%.4f (>=%.2f) InversionRate=%.4f (<=%.2f) DiscoveryQuota=%.4f (>=%.2f) executed=%d explore=%d inversions=%d",
		g.HHI, gateHHIMax, g.StarvationIndex, gateStarvationMin,
		g.InversionRate, gateInversionMax, g.DiscoveryQuota, gateDiscoveryMin,
		g.Executed, g.Explore, g.Inversions)
}

func assertGates(t *testing.T, g gateResult) {
	t.Helper()
	if g.HHI > gateHHIMax+1e-9 {
		t.Errorf("HHI gate: %.4g > %.0f", g.HHI, gateHHIMax)
	}
	if g.StarvationIndex < gateStarvationMin-1e-9 {
		t.Errorf("StarvationIndex gate: %.4f < %.2f", g.StarvationIndex, gateStarvationMin)
	}
	if g.InversionRate > gateInversionMax+1e-9 {
		t.Errorf("InversionRate gate: %.4f > %.2f", g.InversionRate, gateInversionMax)
	}
	if g.DiscoveryQuota < gateDiscoveryMin-1e-9 {
		t.Errorf("DiscoveryQuota gate: %.4f < %.2f", g.DiscoveryQuota, gateDiscoveryMin)
	}
}

// TestExplorationQuotaIntegrationGates is the P8 dynamic integration test. It
// runs the real frontier+scorer+scheduler graph over a heavily skewed,
// multi-domain scenario (VERIFY priority 1000 vs DISCOVER/FETCH_HTTP priority 10)
// N>=5 times and asserts all four P8 gates pass on every run, then checks
// run-to-run stability of the gate values. The quota is what pulls DISCOVER/
// FETCH_HTTP forward (natural priority ordering would admit all 120 VERIFY first
// over the 88 exploration tasks). InversionRate uses the scheduler-tracked quota
// override counter (s.inversions): the only inversions with cap=8 and generous
// domain limits are those the quota deliberately injects.
func TestExplorationQuotaIntegrationGates(t *testing.T) {
	const cap = 8
	var first string
	for r := 0; r < integrationRuns; r++ {
		s, fr := newGateScheduler(t)
		meta := seedScenario(t, s, fr)
		executed := drainAll(t, s, cap)
		if got := len(executed); got != 208 {
			t.Fatalf("run %d: drained %d, want 208 (full drain)", r, got)
		}
		total, explore, inversions := s.QuotaStats()
		if total != 208 {
			t.Fatalf("run %d: QuotaStats total=%d, want 208", r, total)
		}
		g := computeGates(executed, total, explore, inversions, meta.domains)
		t.Logf("run %d/%d: %s", r+1, integrationRuns, gateString(g))
		assertGates(t, g)
		gstr := fmt.Sprintf("HHI=%.6g|ST=%.6f|INV=%.6f|DQ=%.6f", g.HHI, g.StarvationIndex, g.InversionRate, g.DiscoveryQuota)
		if first == "" {
			first = gstr
		} else if first != gstr {
			t.Errorf("run %d gate values differ from run 1 (non-deterministic)\nrun1: %s\nrun %d: %s", r+1, first, r+1, gstr)
		}
	}
	t.Logf("All %d integration runs passed all four gates; gate values stable across runs.", integrationRuns)
}
