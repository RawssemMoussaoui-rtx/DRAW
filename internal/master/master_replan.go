package master

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"draw/internal/model"
	"draw/internal/storage"
)

const (
	ExternalReplanCooldown       = 15 * time.Second
	externalReplanChannelTimeout = 5 * time.Second
	externalReplanResponseTimeout = 30 * time.Second
	defaultSourceQualityScore    = 0.3
)

const (
	ExternalReasonCodeCoverageGap    = "COVERAGE_GAP"
	ExternalReasonCodeManualRecovery = "MANUAL_RECOVERY"
)

const (
	RejectionAgentBudgetExhausted  = "AGENT_BUDGET_EXHAUSTED"
	RejectionSessionNotActive      = "SESSION_NOT_ACTIVE"
	RejectionCooldownActive        = "COOLDOWN_ACTIVE"
	RejectionInvalidReasonCode     = "INVALID_REASON_CODE"
	RejectionInvalidTargetScope    = "INVALID_TARGET_SCOPE"
	RejectionDuplicateProposal     = "DUPLICATE_PROPOSAL"
	RejectionCircuitBreakerTripped = "CIRCUIT_BREAKER_TRIPPED"
)

type externalReplanRequest struct {
	sessionID   model.SessionID
	trigger     ReplanTrigger
	reasonCode  string
	targetScope string
	resp        chan externalReplanResponse
}

type externalReplanResponse struct {
	accepted        bool
	rejectionReason string
	budgetAfter     map[string]int
}

func normalizeScope(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func semanticHash(reasonCode, normalizedTargetScope string) string {
	h := sha256.Sum256([]byte(reasonCode + "|" + normalizedTargetScope))
	return hex.EncodeToString(h[:])
}

func isValidExternalReasonCode(code string) bool {
	return code == ExternalReasonCodeCoverageGap || code == ExternalReasonCodeManualRecovery
}

func mapReasonCodeToTrigger(reasonCode, normalizedTargetScope string) ReplanTrigger {
	switch reasonCode {
	case ExternalReasonCodeCoverageGap:
		return ReplanTrigger{
			Kind:         ReplanTriggerCoverageGap,
			MissingTopic: normalizedTargetScope,
		}
	case ExternalReasonCodeManualRecovery:
		return ReplanTrigger{
			Kind:         ReplanTriggerManual,
			MissingTopic: normalizedTargetScope,
		}
	default:
		return ReplanTrigger{}
	}
}

func budgetAfterMap(used, maxReplans int) map[string]int {
	return map[string]int{
		"agent_replans_used":      used,
		"agent_replans_remaining": maxReplans - used,
	}
}

// averageEvidenceQuality computes the mean SourceProfile.QualityScore across
// all evidence in the session, reusing the existing pipeline quality metric
// (SourceRegistry.Lookup -> QualityScore). When the registry or evidence
// store is unconfigured, returns 0.
func (m *Master) averageEvidenceQuality(sid model.SessionID) float64 {
	if m.es == nil {
		return 0
	}
	evs := m.es.Query(storage.EvidenceFilter{SessionID: sid})
	if len(evs) == 0 {
		return 0
	}
	sum := 0.0
	for _, ev := range evs {
		q := defaultSourceQualityScore
		if m.reg != nil {
			if p, ok := m.reg.Lookup(string(ev.SourceID)); ok && p != nil {
				q = p.QualityScore
			}
		}
		sum += q
	}
	return sum / float64(len(evs))
}

// validateTargetScope checks that the normalized scope matches an existing
// topic or entity in the current research state or evidence.
func (m *Master) validateTargetScope(state *ResearchState, normalizedScope string) bool {
	if state == nil || state.Session == nil {
		return false
	}
	intent := state.Session.Intent
	if intent.Entity != "" {
		if normalizedScope == normalizeScope(intent.Entity) {
			return true
		}
	}
	for _, t := range intent.Topics {
		if normalizedScope == normalizeScope(t) {
			return true
		}
	}
	if m.es != nil {
		evs := m.es.Query(storage.EvidenceFilter{SessionID: state.Session.ID, Topic: normalizedScope})
		if len(evs) > 0 {
			return true
		}
		// Fallback: case-insensitive check across all session evidence,
		// since the store's Topic filter uses exact (case-sensitive) matching.
		allEvs := m.es.Query(storage.EvidenceFilter{SessionID: state.Session.ID})
		for _, ev := range allEvs {
			if normalizeScope(ev.Topic) == normalizedScope {
				return true
			}
		}
	}
	return false
}

func (m *Master) logExternalReplan(now time.Time, sid model.SessionID, reasonCode, targetScope, hash string, accepted bool, rejectionReason string) {
	level := "INFO"
	if !accepted {
		level = "WARN"
	}
	var msg string
	if accepted {
		msg = fmt.Sprintf("external_replan accepted: reason_code=%s target_scope=%s hash=%s", reasonCode, targetScope, hash)
	} else {
		msg = fmt.Sprintf("external_replan rejected: reason_code=%s target_scope=%s hash=%s rejection=%s", reasonCode, targetScope, hash, rejectionReason)
	}
	m.emitEvent(Event{
		SessionID: sid,
		Kind:      "external_replan",
		Level:     level,
		Message:   msg,
		TS:        now,
	})
}

// rejectExternalReplan is the single exit path for every rejection after the
// guard/nil-state check. It resets the circuit-breaker consecutive-accepted
// streak (a rejection breaks "consecutive accepted"), logs the rejection,
// and returns a pre-populated response. The caller is responsible for
// releasing m.mu and sending the response on req.resp.
func (m *Master) rejectExternalReplan(req externalReplanRequest, sid model.SessionID, hash, reason string, now time.Time, state *ResearchState) externalReplanResponse {
	m.circuitBreakerConsecutiveAccepted = 0
	m.logExternalReplan(now, sid, req.reasonCode, req.targetScope, hash, false, reason)
	return externalReplanResponse{
		accepted:        false,
		rejectionReason: reason,
		budgetAfter:     budgetAfterMap(state.AgentReplanCount, state.AgentMaxReplans),
	}
}

// TriggerExternalReplan is the MasterAPI entry point for external replan
// requests (Phase D). It constructs a command and submits it onto replanCh,
// which is serviced by the Run loop. This method does NOT mutate Master state
// directly — all state changes happen inside handleExternalReplan in the Run
// goroutine. The method blocks on the response channel for the 8-layer gate
// verdict.
func (m *Master) TriggerExternalReplan(reasonCode, targetScope string) (bool, string, map[string]int) {
	m.mu.Lock()
	sid := model.SessionID("")
	if m.state != nil && m.state.Session != nil {
		sid = m.state.Session.ID
	}
	m.mu.Unlock()

	normalizedScope := normalizeScope(targetScope)
	trigger := mapReasonCodeToTrigger(reasonCode, normalizedScope)

	req := externalReplanRequest{
		sessionID:   sid,
		trigger:     trigger,
		reasonCode:  reasonCode,
		targetScope: targetScope,
		resp:        make(chan externalReplanResponse, 1),
	}

	select {
	case m.replanCh <- req:
	case <-time.After(externalReplanChannelTimeout):
		return false, "REPLAN_CHANNEL_TIMEOUT", nil
	}

	select {
	case resp := <-req.resp:
		return resp.accepted, resp.rejectionReason, resp.budgetAfter
	case <-time.After(externalReplanResponseTimeout):
		return false, "REPLAN_RESPONSE_TIMEOUT", nil
	}
}

// handleExternalReplan evaluates the 8-layer protection gate and, if all
// layers pass, merges the replan delta via the existing mergeDelta path.
// Runs exclusively in the Run goroutine.
func (m *Master) handleExternalReplan(req externalReplanRequest) {
	now := time.Now().UTC()
	normalizedScope := normalizeScope(req.targetScope)
	hash := semanticHash(req.reasonCode, normalizedScope)

	m.mu.Lock()

	// Guard: no active session at all.
	if m.state == nil || m.state.Session == nil {
		m.logExternalReplan(now, req.sessionID, req.reasonCode, normalizedScope, hash, false, RejectionSessionNotActive)
		m.mu.Unlock()
		req.resp <- externalReplanResponse{
			accepted:        false,
			rejectionReason: RejectionSessionNotActive,
		}
		return
	}

	state := m.state
	sid := state.Session.ID

	// Layer 1: Quota check (before deduction).
	if state.AgentReplanCount >= state.AgentMaxReplans {
		resp := m.rejectExternalReplan(req, sid, hash, RejectionAgentBudgetExhausted, now, state)
		m.mu.Unlock()
		req.resp <- resp
		return
	}

	// Layer 2: Non-refundable deduction (before evaluating layers 3-8).
	state.AgentReplanCount++
	state.UpdatedAt = now

	// Layer 3: Active session status check.
	if state.Terminal || m.StopPending {
		resp := m.rejectExternalReplan(req, sid, hash, RejectionSessionNotActive, now, state)
		m.mu.Unlock()
		req.resp <- resp
		return
	}

	// Layer 4: Cooldown enforcement (15s minimum between accepted proposals).
	if !m.lastAcceptedReplanAt.IsZero() && now.Sub(m.lastAcceptedReplanAt) <= ExternalReplanCooldown {
		resp := m.rejectExternalReplan(req, sid, hash, RejectionCooldownActive, now, state)
		m.mu.Unlock()
		req.resp <- resp
		return
	}

	// Layer 5: Enumerated reason_code.
	if !isValidExternalReasonCode(req.reasonCode) {
		resp := m.rejectExternalReplan(req, sid, hash, RejectionInvalidReasonCode, now, state)
		m.mu.Unlock()
		req.resp <- resp
		return
	}

	// Layer 6: target_scope validation (must match existing topic/entity in evidence or intent).
	if !m.validateTargetScope(state, normalizedScope) {
		resp := m.rejectExternalReplan(req, sid, hash, RejectionInvalidTargetScope, now, state)
		m.mu.Unlock()
		req.resp <- resp
		return
	}

	// Layer 7: Semantic hash dedup (compare against ALL prior proposals in session).
	if m.proposalHashes[hash] {
		resp := m.rejectExternalReplan(req, sid, hash, RejectionDuplicateProposal, now, state)
		m.mu.Unlock()
		req.resp <- resp
		return
	}

	// Layer 8: Circuit breaker (permanent for the session once tripped).
	if m.circuitBreakerTripped {
		m.logExternalReplan(now, sid, req.reasonCode, req.targetScope, hash, false, RejectionCircuitBreakerTripped)
		resp := externalReplanResponse{
			accepted:        false,
			rejectionReason: RejectionCircuitBreakerTripped,
			budgetAfter:     budgetAfterMap(state.AgentReplanCount, state.AgentMaxReplans),
		}
		m.mu.Unlock()
		req.resp <- resp
		return
	}

	// --- All 8 layers passed: accept the proposal ---

	m.proposalHashes[hash] = true

	quality := m.averageEvidenceQuality(sid)

	// Circuit breaker quality-drift tracking (side-effect of acceptance only).
	m.circuitBreakerConsecutiveAccepted++
	if m.circuitBreakerConsecutiveAccepted >= 2 {
		if quality < m.lastAcceptedReplanQuality {
			m.circuitBreakerTripped = true
		} else {
			// Quality did not trend downward; reset streak to 1
			// (current proposal becomes the new baseline).
			m.circuitBreakerConsecutiveAccepted = 1
		}
	}
	m.lastAcceptedReplanQuality = quality
	m.lastAcceptedReplanAt = now
	state.UpdatedAt = now

	m.logExternalReplan(now, sid, req.reasonCode, req.targetScope, hash, true, "")

	// Build replan trigger from reason_code and merge the delta
	// through the existing (unchanged) mergeDelta path.
	trigger := mapReasonCodeToTrigger(req.reasonCode, normalizedScope)
	delta := m.planner.Replan(state.Clone(), trigger)
	_ = m.mergeDelta(delta)

	resp := externalReplanResponse{
		accepted:        true,
		rejectionReason: "",
		budgetAfter:     budgetAfterMap(state.AgentReplanCount, state.AgentMaxReplans),
	}
	m.mu.Unlock()
	req.resp <- resp
}
