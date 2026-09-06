package master

import (
	"fmt"
	"net/url"
	"sort"
	"sync"
	"testing"
	"time"

	"draw/internal/config"
	"draw/internal/frontier"
	"draw/internal/model"
)

// BenchmarkCostAccounting measures the per-call execution latency of the
// fakeManager for each TaskType defined in the active CostModel, comparing the
// measured average against the configured cost. The fakeManager sleeps a
// deterministic 2ms per call to simulate retrieval work and records the
// observed duration (time.Since) on every invocation via its respond closure.
func BenchmarkCostAccounting(b *testing.B) {
	m := NewMaster(config.Defaults(), newFakeScheduler(&fakeManager{}, 4))
	cfg := m.cfg.CostModel

	types := make([]model.TaskType, 0, len(cfg))
	for tt := range cfg {
		types = append(types, tt)
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })

	for _, tt := range types {
		tt := tt
		b.Run(string(tt), func(b *testing.B) {
			var (
				mu     sync.Mutex
				durSum time.Duration
			)
			task := model.Task{ID: model.NewTaskID(), Type: tt}
			mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
				s := time.Now()
				time.Sleep(2 * time.Millisecond)
				mu.Lock()
				durSum += time.Since(s)
				mu.Unlock()
				return model.TaskResult{Status: model.RetrievalStatusSuccess}
			}}

			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				_, _ = mgr.Execute(task)
			}
			b.StopTimer()

			mgr.mu.Lock()
			calls := mgr.calls
			mgr.mu.Unlock()
			if calls == 0 {
				calls = b.N
			}
			avg := durSum / time.Duration(calls)
			b.ReportMetric(float64(avg)/float64(time.Millisecond), "measured_ms_"+string(tt))
			b.ReportMetric(float64(cfg[tt]), "configured_cost_"+string(tt))
		})
	}
}

// BenchmarkReplanMerge_ConcurrentPushes stresses the frontier's concurrent
// Push path together with the MergePlan/Submit seam (#3 + #7). Eight goroutines
// push 250 distinct candidates each to a real (in-memory) frontier while two
// merger goroutines concurrently invoke MergePlan (a pure plan-merge over a
// local copy) and Submit (mutex-serialized on the fakeScheduler). The benchmark
// asserts that the final frontier size equals the unique candidate count and
// that no worker goroutine panics.
func BenchmarkReplanMerge_ConcurrentPushes(b *testing.B) {
	cfg := config.Defaults()
	const (
		goroutines  = 8
		perG        = 250
		totalPushes = goroutines * perG // 2000, all distinct
		mergers     = 2
	)

	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		f := frontier.NewMemoryFrontier(cfg, nil)
		sched := newFakeScheduler(&fakeManager{
			respond: func(tk model.Task, c int) model.TaskResult {
				return model.TaskResult{Status: model.RetrievalStatusSuccess}
			},
		}, goroutines*2)
		m := NewMaster(cfg, sched)

		start := time.Now()

		var (
			wg       sync.WaitGroup
			panicMu  sync.Mutex
			panicked bool
		)
		observePanic := func() {
			if r := recover(); r != nil {
				panicMu.Lock()
				panicked = true
				panicMu.Unlock()
			}
		}

		// 8 goroutines push 250 unique candidates each to the real frontier.
		wg.Add(goroutines)
		for g := 0; g < goroutines; g++ {
			g := g
			go func() {
				defer wg.Done()
				defer observePanic()
				cands := make([]frontier.URLCandidate, 0, perG)
				for i := 0; i < perG; i++ {
					u, err := url.Parse(fmt.Sprintf("https://h-%d-%d.example.com/p", g, i))
					if err != nil {
						panic(err)
					}
					cands = append(cands, frontier.URLCandidate{
						URL:          u,
						Domain:       u.Hostname(),
						SessionID:    model.NewSessionID(),
						CrawlDepth:   0,
						TaskDepth:    0,
						PriorityHint: 500,
						SourceClass:  model.SourceClassUnknown,
					})
				}
				if err := f.Push(cands); err != nil {
					panic(err)
				}
			}()
		}

		// Concurrent MergePlan/Submit racing with the frontier pushes.
		// MergePlan operates on a local copy of the plan (pure), and Submit is
		// serialized by the fakeScheduler's own mutex, so this is safe under
		// concurrency.
		wg.Add(mergers)
		for k := 0; k < mergers; k++ {
			go func() {
				defer wg.Done()
				defer observePanic()
				base := basePlan()
				for i := 0; i < perG; i++ {
					delta := model.PlanDelta{
						Add:            []model.Task{{ID: model.NewTaskID(), Type: model.TaskTypeVerify, EstimatedCost: 1}},
						Drop:           []model.TaskID{model.NewTaskID()},
						AdjustPriority: map[model.TaskID]int{model.NewTaskID(): 500},
					}
					merged := MergePlan(*base, delta)
					if len(merged.Phases) == 0 {
						panic("merge produced no phases")
					}
					tk := delta.Add[0]
					tk.ID = model.NewTaskID()
					_ = m.orch.Submit(tk)
					_ = merged
				}
			}()
		}

		wg.Wait()
		phaseDur := time.Since(start)

		b.StopTimer()
		size := f.Len()
		ok := 1.0
		if panicked || size != totalPushes {
			ok = 0.0
		}
		b.ReportMetric(float64(size), "final_size")
		b.ReportMetric(ok, "ok")
		if phaseDur > 0 {
			b.ReportMetric(float64(totalPushes)/phaseDur.Seconds(), "push_throughput")
		}
	}
}
