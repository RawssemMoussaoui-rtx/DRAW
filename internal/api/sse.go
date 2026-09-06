package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"draw/internal/master"
	"draw/internal/model"
	"draw/internal/storage"
)

// StateProvider provides snapshots of the master research state.
// *master.Master satisfies this interface via its State() method.
type StateProvider interface {
	State() master.ResearchState
}

// PhaseEvent is emitted when a plan phase transitions between
// "started", "running", and "completed" states.
type PhaseEvent struct {
	Phase  string `json:"phase"`
	Status string `json:"status"`
}

// ProgressEvent is emitted on every poll to report current progress.
type ProgressEvent struct {
	Progress       float64 `json:"progress"`
	Phase          string  `json:"phase"`
	Evidence       int     `json:"evidence"`
	Contradictions int     `json:"contradictions"`
	BudgetUsed     int     `json:"budget_used"`
	BudgetTotal    int     `json:"budget_total"`
}

// SSEHandler streams research progress over Server-Sent Events.
type SSEHandler struct {
	sp          StateProvider
	buildResult func() model.ResultEnvelope
	es          storage.EventStore
	sid         model.SessionID
	interval    time.Duration
}

// NewSSEHandler creates an SSE handler that polls the StateProvider every
// 250ms by default. The buildResult callback is invoked when the research
// reaches a terminal state to produce the final report event.
func NewSSEHandler(sp StateProvider, buildResult func() model.ResultEnvelope) *SSEHandler {
	return &SSEHandler{
		sp:          sp,
		buildResult: buildResult,
		interval:    250 * time.Millisecond,
	}
}

// planPhaseName derives the human-readable phase name for the given state.
// It prefers the plan's phase name, falls back to the canonical order slice,
// and finally uses a synthetic "phase_N" label.
func planPhaseName(rs master.ResearchState) string {
	if rs.PhaseIndex >= 0 && rs.Plan != nil && rs.PhaseIndex < len(rs.Plan.Phases) {
		return rs.Plan.Phases[rs.PhaseIndex].Name
	}
	if rs.PhaseIndex >= 0 && rs.PhaseIndex < len(master.PlanPhaseOrder) {
		return master.PlanPhaseOrder[rs.PhaseIndex]
	}
	return fmt.Sprintf("phase_%d", rs.PhaseIndex)
}

// Handler returns an http.HandlerFunc that streams SSE events to the client.
// Each connection independently tracks phase progress. The handler polls the
// StateProvider at the configured interval until the request is cancelled or
// the research state becomes terminal. When an EventStore and session id are
// configured on the receiver, historical events are replayed before the first
// poll tick.
func (h *SSEHandler) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var replay []storage.Event
		if h.es != nil {
			replay = h.es.Load(h.sid, 0, 1000)
		}
		h.serveSSE(w, r, replay)
	}
}

// NewEventSDEHandler creates an SSE handler variant that first replays
// historical events from the given EventStore (Load(sid, 0, 1000)), then
// performs the same live polling as SSEHandler.Handler. When es is nil the
// behaviour is identical to SSEHandler.Handler (no replay).
func NewEventSDEHandler(sp StateProvider, es storage.EventStore, buildResult func() model.ResultEnvelope, sid model.SessionID) *SSEHandler {
	return &SSEHandler{
		sp:          sp,
		buildResult: buildResult,
		es:          es,
		sid:         sid,
		interval:    250 * time.Millisecond,
	}
}

// serveSSE is the shared SSE serving logic. replay is an optional slice of
// historical storage.Event values emitted (as `event` frames) before the first
// poll tick; nil means no replay and the behaviour is identical to Handler().
func (h *SSEHandler) serveSSE(w http.ResponseWriter, r *http.Request, replay []storage.Event) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	defer func() {
		if rec := recover(); rec != nil {
			data, merr := json.Marshal(map[string]string{"error": fmt.Sprintf("%v", rec)})
			if merr != nil {
				data = []byte(`{"error":"marshal_failed"}`)
			}
			fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}()

	// Emit historical events first (replay), if any were loaded.
	for i := range replay {
		writeSSE(w, flusher, "event", replay[i])
	}

	lastPhaseIdx := -1
	runningPending := false
	completedEmitted := false

	for {
		select {
		case <-r.Context().Done():
			return
		default:
		}

		rs := h.sp.State()
		phaseName := planPhaseName(rs)

		phaseChanged := lastPhaseIdx == -1 || rs.PhaseIndex != lastPhaseIdx
		if phaseChanged {
			writeSSE(w, flusher, "phase", PhaseEvent{Phase: phaseName, Status: "started"})
			lastPhaseIdx = rs.PhaseIndex
			runningPending = true
			completedEmitted = false
		}
		if !phaseChanged && runningPending {
			writeSSE(w, flusher, "phase", PhaseEvent{Phase: phaseName, Status: "running"})
			runningPending = false
		}
		if rs.PhaseCompleted[phaseName] && !completedEmitted {
			writeSSE(w, flusher, "phase", PhaseEvent{Phase: phaseName, Status: "completed"})
			completedEmitted = true
		}

		completedCount := 0
		for _, v := range rs.PhaseCompleted {
			if v {
				completedCount++
			}
		}
		completeness := 0.0
		if len(master.PlanPhaseOrder) > 0 {
			completeness = float64(completedCount) / float64(len(master.PlanPhaseOrder))
		}
		writeSSE(w, flusher, "progress", ProgressEvent{
			Progress:       completeness,
			Phase:          phaseName,
			Evidence:       rs.EvidenceCount,
			Contradictions: rs.Evidence.Contradictions,
			BudgetUsed:     rs.BudgetUsed,
			BudgetTotal:    rs.BudgetTotal,
		})

		if rs.Terminal {
			if h.buildResult == nil {
				writeSSE(w, flusher, "error", map[string]string{"error": "no result builder"})
			} else {
				writeSSE(w, flusher, "report", h.buildResult())
			}
			return
		}

		select {
		case <-r.Context().Done():
			return
		case <-time.After(h.interval):
		}
	}
}

// writeSSE writes a single SSE event frame and flushes if possible.
func writeSSE(w http.ResponseWriter, flusher http.Flusher, event string, payload interface{}) {
	data, err := json.Marshal(payload)
	if err != nil {
		data = []byte(`{"error":"marshal_failed"}`)
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	if flusher != nil {
		flusher.Flush()
	}
}
