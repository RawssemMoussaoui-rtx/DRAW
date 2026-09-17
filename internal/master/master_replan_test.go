package master

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	"draw/internal/config"
	"draw/internal/model"
	"draw/internal/storage"
)

func seedReplanEvidence(t *testing.T, m *Master, sid model.SessionID, topic string) {
	t.Helper()
	ev := model.Evidence{
		SessionID:    sid,
		TaskID:       model.NewTaskID(),
		SourceID:     model.SourceID("alpha.example.com"),
		Topic:        topic,
		Claim:        "revenue",
		Value:        "100M",
		Confidence:   0.8,
		Verification: model.VerificationUnverified,
		CollectedAt:  time.Now().UTC(),
	}
	if _, err := m.es.Put(ev); err != nil {
		t.Fatalf("es.Put: %v", err)
	}
}

func newReplanMaster(t *testing.T) (*Master, *fakeScheduler) {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := storage.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	es, err := storage.NewSQLiteEvidenceStore(db)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := storage.NewSQLiteSourceRegistry(db, 0.3)
	if err != nil {
		t.Fatal(err)
	}
	sched := newFakeScheduler(&fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
		return model.TaskResult{Status: model.RetrievalStatusSuccess}
	}}, 4)
	m := NewMaster(config.Defaults(), sched,
		WithEvidenceStore(es),
		WithSourceRegistry(reg),
	)
	return m, sched
}

func submitReplanSession(t *testing.T, m *Master, entity string) model.SessionID {
	t.Helper()
	sid, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1", Query: entity, Seeds: []string{seedURL()},
	})
	if err != nil {
		t.Fatal(err)
	}
	return sid
}

func doReplan(m *Master, reasonCode, targetScope string) externalReplanResponse {
	req := externalReplanRequest{
		reasonCode:  reasonCode,
		targetScope: targetScope,
		resp:        make(chan externalReplanResponse, 1),
	}
	m.handleExternalReplan(req)
	select {
	case resp := <-req.resp:
		return resp
	case <-time.After(5 * time.Second):
		panic("timed out waiting for replan response")
	}
}

func agentReplanState(m *Master) (used, maxReplans int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == nil {
		return 0, 0
	}
	return m.state.AgentReplanCount, m.state.AgentMaxReplans
}

// ========================= Layer 1: Quota =========================

func TestReplan_Layer1_QuotaExhausted(t *testing.T) {
	m, _ := newReplanMaster(t)
	sid := submitReplanSession(t, m, "Company X")
	seedReplanEvidence(t, m, sid, "Company X")

	// Exhaust the external budget (default AgentMaxReplans = 1).
	m.mu.Lock()
	m.state.AgentReplanCount = 1
	m.state.AgentMaxReplans = 1
	m.mu.Unlock()

	resp := doReplan(m, "COVERAGE_GAP", "Company X")
	if resp.accepted {
		t.Fatal("expected rejection due to exhausted quota")
	}
	if resp.rejectionReason != RejectionAgentBudgetExhausted {
		t.Errorf("rejection reason: got %q want %q", resp.rejectionReason, RejectionAgentBudgetExhausted)
	}
	// No deduction should occur when quota is already exhausted.
	used, maxR := agentReplanState(m)
	if used != 1 {
		t.Errorf("AgentReplanCount after rejection: got %d want 1 (no deduction)", used)
	}
	if maxR != 1 {
		t.Errorf("AgentMaxReplans: got %d want 1", maxR)
	}
}

// ========================= Layer 2: Non-refundable deduction =========================

func TestReplan_Layer2_DeductionIsNonRefundable(t *testing.T) {
	m, _ := newReplanMaster(t)
	sid := submitReplanSession(t, m, "Company X")
	seedReplanEvidence(t, m, sid, "Company X")

	// A proposal rejected at layer 3 (terminal session) should still
	// have deducted at layer 2.
	m.mu.Lock()
	m.state.Terminal = true
	m.mu.Unlock()

	resp := doReplan(m, "COVERAGE_GAP", "Company X")
	if resp.accepted {
		t.Fatal("expected rejection due to terminal session")
	}
	if resp.rejectionReason != RejectionSessionNotActive {
		t.Errorf("rejection reason: got %q want %q", resp.rejectionReason, RejectionSessionNotActive)
	}
	used, _ := agentReplanState(m)
	if used != 1 {
		t.Errorf("AgentReplanCount after rejected-then-deducted: got %d want 1 (deduction not refunded)", used)
	}
}

// ========================= Layer 3: Active session =========================

func TestReplan_Layer3_SessionNotActive(t *testing.T) {
	m, _ := newReplanMaster(t)
	submitReplanSession(t, m, "Company X")

	m.mu.Lock()
	m.state.Terminal = true
	m.mu.Unlock()

	resp := doReplan(m, "COVERAGE_GAP", "Company X")
	if resp.accepted {
		t.Fatal("expected rejection for terminal session")
	}
	if resp.rejectionReason != RejectionSessionNotActive {
		t.Errorf("got %q want %q", resp.rejectionReason, RejectionSessionNotActive)
	}
}

// ========================= Layer 4: Cooldown =========================

func TestReplan_Layer4_Cooldown(t *testing.T) {
	m, _ := newReplanMaster(t)
	sid := submitReplanSession(t, m, "Company X")
	seedReplanEvidence(t, m, sid, "Company X")

	// Raise budget so two proposals can be attempted.
	m.mu.Lock()
	m.state.AgentMaxReplans = 5
	m.mu.Unlock()

	// First proposal: accepted.
	resp := doReplan(m, "COVERAGE_GAP", "company x")
	if !resp.accepted {
		t.Fatalf("first proposal should be accepted, got rejected: %s", resp.rejectionReason)
	}

	// Second proposal (different scope, different hash): rejected due to cooldown.
	resp2 := doReplan(m, "MANUAL_RECOVERY", "company y")
	if resp2.accepted {
		t.Fatal("second proposal should be rejected by cooldown")
	}
	if resp2.rejectionReason != RejectionCooldownActive {
		t.Errorf("got %q want %q", resp2.rejectionReason, RejectionCooldownActive)
	}
}

func TestReplan_Layer4_CooldownDoesNotTriggerOnRejection(t *testing.T) {
	m, _ := newReplanMaster(t)
	sid := submitReplanSession(t, m, "Company X")
	seedReplanEvidence(t, m, sid, "Company X")

	m.mu.Lock()
	m.state.AgentMaxReplans = 5
	// Set lastAcceptedReplanAt to 20s ago — cooldown expired, so layer 4 passes.
	m.lastAcceptedReplanAt = time.Now().Add(-20 * time.Second)
	m.mu.Unlock()

	// Proposal with invalid reason code: passes cooldown (layer 4),
	// rejected at layer 5 (INVALID_REASON_CODE).
	resp := doReplan(m, "BOGUS_CODE", "company y")
	if resp.accepted {
		t.Fatal("proposal with bogus reason should be rejected")
	}
	if resp.rejectionReason != RejectionInvalidReasonCode {
		t.Errorf("got %q want %q", resp.rejectionReason, RejectionInvalidReasonCode)
	}

	// Verify lastAcceptedReplanAt was NOT modified by the rejection.
	m.mu.Lock()
	if m.lastAcceptedReplanAt.After(time.Now().Add(-19 * time.Second)) {
		t.Error("rejected proposal must not modify lastAcceptedReplanAt")
	}
	// A valid proposal should still be accepted (cooldown was never re-triggered
	// by the rejection, and the original timestamp is still >15s ago).
	m.mu.Unlock()

	resp2 := doReplan(m, "COVERAGE_GAP", "company x")
	if !resp2.accepted {
		t.Fatalf("valid proposal after rejection should be accepted: %s", resp2.rejectionReason)
	}
}

// ========================= Layer 5: reason_code =========================

func TestReplan_Layer5_InvalidReasonCode(t *testing.T) {
	m, _ := newReplanMaster(t)
	sid := submitReplanSession(t, m, "Company X")
	seedReplanEvidence(t, m, sid, "Company X")

	resp := doReplan(m, "BOGUS", "Company X")
	if resp.accepted {
		t.Fatal("expected rejection for invalid reason code")
	}
	if resp.rejectionReason != RejectionInvalidReasonCode {
		t.Errorf("got %q want %q", resp.rejectionReason, RejectionInvalidReasonCode)
	}
	// Budget was still deducted.
	used, _ := agentReplanState(m)
	if used != 1 {
		t.Errorf("AgentReplanCount after rejection: got %d want 1 (deduction not refunded)", used)
	}
}

// ========================= Layer 6: target_scope =========================

func TestReplan_Layer6_InvalidTargetScope(t *testing.T) {
	m, _ := newReplanMaster(t)
	sid := submitReplanSession(t, m, "Company X")
	seedReplanEvidence(t, m, sid, "Company X")

	resp := doReplan(m, "COVERAGE_GAP", "Nonexistent Topic")
	if resp.accepted {
		t.Fatal("expected rejection for invalid target scope")
	}
	if resp.rejectionReason != RejectionInvalidTargetScope {
		t.Errorf("got %q want %q", resp.rejectionReason, RejectionInvalidTargetScope)
	}
}

func TestReplan_Layer6_TargetScopeMatchesEntity(t *testing.T) {
	m, _ := newReplanMaster(t)
	sid := submitReplanSession(t, m, "Company X")
	seedReplanEvidence(t, m, sid, "Company X")

	// "company x" (lowercase) should match the entity "Company X".
	resp := doReplan(m, "COVERAGE_GAP", "  Company X  ")
	if !resp.accepted {
		t.Fatalf("expected acceptance for scope matching entity: %s", resp.rejectionReason)
	}
}

// ========================= Layer 7: Semantic hash dedup =========================

func TestReplan_Layer7_DuplicateProposal(t *testing.T) {
	m, _ := newReplanMaster(t)
	sid := submitReplanSession(t, m, "Company X")
	seedReplanEvidence(t, m, sid, "Company X")

	// First call: accepted (uses up the budget of 1).
	resp := doReplan(m, "COVERAGE_GAP", "company x")
	if !resp.accepted {
		t.Fatalf("first proposal should be accepted: %s", resp.rejectionReason)
	}

	// Exhaust budget so we can test dedup on a hypothetical second call.
	m.mu.Lock()
	m.state.AgentReplanCount = 0
	m.state.AgentMaxReplans = 5 // allow multiple proposals for dedup testing
	m.lastAcceptedReplanAt = time.Time{} // clear cooldown
	m.mu.Unlock()

	// Same reason + scope → DUPLICATE_PROPOSAL
	resp2 := doReplan(m, "COVERAGE_GAP", "company x")
	if resp2.accepted {
		t.Fatal("duplicate proposal should be rejected")
	}
	if resp2.rejectionReason != RejectionDuplicateProposal {
		t.Errorf("got %q want %q", resp2.rejectionReason, RejectionDuplicateProposal)
	}
}

func TestReplan_Layer7_HashNormalization(t *testing.T) {
	m, _ := newReplanMaster(t)
	sid := submitReplanSession(t, m, "Company X")
	seedReplanEvidence(t, m, sid, "Company X")

	m.mu.Lock()
	m.state.AgentMaxReplans = 5
	m.mu.Unlock()

	// First call with spaces/case differences — should normalize to the same hash.
	resp := doReplan(m, "COVERAGE_GAP", "  Company X  ")
	if !resp.accepted {
		t.Fatalf("first proposal should be accepted: %s", resp.rejectionReason)
	}

	m.mu.Lock()
	m.lastAcceptedReplanAt = time.Time{} // clear cooldown
	m.mu.Unlock()

	// Different casing/whitespace → same normalized hash → DUPLICATE_PROPOSAL
	resp2 := doReplan(m, "COVERAGE_GAP", "company x")
	if resp2.accepted {
		t.Fatal("normalized duplicate should be rejected")
	}
	if resp2.rejectionReason != RejectionDuplicateProposal {
		t.Errorf("got %q want %q", resp2.rejectionReason, RejectionDuplicateProposal)
	}
}

// ========================= Layer 8: Circuit breaker =========================

func TestReplan_Layer8_CircuitBreakerTripsOnDownwardQuality(t *testing.T) {
	m, _ := newReplanMaster(t)
	sid := submitReplanSession(t, m, "Company X")
	// Seed evidence with two topics from the same source.
	ev := model.Evidence{
		SessionID:    sid,
		TaskID:       model.NewTaskID(),
		SourceID:     model.SourceID("alpha.example.com"),
		Topic:        "Company X",
		Claim:        "revenue",
		Value:        "100M",
		Confidence:   0.8,
		Verification: model.VerificationUnverified,
		CollectedAt:  time.Now().UTC(),
	}
	if _, err := m.es.Put(ev); err != nil {
		t.Fatal(err)
	}
	ev2 := model.Evidence{
		SessionID:    sid,
		TaskID:       model.NewTaskID(),
		SourceID:     model.SourceID("alpha.example.com"),
		Topic:        "Company Y",
		Claim:        "revenue",
		Value:        "200M",
		Confidence:   0.8,
		Verification: model.VerificationUnverified,
		CollectedAt:  time.Now().UTC(),
	}
	if _, err := m.es.Put(ev2); err != nil {
		t.Fatal(err)
	}

	// Register source with quality 0.5 so first acceptance records quality 0.5.
	prof := &model.SourceProfile{
		Domain:       "alpha.example.com",
		Class:        model.SourceClassUnknown,
		QualityScore: 0.5,
		AuthType:     model.AuthTypeNone,
	}
	if err := m.reg.Upsert(*prof); err != nil {
		t.Fatal(err)
	}

	m.mu.Lock()
	m.state.AgentMaxReplans = 10
	m.lastAcceptedReplanAt = time.Time{}
	m.mu.Unlock()

	// 1st acceptance: quality = 0.5, consecutive = 1
	resp := doReplan(m, "COVERAGE_GAP", "company x")
	if !resp.accepted {
		t.Fatalf("1st proposal should be accepted: %s", resp.rejectionReason)
	}

	// Lower the source quality to 0.3 to simulate downward drift.
	prof.QualityScore = 0.3
	if err := m.reg.Upsert(*prof); err != nil {
		t.Fatal(err)
	}

	// Clear cooldown and reset budget for next proposal.
	m.mu.Lock()
	m.lastAcceptedReplanAt = time.Time{}
	m.state.AgentReplanCount = 0
	m.state.AgentMaxReplans = 10
	m.mu.Unlock()

	// 2nd acceptance (different reason code + different scope → new hash):
	// quality = 0.3 < 0.5 → consecutive = 2, TRIP
	resp2 := doReplan(m, "MANUAL_RECOVERY", "company y")
	if !resp2.accepted {
		t.Fatalf("2nd proposal should be accepted (breaker trips as side-effect): %s", resp2.rejectionReason)
	}

	// Verify breaker is tripped.
	m.mu.Lock()
	tripped := m.circuitBreakerTripped
	m.mu.Unlock()
	if !tripped {
		t.Error("circuit breaker should be tripped after 2 consecutive downward-quality acceptances")
	}

	// 3rd proposal: different hash + valid scope, should be rejected by CIRCUIT_BREAKER_TRIPPED.
	m.mu.Lock()
	m.lastAcceptedReplanAt = time.Time{}
	m.state.AgentReplanCount = 0
	m.state.AgentMaxReplans = 10
	m.mu.Unlock()

	resp3 := doReplan(m, "COVERAGE_GAP", "company y")
	if resp3.accepted {
		t.Fatal("3rd proposal should be rejected (breaker tripped)")
	}
	if resp3.rejectionReason != RejectionCircuitBreakerTripped {
		t.Errorf("got %q want %q", resp3.rejectionReason, RejectionCircuitBreakerTripped)
	}

	// 4th proposal: yet another hash, still rejected (permanent trip).
	m.mu.Lock()
	m.lastAcceptedReplanAt = time.Time{}
	m.state.AgentReplanCount = 0
	m.state.AgentMaxReplans = 10
	m.mu.Unlock()

	resp4 := doReplan(m, "MANUAL_RECOVERY", "company x")
	if resp4.accepted {
		t.Fatal("4th proposal should still be rejected (breaker permanently tripped)")
	}
	if resp4.rejectionReason != RejectionCircuitBreakerTripped {
		t.Errorf("got %q want %q", resp4.rejectionReason, RejectionCircuitBreakerTripped)
	}
}

func TestReplan_Layer8_CircuitBreakerNoTripOnFlatQuality(t *testing.T) {
	m, _ := newReplanMaster(t)
	sid := submitReplanSession(t, m, "Company X")
	seedReplanEvidence(t, m, sid, "Company X")

	// Register source with quality 0.3 (same as default).
	prof := &model.SourceProfile{
		Domain:       "alpha.example.com",
		Class:        model.SourceClassUnknown,
		QualityScore: 0.3,
		AuthType:     model.AuthTypeNone,
	}
	if err := m.reg.Upsert(*prof); err != nil {
		t.Fatal(err)
	}

	m.mu.Lock()
	m.state.AgentMaxReplans = 10
	m.lastAcceptedReplanAt = time.Time{}
	m.lastAcceptedReplanQuality = 0.3
	m.mu.Unlock()

	// 1st acceptance: quality = 0.3, consecutive = 1
	resp := doReplan(m, "COVERAGE_GAP", "company x")
	if !resp.accepted {
		t.Fatalf("1st proposal should be accepted: %s", resp.rejectionReason)
	}

	m.mu.Lock()
	m.lastAcceptedReplanAt = time.Time{}
	m.state.AgentReplanCount = 0
	m.state.AgentMaxReplans = 10
	m.mu.Unlock()

	// 2nd acceptance (different reason code → different hash, same scope):
	// quality = 0.3, NOT < 0.3 → reset to 1, NOT tripped
	resp2 := doReplan(m, "MANUAL_RECOVERY", "company x")
	if !resp2.accepted {
		t.Fatalf("2nd proposal should be accepted: %s", resp2.rejectionReason)
	}

	m.mu.Lock()
	tripped := m.circuitBreakerTripped
	m.mu.Unlock()
	if tripped {
		t.Error("circuit breaker should NOT be tripped when quality is flat")
	}
}

func TestReplan_Layer8_CircuitBreakerAlreadyTripped(t *testing.T) {
	m, _ := newReplanMaster(t)
	sid := submitReplanSession(t, m, "Company X")
	seedReplanEvidence(t, m, sid, "Company X")

	m.mu.Lock()
	m.circuitBreakerTripped = true
	m.state.AgentMaxReplans = 5
	m.mu.Unlock()

	resp := doReplan(m, "COVERAGE_GAP", "company x")
	if resp.accepted {
		t.Fatal("accepted proposal should be rejected when breaker is tripped")
	}
	if resp.rejectionReason != RejectionCircuitBreakerTripped {
		t.Errorf("got %q want %q", resp.rejectionReason, RejectionCircuitBreakerTripped)
	}
}

// ========================= Success path =========================

func TestReplan_Success_AcceptsAndMergesDelta(t *testing.T) {
	m, sched := newReplanMaster(t)
	sid := submitReplanSession(t, m, "Company X")
	seedReplanEvidence(t, m, sid, "Company X")

	resp := doReplan(m, "COVERAGE_GAP", "company x")
	if !resp.accepted {
		t.Fatalf("expected acceptance: %s", resp.rejectionReason)
	}

	// Verify budget was deducted.
	used, _ := agentReplanState(m)
	if used != 1 {
		t.Errorf("AgentReplanCount after acceptance: got %d want 1", used)
	}

	// Verify a task was injected into the scheduler.
	time.Sleep(50 * time.Millisecond)
	stats := sched.Stats()
	if stats.Queued < 1 {
		t.Errorf("expected >=1 queued task after replan, got %d", stats.Queued)
	}

	// Verify the hash was recorded.
	expectedHash := semanticHash("COVERAGE_GAP", "company x")
	m.mu.Lock()
	recorded := m.proposalHashes[expectedHash]
	m.mu.Unlock()
	if !recorded {
		t.Error("proposal hash not recorded in proposalHashes")
	}
}

// ========================= Logging =========================

type recordingObserver struct {
	mu     sync.Mutex
	events []Event
}

func (r *recordingObserver) Emit(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recordingObserver) getEvents() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]Event, len(r.events))
	copy(cp, r.events)
	return cp
}

func TestReplan_LoggingEveryProposal(t *testing.T) {
	m, _ := newReplanMaster(t)
	sid := submitReplanSession(t, m, "Company X")
	seedReplanEvidence(t, m, sid, "Company X")

	obs := &recordingObserver{}
	m.observer = obs

	// Accepted
	resp := doReplan(m, "COVERAGE_GAP", "company x")
	if !resp.accepted {
		t.Fatalf("expected acceptance: %s", resp.rejectionReason)
	}

	// Rejected (duplicate)
	m.mu.Lock()
	m.lastAcceptedReplanAt = time.Time{}
	m.state.AgentMaxReplans = 5
	m.mu.Unlock()

	resp2 := doReplan(m, "COVERAGE_GAP", "company x")
	if resp2.accepted {
		t.Fatal("duplicate should be rejected")
	}

	events := obs.getEvents()
	if len(events) < 2 {
		t.Fatalf("expected >=2 events, got %d", len(events))
	}

	// Check that the accepted event was logged.
	foundAccepted := false
	foundRejected := false
	for _, ev := range events {
		if ev.Kind != "external_replan" {
			continue
		}
		if strings.Contains(ev.Message, "accepted") {
			foundAccepted = true
		}
		if strings.Contains(ev.Message, "rejected") {
			foundRejected = true
		}
	}
	if !foundAccepted {
		t.Error("expected an 'accepted' external_replan event")
	}
	if !foundRejected {
		t.Error("expected a 'rejected' external_replan event")
	}
}

// ========================= Helper tests =========================

func TestSemanticHash_Normalization(t *testing.T) {
	s1 := normalizeScope("company x")
	s2 := normalizeScope("  Company X  ")
	if s1 != s2 {
		t.Fatal("normalizeScope should produce identical output for equivalent inputs")
	}
	h1 := semanticHash("COVERAGE_GAP", s1)
	h2 := semanticHash("COVERAGE_GAP", s2)
	if h1 != h2 {
		t.Error("semantic hash should be identical after normalization")
	}
}

func TestSemanticHash_DifferentInputs(t *testing.T) {
	h1 := semanticHash("COVERAGE_GAP", "company x")
	h2 := semanticHash("MANUAL_RECOVERY", "company x")
	if h1 == h2 {
		t.Error("different reason codes should produce different hashes")
	}
	h3 := semanticHash("COVERAGE_GAP", "other topic")
	if h1 == h3 {
		t.Error("different scopes should produce different hashes")
	}
}

func TestMapReasonCodeToTrigger(t *testing.T) {
	cases := []struct {
		reasonCode string
		kind       ReplanTriggerKind
	}{
		{"COVERAGE_GAP", ReplanTriggerCoverageGap},
		{"MANUAL_RECOVERY", ReplanTriggerManual},
		{"BOGUS", ReplanTriggerKind("")},
	}
	for _, tc := range cases {
		tr := mapReasonCodeToTrigger(tc.reasonCode, "topic")
		if tr.Kind != tc.kind {
			t.Errorf("reason_code=%s: got kind %q want %q", tc.reasonCode, tr.Kind, tc.kind)
		}
		if tc.reasonCode != "BOGUS" && tr.MissingTopic != "topic" {
			t.Errorf("reason_code=%s: MissingTopic = %q want %q", tc.reasonCode, tr.MissingTopic, "topic")
		}
	}
}

func TestIsValidExternalReasonCode(t *testing.T) {
	if !isValidExternalReasonCode("COVERAGE_GAP") {
		t.Error("COVERAGE_GAP should be valid")
	}
	if !isValidExternalReasonCode("MANUAL_RECOVERY") {
		t.Error("MANUAL_RECOVERY should be valid")
	}
	if isValidExternalReasonCode("BOGUS") {
		t.Error("BOGUS should be invalid")
	}
}

func TestBudgetAfterMap(t *testing.T) {
	bm := budgetAfterMap(2, 3)
	if bm["agent_replans_used"] != 2 {
		t.Errorf("used: got %d want 2", bm["agent_replans_used"])
	}
	if bm["agent_replans_remaining"] != 1 {
		t.Errorf("remaining: got %d want 1", bm["agent_replans_remaining"])
	}
}

func TestSemanticHashMatchesSHA256(t *testing.T) {
	// Verify the hash is a valid SHA-256 hex string.
	h := semanticHash("COVERAGE_GAP", "company x")
	if len(h) != 64 {
		t.Errorf("hash length: got %d want 64", len(h))
	}
	sum := sha256.Sum256([]byte("COVERAGE_GAP|company x"))
	expected := hex.EncodeToString(sum[:])
	if h != expected {
		t.Error("hash does not match expected SHA-256")
	}
}
