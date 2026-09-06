package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"draw/internal/master"
	"draw/internal/model"
	"draw/internal/storage"
)

// --- Test fakes ---

// fakeEventStore is a minimal in-memory EventStore for testing replay.
type fakeEventStore struct {
	events []storage.Event
}

func (s *fakeEventStore) Append(storage.Event) error { return nil }

func (s *fakeEventStore) Load(_ model.SessionID, _ int64, _ int) []storage.Event {
	return s.events
}

func (s *fakeEventStore) LoadSince(_ model.SessionID, _ time.Time) []storage.Event {
	return s.events
}

var _ storage.EventStore = (*fakeEventStore)(nil)

// chanStateProvider returns scripted states one at a time from a buffered
// channel. State() blocks until a value is available, giving the test full
// control over the sequence without timing-based races.
type chanStateProvider struct {
	ch chan master.ResearchState
}

func (f *chanStateProvider) State() master.ResearchState {
	return <-f.ch
}

// constStateProvider returns the same non-terminal state every call.
// State() never blocks, which lets us test client disconnect behaviour.
type constStateProvider struct {
	rs master.ResearchState
}

func (f *constStateProvider) State() master.ResearchState {
	return f.rs
}

// --- SSE parsing helpers ---

type sseEvent struct {
	Event string
	Data  string
}

// parseSSE parses an SSE byte stream into a slice of events. Each event
// block is terminated by a blank line.
func parseSSE(body string) []sseEvent {
	var events []sseEvent
	var current *sseEvent
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			if current != nil {
				events = append(events, *current)
				current = nil
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			if current != nil {
				events = append(events, *current)
			}
			current = &sseEvent{
				Event: strings.TrimSpace(strings.TrimPrefix(line, "event:")),
			}
		} else if strings.HasPrefix(line, "data: ") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			if current == nil {
				current = &sseEvent{}
			}
			current.Data = data
		}
	}
	if current != nil {
		events = append(events, *current)
	}
	return events
}

func filterEvents(events []sseEvent, eventType string) []sseEvent {
	var result []sseEvent
	for _, e := range events {
		if e.Event == eventType {
			result = append(result, e)
		}
	}
	return result
}

func findPhaseEvent(t *testing.T, phases []sseEvent, phase, status string) bool {
	t.Helper()
	for _, p := range phases {
		var pe PhaseEvent
		if err := json.Unmarshal([]byte(p.Data), &pe); err != nil {
			continue
		}
		if pe.Phase == phase && pe.Status == status {
			return true
		}
	}
	return false
}

func fixedBuildResult() model.ResultEnvelope {
	return model.ResultEnvelope{
		Status:    "completed",
		SessionID: model.SessionID("ses_test"),
	}
}

// --- Tests ---

func TestSSE_StreamsPhaseProgressReport(t *testing.T) {
	states := []master.ResearchState{
		{PhaseIndex: 0}, // started: discovery
		{PhaseIndex: 0}, // running: discovery
		{PhaseIndex: 0, PhaseCompleted: map[string]bool{"discovery": true}},                 // completed: discovery
		{PhaseIndex: 1, PhaseCompleted: map[string]bool{"discovery": true}},                 // started: primary_retrieval
		{PhaseIndex: 1, PhaseCompleted: map[string]bool{"discovery": true}},                 // running: primary_retrieval
		{PhaseIndex: 1, PhaseCompleted: map[string]bool{"discovery": true}, Terminal: true}, // report
	}

	ch := make(chan master.ResearchState, len(states))
	for _, s := range states {
		ch <- s
	}

	fake := &chanStateProvider{ch: ch}
	h := NewSSEHandler(fake, fixedBuildResult)
	h.interval = time.Millisecond

	server := httptest.NewServer(h.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %q", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	events := parseSSE(string(body))
	if len(events) == 0 {
		t.Fatal("expected at least one SSE event")
	}

	phases := filterEvents(events, "phase")

	if !findPhaseEvent(t, phases, "discovery", "started") {
		t.Error("missing phase started for discovery")
	}
	if !findPhaseEvent(t, phases, "discovery", "running") {
		t.Error("missing phase running for discovery")
	}
	if !findPhaseEvent(t, phases, "discovery", "completed") {
		t.Error("missing phase completed for discovery")
	}
	if !findPhaseEvent(t, phases, "primary_retrieval", "started") {
		t.Error("missing phase started for primary_retrieval")
	}
	if !findPhaseEvent(t, phases, "primary_retrieval", "running") {
		t.Error("missing phase running for primary_retrieval")
	}

	// Verify phase event ordering: started → running → completed for discovery,
	// then started → running for primary_retrieval.
	expectedPhaseOrder := []struct {
		phase  string
		status string
	}{
		{"discovery", "started"},
		{"discovery", "running"},
		{"discovery", "completed"},
		{"primary_retrieval", "started"},
		{"primary_retrieval", "running"},
	}
	phaseIdx := 0
	for _, e := range events {
		if e.Event != "phase" {
			continue
		}
		var pe PhaseEvent
		if err := json.Unmarshal([]byte(e.Data), &pe); err != nil {
			t.Fatalf("unmarshal phase event: %v", err)
		}
		if phaseIdx < len(expectedPhaseOrder) {
			exp := expectedPhaseOrder[phaseIdx]
			if pe.Phase == exp.phase && pe.Status == exp.status {
				phaseIdx++
			}
		}
	}
	if phaseIdx != len(expectedPhaseOrder) {
		t.Errorf("phase events not in expected order: matched %d of %d", phaseIdx, len(expectedPhaseOrder))
	}

	progresses := filterEvents(events, "progress")
	if len(progresses) == 0 {
		t.Error("expected progress events")
	}

	// Verify progress fields
	for _, p := range progresses {
		var pe ProgressEvent
		if err := json.Unmarshal([]byte(p.Data), &pe); err != nil {
			t.Fatalf("unmarshal progress event: %v", err)
		}
		if pe.Progress < 0 || pe.Progress > 1 {
			t.Errorf("progress out of range [0,1]: %v", pe.Progress)
		}
	}

	// Report event must be the last event and carry the buildResult JSON.
	last := events[len(events)-1]
	if last.Event != "report" {
		t.Fatalf("expected last event to be report, got %q", last.Event)
	}
	var env model.ResultEnvelope
	if err := json.Unmarshal([]byte(last.Data), &env); err != nil {
		t.Fatalf("unmarshal report event: %v", err)
	}
	if env.Status != "completed" {
		t.Errorf("expected report status 'completed', got %q", env.Status)
	}
}

func TestSSE_Determinism(t *testing.T) {
	states := []master.ResearchState{
		{PhaseIndex: 0},
		{PhaseIndex: 0},
		{PhaseIndex: 0, PhaseCompleted: map[string]bool{"discovery": true}},
		{PhaseIndex: 1, PhaseCompleted: map[string]bool{"discovery": true}},
		{PhaseIndex: 1, PhaseCompleted: map[string]bool{"discovery": true}, Terminal: true},
	}

	run := func() string {
		ch := make(chan master.ResearchState, len(states))
		for _, s := range states {
			ch <- s
		}
		fake := &chanStateProvider{ch: ch}
		h := NewSSEHandler(fake, fixedBuildResult)
		h.interval = time.Millisecond

		server := httptest.NewServer(h.Handler())
		defer server.Close()

		resp, err := http.Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}

	first := run()
	second := run()

	if first != second {
		t.Errorf("non-deterministic SSE output:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestSSE_TerminalClosesStream(t *testing.T) {
	ch := make(chan master.ResearchState, 1)
	ch <- master.ResearchState{PhaseIndex: 0, Terminal: true}

	fake := &chanStateProvider{ch: ch}
	h := NewSSEHandler(fake, fixedBuildResult)
	h.interval = time.Millisecond

	server := httptest.NewServer(h.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}

	events := parseSSE(string(body))
	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}

	last := events[len(events)-1]
	if last.Event != "report" {
		t.Errorf("expected last event to be report, got %q", last.Event)
	}
}

func TestSSE_ClientDisconnect(t *testing.T) {
	fake := &constStateProvider{rs: master.ResearchState{PhaseIndex: 0}}
	h := NewSSEHandler(fake, fixedBuildResult)
	h.interval = 5 * time.Millisecond

	server := httptest.NewServer(h.Handler())
	defer server.Close()

	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}

	readDone := make(chan struct{})
	go func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		close(readDone)
	}()

	// Let the handler start polling.
	time.Sleep(50 * time.Millisecond)

	// Simulate client disconnect.
	cancel()

	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for body read to complete after disconnect")
	}

	// Allow server-side goroutines to clean up.
	time.Sleep(50 * time.Millisecond)

	after := runtime.NumGoroutine()
	if diff := after - before; diff > 2 {
		t.Errorf("goroutine leak: before=%d after=%d (diff=%d)", before, after, diff)
	}
}

// TestSSE_ReplaysHistoricalEvents verifies that NewEventSDEHandler emits all
// events loaded from the EventStore as `event` SSE frames BEFORE the first live
// poll tick, and that when the EventStore is nil the handler behaves identically
// to SSEHandler.Handler (no replay frames).
func TestSSE_ReplaysHistoricalEvents(t *testing.T) {
	sid := model.SessionID("ses_replay")
	ts := time.Now().UTC()
	stored := []storage.Event{
		{ID: 1, SessionID: sid, TaskID: tidPtr("tsk_a"), Kind: "task_completed", Level: "INFO", Message: "alpha done", TS: ts},
		{ID: 2, SessionID: sid, Kind: "terminal", Level: "WARN", Message: "research_complete", TS: ts.Add(time.Millisecond)},
	}
	evStore := &fakeEventStore{events: stored}

	// StateProvider returns a terminal state immediately so the live poll
	// emits exactly one phase/progress/report set and returns.
	st := master.ResearchState{
		Session:        &model.Session{ID: sid, UserID: "u1"},
		Plan:           &model.Plan{Phases: []model.Phase{{Name: "discovery"}}},
		PhaseIndex:     0,
		Terminal:       true,
		TerminalReason: "research_complete",
	}
	sp := &constStateProvider{rs: st}

	h := NewEventSDEHandler(sp, evStore, fixedBuildResult, sid)
	h.interval = time.Millisecond

	server := httptest.NewServer(h.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}

	events := parseSSE(string(body))

	// Historical events must come first, as `event` frames.
	eventEvts := filterEvents(events, "event")
	if len(eventEvts) != len(stored) {
		t.Fatalf("expected %d historical event frames, got %d", len(stored), len(eventEvts))
	}
	for i, ee := range eventEvts {
		var se storage.Event
		if err := json.Unmarshal([]byte(ee.Data), &se); err != nil {
			t.Fatalf("unmarshal historical event %d: %v", i, err)
		}
		if se.Kind != stored[i].Kind {
			t.Errorf("event %d: Kind = %q, want %q", i, se.Kind, stored[i].Kind)
		}
		if se.Message != stored[i].Message {
			t.Errorf("event %d: Message = %q, want %q", i, se.Message, stored[i].Message)
		}
	}

	// After replay, the live poll must still emit a report (terminal).
	last := events[len(events)-1]
	if last.Event != "report" {
		t.Errorf("expected last event to be report, got %q", last.Event)
	}

	// The historical event frames must precede any phase/progress/report frames.
	firstPhaseIdx := -1
	for i, e := range events {
		if e.Event == "phase" || e.Event == "progress" || e.Event == "report" {
			firstPhaseIdx = i
			break
		}
	}
	if firstPhaseIdx < len(eventEvts) {
		t.Errorf("historical events did not all precede live-poll frames: firstLive=%d, replayCount=%d", firstPhaseIdx, len(eventEvts))
	}

	// Nil EventStore → no replay, behaves like Handler().
	hNil := NewEventSDEHandler(sp, nil, fixedBuildResult, sid)
	hNil.interval = time.Millisecond
	serverNil := httptest.NewServer(hNil.Handler())
	defer serverNil.Close()
	respNil, _ := http.Get(serverNil.URL)
	bodyNil, _ := io.ReadAll(respNil.Body)
	respNil.Body.Close()
	nilEvents := parseSSE(string(bodyNil))
	if len(filterEvents(nilEvents, "event")) != 0 {
		t.Errorf("expected zero replay event frames when EventStore is nil, got %d", len(filterEvents(nilEvents, "event")))
	}
	lastNil := nilEvents[len(nilEvents)-1]
	if lastNil.Event != "report" {
		t.Errorf("expected last nil-store event to be report, got %q", lastNil.Event)
	}
}

func tidPtr(s string) *model.TaskID {
	tid := model.TaskID(s)
	return &tid
}
