package orchestrator

import (
	"container/heap"
	"fmt"
	"strings"
	"sync"
	"time"

	"draw/internal/config"
	"draw/internal/frontier"
	"draw/internal/manager"
	"draw/internal/master"
	"draw/internal/model"
	"draw/internal/storage"
)

const frontierDrainBatchMax = 32

// explorationTaskTypes are the "exploration" task categories that the admission
// quota must keep flowing: DISCOVER (seed/rediscovery) and FETCH_HTTP (frontier
// retrieval). FETCH_BROWSER is deliberately excluded (expensive, capacity-capped)
// and VERIFY/RECONCILE are "exploitation" categories.
var explorationTaskTypes = map[model.TaskType]bool{
	model.TaskTypeDiscover:  true,
	model.TaskTypeFetchHTTP: true,
}

const defaultPriorityHint = 500

type Scheduler struct {
	cfg    config.SchedulerConfig
	fr     frontier.Frontier
	src    storage.SourceRegistry
	rc     *ResourceController
	caps   TaskCapabilities
	registered []registeredManager

	mu           sync.Mutex
	pending      taskHeap
	keyIndex     map[string]*taskItem
	materialized map[string]bool
	active       map[model.TaskID]*activeTask
	domainActive map[string]int
	browserActive int
	heavyActive   int
	slotSeq       uint64
	stats         master.SchedulerStats

	// executedByType counts tasks admitted (and therefore dispatched to a
	// worker) by TaskType. It drives the exploration quota (DISCOVER+FETCH_HTTP
	// floor) and is mutated only under mu.
	executedByType map[model.TaskType]int
	// quotaFloor is the minimum executed fraction in explorationTaskTypes
	// (0 disables the quota). See config.ExplorationQuotaFloor.
	quotaFloor float64
	// inversions counts quota-driven priority inversions: admits where an
	// eligible non-exploration task of higher priority was passed over to
	// satisfy the exploration floor. Surgical instrumentation for P8 reporting.
	inversions int
}

type (
	taskItem struct {
		task model.Task
		idx  int
	}
	taskHeap []*taskItem
	activeTask struct {
		task    model.Task
		manager manager.Manager
		slot    manager.WorkerSlot
	}
	registeredManager struct {
		m    manager.Manager
		caps []manager.Capability
	}
)

func (h taskHeap) Len() int           { return len(h) }
func (h taskHeap) Less(i, j int) bool { return taskLess(h[i].task, h[j].task) }
func (h taskHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].idx = i
	h[j].idx = j
}
func (h *taskHeap) Push(x any) {
	it := x.(*taskItem)
	it.idx = len(*h)
	*h = append(*h, it)
}
func (h *taskHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	old[n-1] = nil
	it.idx = -1
	*h = old[:n-1]
	return it
}

func NewScheduler(cfg config.SchedulerConfig, fr frontier.Frontier, rc *ResourceController, src storage.SourceRegistry, caps TaskCapabilities) *Scheduler {
	if caps == nil {
		caps = DefaultTaskCapabilities()
	}
	if src == nil {
		src = NoopSourceRegistry{}
	}
	return &Scheduler{
		cfg:          cfg,
		fr:           fr,
		src:          src,
		rc:           rc,
		caps:         caps,
		keyIndex:     map[string]*taskItem{},
		materialized: map[string]bool{},
		active:       map[model.TaskID]*activeTask{},
		domainActive: map[string]int{},
		executedByType: map[model.TaskType]int{},
		quotaFloor:    cfg.ExplorationQuotaFloor,
		stats: master.SchedulerStats{
			GlobalCap:            cfg.MaxGlobalConcurrency,
			BrowserCapConfigured: cfg.Upgrade.BrowserMaxConcurrency,
			BrowserCap:           cfg.Upgrade.BrowserMaxConcurrency,
			HeavyCapConfigured:   cfg.MaxGlobalConcurrency,
			HeavyCapEffective:    cfg.MaxGlobalConcurrency,
			DomainActive:         map[string]int{},
		},
	}
}

var _ master.Scheduler = (*Scheduler)(nil)

func (s *Scheduler) Submit(t model.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.TaskKey == "" {
		t.TaskKey = fmt.Sprintf("task:%s", t.ID)
	}
	key := sessionTaskKey(t.SessionID, t.TaskKey)
	if _, exists := s.keyIndex[key]; exists {
		return nil
	}
	it := &taskItem{task: t}
	heap.Push(&s.pending, it)
	s.keyIndex[key] = it
	return nil
}

func (s *Scheduler) RegisterManager(m manager.Manager, caps []manager.Capability) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registered = append(s.registered, registeredManager{m: m, caps: caps})
	return nil
}

func (s *Scheduler) Admit(now time.Time) (model.Task, manager.Manager, manager.WorkerSlot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	caps := s.rc.EffectiveCaps(now)
	s.stats.CPULoad = caps.CPU
	s.stats.RAMLoad = caps.RAM
	s.stats.GlobalCap = caps.Global
	s.stats.BrowserCap = caps.Browser
	s.stats.BrowserCapConfigured = caps.BrowserConfigured
	s.stats.HeavyCapEffective = caps.Heavy
	s.stats.HeavyCapConfigured = caps.HeavyConfigured

	s.drainFrontier(caps)

	snapshot := make([]*taskItem, len(s.pending))
	copy(snapshot, s.pending)
	pick := s.selectTask(snapshot, now, caps)
	if pick == nil {
		s.refreshDomainActiveLocked()
		return model.Task{}, nil, manager.WorkerSlot{}, false
	}
	t := pick.task
	_, mgr, _ := s.assignManager(t.Type)
	s.removePending(pick)
	t.State = model.TaskStateRunning
	t.StartedAt = now
	s.active[t.ID] = &activeTask{task: t, manager: mgr, slot: manager.WorkerSlot{ID: s.nextSlot()}}
	s.domainActive[t.SourceTarget]++
	if t.Type == model.TaskTypeFetchBrowser {
		s.browserActive++
	}
	if t.EstimatedCost >= 3 {
		s.heavyActive++
	}
	s.executedByType[t.Type]++
	return t, mgr, s.active[t.ID].slot, true
}

// selectTask picks the task to admit from a priority-ordered snapshot of the
// pending heap (heap order == taskLess: higher Priority first, then lower cost,
// then earlier created). The first eligible entry is the naturally highest-
// priority admissible task. When the exploration quota floor is not yet met,
// it is overridden in favour of the highest-priority *eligible exploration*
// task (DISCOVER/FETCH_HTTP), so crawl breadth is guaranteed. A quota override
// that passes over an otherwise-admissible higher-priority non-exploration task
// is recorded as a priority inversion for reporting.
func (s *Scheduler) selectTask(snapshot []*taskItem, now time.Time, caps Caps) *taskItem {
	var top *taskItem
	var explore *taskItem
	for _, it := range snapshot {
		if !s.eligible(it.task, now, caps) {
			continue
		}
		if top == nil {
			top = it
		}
		if explore == nil && explorationTaskTypes[it.task.Type] {
			explore = it
		}
	}
	if top == nil {
		return nil
	}
	if s.quotaFloor > 0 && !s.explorationQuotaMet() && explore != nil {
		if explore != top {
			s.inversions++
		}
		return explore
	}
	return top
}

// explorationQuotaMet reports whether the DISCOVER+FETCH_HTTP share of tasks
// admitted so far is already at or above the configured floor. When false (and
// the floor is enabled) selection is biased toward exploration tasks.
func (s *Scheduler) explorationQuotaMet() bool {
	total, explore, _ := s.quotaCounts()
	if total == 0 {
		return false
	}
	return float64(explore)/float64(total) >= s.quotaFloor
}

func (s *Scheduler) quotaCounts() (total, explore, inversions int) {
	for typ, n := range s.executedByType {
		total += n
		if explorationTaskTypes[typ] {
			explore += n
		}
	}
	inversions = s.inversions
	return total, explore, inversions
}

// QuotaStats exposes the exploration-quota accounting for tests and reporting.
// total = executed tasks, explore = DISCOVER+FETCH_HTTP executed, inversions =
// quota-driven priority inversions. Must be called without holding mu by an
// external reader; it takes the lock itself.
func (s *Scheduler) QuotaStats() (total, explore, inversions int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.quotaCounts()
}

func (s *Scheduler) Release(slot manager.WorkerSlot, taskID model.TaskID, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, exists := s.active[taskID]
	if !exists {
		at, exists = s.lookupBySlot(slot)
		if !exists {
			return
		}
	}
	if at.slot != slot {
		return
	}
	delete(s.active, taskID)
	s.domainActive[at.task.SourceTarget] = max(0, s.domainActive[at.task.SourceTarget]-1)
	if at.task.Type == model.TaskTypeFetchBrowser {
		s.browserActive = max(0, s.browserActive-1)
	}
	if at.task.EstimatedCost >= 3 {
		s.heavyActive = max(0, s.heavyActive-1)
	}
	_ = ok
}

func (s *Scheduler) Stats() master.SchedulerStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.stats
	out.Active = len(s.active)
	out.Queued = len(s.pending)
	out.BrowserActive = s.browserActive
	out.HeavyActive = s.heavyActive
	if len(s.domainActive) > 0 {
		out.DomainActive = make(map[string]int, len(s.domainActive))
		for k, v := range s.domainActive {
			out.DomainActive[k] = v
		}
	} else {
		out.DomainActive = map[string]int{}
	}
	return out
}

func (s *Scheduler) drainFrontier(caps Caps) {
	if s.fr == nil {
		return
	}
	avail := caps.Global - len(s.active)
	if avail <= 0 {
		return
	}
	batch := min(avail, frontierDrainBatchMax)
	for _, c := range s.fr.Next(batch) {
		if c.URL == nil {
			continue
		}
		if s.materialized[frontier.CanonicalKey(c.URL)] {
			continue
		}
		t := materializeTask(c, s.cfg)
		if t.TaskKey == "" {
			t.TaskKey = fmt.Sprintf("frontier:%s:%s", c.SessionID, frontier.CanonicalKey(c.URL))
		}
		key := sessionTaskKey(t.SessionID, t.TaskKey)
		if _, exists := s.keyIndex[key]; exists {
			continue
		}
		it := &taskItem{task: t}
		heap.Push(&s.pending, it)
		s.keyIndex[key] = it
		s.materialized[frontier.CanonicalKey(c.URL)] = true
	}
}

func (s *Scheduler) eligible(t model.Task, now time.Time, caps Caps) bool {
	if t.BackoffUntil.After(now) {
		return false
	}
	if len(s.active) >= caps.Global {
		return false
	}
	if t.SourceTarget != "" && s.domainActive[t.SourceTarget] >= s.domainCap(t.SourceTarget) {
		return false
	}
	if t.Type == model.TaskTypeFetchBrowser && s.browserActive >= caps.Browser {
		return false
	}
	if t.EstimatedCost >= 3 && s.heavyActive >= caps.Heavy {
		return false
	}
	_, _, ok := s.assignManager(t.Type)
	return ok
}

func (s *Scheduler) assignManager(tt model.TaskType) (manager.Capability, manager.Manager, bool) {
	cap, ok := s.caps.CapFor(tt)
	if !ok {
		return "", nil, false
	}
	for i := range s.registered {
		rm := &s.registered[i]
		for _, c := range rm.caps {
			if c == cap {
				return cap, rm.m, true
			}
		}
	}
	return cap, nil, false
}

func (s *Scheduler) domainCap(domain string) int {
	return perDomainLimit(domain, s.cfg, s.src)
}

func (s *Scheduler) removePending(it *taskItem) {
	idx := it.idx
	if idx >= 0 && idx < len(s.pending) {
		heap.Remove(&s.pending, idx)
	}
	if it.task.TaskKey != "" {
		delete(s.keyIndex, sessionTaskKey(it.task.SessionID, it.task.TaskKey))
	}
}

func (s *Scheduler) nextSlot() string {
	s.slotSeq++
	return fmt.Sprintf("slot-%d", s.slotSeq)
}

func (s *Scheduler) lookupBySlot(slot manager.WorkerSlot) (*activeTask, bool) {
	for _, at := range s.active {
		if at.slot == slot {
			return at, true
		}
	}
	return nil, false
}

func (s *Scheduler) refreshDomainActiveLocked() {
	s.stats.DomainActive = make(map[string]int, len(s.domainActive))
	for k, v := range s.domainActive {
		s.stats.DomainActive[k] = v
	}
}

func sessionTaskKey(session model.SessionID, taskKey string) string {
	return string(session) + ":" + taskKey
}

func taskLess(a, b model.Task) bool {
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	if a.EstimatedCost != b.EstimatedCost {
		return a.EstimatedCost < b.EstimatedCost
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return string(a.ID) < string(b.ID)
}

func materializeTask(c frontier.URLCandidate, cfg config.SchedulerConfig) model.Task {
	domain := c.Domain
	if domain == "" && c.URL != nil {
		domain = strings.ToLower(c.URL.Hostname())
	}
	prio := c.PriorityHint
	if prio <= 0 {
		prio = defaultPriorityHint
	}
	if prio > 1000 {
		prio = 1000
	}
	cost := cfg.CostModel[model.TaskTypeFetchHTTP]
	if cost <= 0 {
		cost = 1
	}
	parent := c.ParentTaskID
	return model.Task{
		ID:              model.NewTaskID(),
		SessionID:       c.SessionID,
		Type:            model.TaskTypeFetchHTTP,
		State:           model.TaskStateReady,
		Priority:        prio,
		SourceClass:     c.SourceClass,
		SourceTarget:    domain,
		URL:             c.URL,
		ParentTaskID:    parent,
		CreatedByTaskID: parent,
		DiscoveredFromURL: c.DiscoveredFromURL,
		CrawlDepth:      c.CrawlDepth,
		TaskDepth:       c.TaskDepth,
		EstimatedCost:   cost,
		TaskKey:         fmt.Sprintf("frontier:%s:%s:%s", c.SessionID, domain, frontier.CanonicalKey(c.URL)),
	}
}
