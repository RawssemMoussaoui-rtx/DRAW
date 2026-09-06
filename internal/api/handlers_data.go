package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"draw/internal/master"
	"draw/internal/model"
	"draw/internal/storage"
)

type stateResponse struct {
	Phase          string  `json:"phase"`
	Progress       float64 `json:"progress"`
	Evidence       int     `json:"evidence"`
	Contradictions int     `json:"contradictions"`
	BudgetUsed     int     `json:"budget_used"`
	BudgetTotal    int     `json:"budget_total"`
	Terminal       bool    `json:"terminal"`
	TerminalReason string  `json:"terminal_reason"`
}

func toStateResponse(rs master.ResearchState) stateResponse {
	phase := fmt.Sprintf("phase_%d", rs.PhaseIndex)
	if rs.PhaseIndex >= 0 && rs.PhaseIndex < len(master.PlanPhaseOrder) {
		phase = master.PlanPhaseOrder[rs.PhaseIndex]
	} else if rs.Plan != nil && rs.PhaseIndex >= 0 && rs.PhaseIndex < len(rs.Plan.Phases) {
		phase = rs.Plan.Phases[rs.PhaseIndex].Name
	}
	completed := 0
	for _, v := range rs.PhaseCompleted {
		if v {
			completed++
		}
	}
	progress := 0.0
	if len(master.PlanPhaseOrder) > 0 {
		progress = float64(completed) / float64(len(master.PlanPhaseOrder))
	}
	return stateResponse{
		Phase:          phase,
		Progress:       progress,
		Evidence:       rs.EvidenceCount,
		Contradictions: rs.Evidence.Contradictions,
		BudgetUsed:     rs.BudgetUsed,
		BudgetTotal:    rs.BudgetTotal,
		Terminal:       rs.Terminal,
		TerminalReason: rs.TerminalReason,
	}
}

func (s *apiServer) getResult() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.requireActive(w, r); !ok {
			return
		}
		env := s.deps.ReportBuilder.Build(s.deps.Master.State())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(env)
	}
}

func (s *apiServer) getEvidence() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := s.requireActive(w, r)
		if !ok {
			return
		}
		evs := s.deps.EvidenceStore.Query(storage.EvidenceFilter{SessionID: id})
		if evs == nil {
			evs = []model.Evidence{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(evs)
	}
}

func (s *apiServer) getSources() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := s.requireActive(w, r)
		if !ok {
			return
		}
		evs := s.deps.EvidenceStore.Query(storage.EvidenceFilter{SessionID: id})
		seen := map[string]bool{}
		var out []model.ReportSource
		for _, ev := range evs {
			d := string(ev.SourceID)
			if d == "" || seen[d] {
				continue
			}
			seen[d] = true
			var rs model.ReportSource
			if profile, ok := s.deps.SourceRegistry.Lookup(d); ok && profile != nil {
				rs = model.ReportSource{
					Name:    profile.Domain,
					URL:     "https://" + d,
					Class:   profile.Class,
					Quality: profile.QualityScore,
				}
			} else {
				rs = model.ReportSource{
					Name:    d,
					URL:     "https://" + d,
					Class:   model.SourceClassUnknown,
					Quality: 0.3,
				}
			}
			out = append(out, rs)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		if out == nil {
			out = []model.ReportSource{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

func (s *apiServer) getEvents() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := model.SessionID(r.PathValue("id"))
		NewEventSDEHandler(s.deps.Master, s.deps.EventStore, s.buildResult, id).Handler().ServeHTTP(w, r)
	}
}
