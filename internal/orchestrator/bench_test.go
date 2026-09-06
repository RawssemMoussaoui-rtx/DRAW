package orchestrator

import (
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"draw/internal/config"
	"draw/internal/frontier"
	"draw/internal/manager"
	"draw/internal/model"
)

type benchAdmitted struct {
	task model.Task
	mgr  manager.Manager
	slot manager.WorkerSlot
}

type benchDrainResult struct {
	maxGlobal  int
	domainMax  map[string]int
	admitted   int
}

func newBenchScheduler(cfg config.SchedulerConfig) (*Scheduler, frontier.Frontier) {
	rc := NewResourceController(cfg, NewSysSampler())
	fr := frontier.NewMemoryFrontier(cfg, NoopSourceRegistry{})
	s := NewScheduler(cfg, fr, rc, NoopSourceRegistry{}, DefaultTaskCapabilities())
	s.RegisterManager(&fakeManager{caps: []manager.Capability{manager.CapHTTP}}, []manager.Capability{manager.CapHTTP})
	return s, fr
}

func urlCandidates(domA, domB string, nEach int) []frontier.URLCandidate {
	out := make([]frontier.URLCandidate, 0, nEach*2)
	for i := 0; i < nEach; i++ {
		out = append(out, frontier.URLCandidate{
			SessionID:    model.SessionID("bench"),
			URL:          &url.URL{Scheme: "https", Host: domA, Path: fmt.Sprintf("/a%d", i)},
			Domain:       domA,
			PriorityHint: 500,
		})
		out = append(out, frontier.URLCandidate{
			SessionID:    model.SessionID("bench"),
			URL:          &url.URL{Scheme: "https", Host: domB, Path: fmt.Sprintf("/b%d", i)},
			Domain:       domB,
			PriorityHint: 500,
		})
	}
	return out
}

func benchTasks(domA, domB string, nEach int) []model.Task {
	out := make([]model.Task, 0, nEach*2)
	for i := 0; i < nEach; i++ {
		out = append(out, testTask(model.TaskID(fmt.Sprintf("a%d", i)), "ses", model.TaskTypeFetchHTTP, 500, 1, domA))
		out = append(out, testTask(model.TaskID(fmt.Sprintf("b%d", i)), "ses", model.TaskTypeFetchHTTP, 500, 1, domB))
	}
	return out
}

func admitDrainBursts(s *Scheduler, now time.Time) benchDrainResult {
	res := benchDrainResult{domainMax: map[string]int{}}
	for {
		var batch []benchAdmitted
		for {
			task, mgr, slot, ok := s.Admit(now)
			if !ok {
				break
			}
			batch = append(batch, benchAdmitted{task: task, mgr: mgr, slot: slot})
			res.admitted++
			st := s.Stats()
			if st.Active > res.maxGlobal {
				res.maxGlobal = st.Active
			}
			for d, v := range st.DomainActive {
				if v > res.domainMax[d] {
					res.domainMax[d] = v
				}
			}
		}
		if len(batch) == 0 {
			break
		}
		var wg sync.WaitGroup
		for i := range batch {
			a := batch[i]
			wg.Add(1)
			go func(a benchAdmitted) {
				defer wg.Done()
				_, _ = a.mgr.Execute(a.task)
				s.Release(a.slot, a.task.ID, true)
			}(a)
		}
		wg.Wait()
	}
	return res
}

func BenchmarkScheduler_ConcurrencySweep(b *testing.B) {
	for _, g := range []int{10, 15, 20, 25, 30, 40, 50} {
		b.Run(fmt.Sprintf("cap=%d", g), func(b *testing.B) {
			cfg := config.Defaults()
			cfg.MaxGlobalConcurrency = g
			cfg.DomainLimits = map[string]int{"a.example": 1 << 16, "b.example": 3}
			s, fr := newBenchScheduler(cfg)
			if err := fr.Push(urlCandidates("a.example", "b.example", 100)); err != nil {
				b.Fatalf("frontier push: %v", err)
			}
			for _, t := range benchTasks("a.example", "b.example", 100) {
				if err := s.Submit(t); err != nil {
					b.Fatalf("submit: %v", err)
				}
			}
			now := time.Unix(1000, 0)
			res := admitDrainBursts(s, now)

			capViolation := 0
			if res.maxGlobal > g {
				capViolation = res.maxGlobal - g
			}
			dom2Max := res.domainMax["b.example"]
			b.ReportMetric(float64(g), "global_cap")
			b.ReportMetric(float64(res.maxGlobal), "effective_concurrency")
			b.ReportMetric(float64(capViolation), "cap_violation")
			if res.maxGlobal > g {
				b.Fatalf("global cap violated: max_active=%d > global_cap=%d", res.maxGlobal, g)
			}
			if dom2Max > 3 {
				b.Fatalf("per-domain cap violated: b.example max_active=%d > 3", dom2Max)
			}
		})
	}
}

func BenchmarkScheduler_PerDomainContention(b *testing.B) {
	const domA, domB = "a.example", "b.example"
	tasks := benchTasks(domA, domB, 250)
	now := time.Unix(1000, 0)

	cfgC := config.Defaults()
	cfgC.MaxGlobalConcurrency = 500
	cfgC.DomainLimits = map[string]int{domA: 2, domB: 1 << 16}
	sC, _ := newBenchScheduler(cfgC)
	for i := range tasks {
		if err := sC.Submit(tasks[i]); err != nil {
			b.Fatalf("submit: %v", err)
		}
	}
	resC := admitDrainBursts(sC, now)
	capped := resC.domainMax[domA]

	cfgU := config.Defaults()
	cfgU.MaxGlobalConcurrency = 500
	cfgU.DomainLimits = map[string]int{domA: 1 << 16, domB: 1 << 16}
	sU, _ := newBenchScheduler(cfgU)
	for i := range tasks {
		if err := sU.Submit(tasks[i]); err != nil {
			b.Fatalf("submit: %v", err)
		}
	}
	resU := admitDrainBursts(sU, now)
	uncapped := resU.domainMax[domA]

	ok := capped <= 2
	b.ReportMetric(float64(capped), "capped_max_active")
	b.ReportMetric(float64(uncapped), "uncapped_max_active")
	if ok {
		b.ReportMetric(1, "ok")
	} else {
		b.ReportMetric(0, "ok")
	}
	if !ok {
		b.Fatalf("per-domain cap violated: capped_max_active=%d > 2", capped)
	}
}
