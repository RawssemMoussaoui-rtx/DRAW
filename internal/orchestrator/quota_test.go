package orchestrator

import (
	"testing"
	"time"

	"draw/internal/config"
	"draw/internal/frontier"
	"draw/internal/manager"
	"draw/internal/model"
)

func newQuotaScheduler(t *testing.T, floor float64) *Scheduler {
	t.Helper()
	cfg := config.Defaults()
	cfg.MaxGlobalConcurrency = 1
	cfg.ExplorationQuotaFloor = floor
	rc := NewResourceController(cfg, NoopResourceSampler{})
	fr := frontier.NewMemoryFrontier(cfg, NoopSourceRegistry{})
	s := NewScheduler(cfg, fr, rc, NoopSourceRegistry{}, DefaultTaskCapabilities())
	if err := s.RegisterManager(&fakeManager{caps: []manager.Capability{manager.CapHTTP}}, []manager.Capability{manager.CapHTTP}); err != nil {
		t.Fatalf("register manager: %v", err)
	}
	return s
}

// submitSkewed seeds a heavily skewed queue: 60 high-priority VERIFY tasks
// (priority 1000) and 40 low-priority exploration tasks (DISCOVER/FETCH_HTTP,
// priority 10). Without the quota the high-priority pool would fully starve
// exploration over any bounded run smaller than 60.
func submitSkewed(t *testing.T, s *Scheduler) {
	t.Helper()
	for i := 0; i < 60; i++ {
		if err := s.Submit(testTask(model.TaskID("v"+itoaOS(i)), "ses1", model.TaskTypeVerify, 1000, 1, "dom"+itoaOS(i%40)+".example")); err != nil {
			t.Fatalf("submit v%d: %v", i, err)
		}
	}
	for i := 0; i < 25; i++ {
		if err := s.Submit(testTask(model.TaskID("d"+itoaOS(i)), "ses1", model.TaskTypeDiscover, 10, 1, "dom"+itoaOS(i%40)+".example")); err != nil {
			t.Fatalf("submit d%d: %v", i, err)
		}
	}
	for i := 0; i < 15; i++ {
		if err := s.Submit(testTask(model.TaskID("f"+itoaOS(i)), "ses1", model.TaskTypeFetchHTTP, 10, 1, "dom"+itoaOS(i%40)+".example")); err != nil {
			t.Fatalf("submit f%d: %v", i, err)
		}
	}
}

// drainAdmit repeatedly admits one task and releases its slot (simulating
// instant completion), recording the admitted task. With MaxGlobalConcurrency=1
// this yields a clean serial execution stream. It stops once admit fails (queue
// empty) or count tasks have been admitted, whichever comes first.
func drainAdmit(t *testing.T, s *Scheduler, count int) []model.Task {
	t.Helper()
	now := time.Unix(1000, 0)
	out := make([]model.Task, 0, count)
	for len(out) < count {
		task, _, slot, ok := s.Admit(now)
		if !ok {
			break
		}
		out = append(out, task)
		s.Release(slot, task.ID, true)
	}
	return out
}

// TestQuotaEnforcesExplorationFloor: with the 20% floor enabled, draining the
// full skewed queue yields DISCOVER+FETCH_HTTP >= 20% of executed tasks
// (the pool itself is 40%, and the quota additionally pulls exploration forward
// so it is never starved over a bounded prefix).
func TestQuotaEnforcesExplorationFloor(t *testing.T) {
	s := newQuotaScheduler(t, 0.20)
	submitSkewed(t, s)

	executed := drainAdmit(t, s, 1000) // drain everything (queue has 100)
	if len(executed) != 100 {
		t.Fatalf("admitted %d tasks, want 100 (full drain)", len(executed))
	}

	total, explore, _ := s.QuotaStats()
	if total != 100 {
		t.Fatalf("QuotaStats total=%d, want 100", total)
	}
	quota := float64(explore) / float64(total)
	if quota < 0.20 {
		t.Errorf("DiscoveryQuota=%.3f, want >= 0.20 (explored=%d/%d)", quota, explore, total)
	}

	// The quota must pull exploration forward: within the first 30 admits the
	// exploration share must already exceed what a pure priority ordering would
	// yield (pure ordering would give 0 exploration in the first 30 because all
	// 60 VERIFY outrank the 40 exploration tasks).
	prefix := executed[:30]
	prefixExplore := 0
	for _, tk := range prefix {
		if explorationTaskTypes[tk.Type] {
			prefixExplore++
		}
	}
	if prefixExplore == 0 {
		t.Errorf("quota did not pull exploration forward: 0 exploration in first 30 admits")
	}
	t.Logf("prefix(30) explore=%d (%.1f%%); final explore=%d/%.0f (%.1f%%)",
		prefixExplore, 100*float64(prefixExplore)/30, explore, float64(total), 100*quota)
}

// TestQuotaDisabledAllowsStarvation: with the quota disabled (floor=0), the same
// heavily skewed queue starves exploration entirely over a bounded run smaller
// than the 60-task high-priority pool — proving the floor (not luck) is what
// guarantees the >= 20% share.
func TestQuotaDisabledAllowsStarvation(t *testing.T) {
	s := newQuotaScheduler(t, 0.0)
	submitSkewed(t, s)

	executed := drainAdmit(t, s, 40) // bounded run < 60 high-priority pool
	if len(executed) != 40 {
		t.Fatalf("admitted %d tasks, want 40", len(executed))
	}
	for _, tk := range executed {
		if explorationTaskTypes[tk.Type] {
			t.Fatalf("with quota disabled, non-exploration ordering admitted an exploration task %s", tk.Type)
		}
	}
	total, explore, _ := s.QuotaStats()
	if explore != 0 {
		t.Errorf("quota disabled: explored=%d, want 0 (starved by skewed priorities)", explore)
	}
	_ = total
}

// TestQuotaFallsBackWhenNoExplorationAvailable: the quota must not stall
// admission when no eligible exploration task exists — it falls back to the
// best available task and records no spurious inversion.
func TestQuotaFallsBackWhenNoExplorationAvailable(t *testing.T) {
	s := newQuotaScheduler(t, 0.20)
	if err := s.Submit(testTask("t1", "ses1", model.TaskTypeVerify, 1000, 1, "a.example")); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	task, _, slot, ok := s.Admit(now)
	if !ok {
		t.Fatal("expected admit of VERIFY when no exploration is queued")
	}
	if task.Type != model.TaskTypeVerify {
		t.Errorf("got %s, want VERIFY", task.Type)
	}
	s.Release(slot, task.ID, true)

	_, explore, inversions := s.QuotaStats()
	if explore != 0 {
		t.Errorf("explore=%d, want 0 (no exploration tasks existed)", explore)
	}
	if inversions != 0 {
		t.Errorf("inversions=%d, want 0 (no quota override occurred)", inversions)
	}
}
