package result

import (
	"encoding/json"
	"sort"
	"testing"
	"time"

	"draw/internal/master"
	"draw/internal/model"
	"draw/internal/storage"
)

// ---- fakes ----

type fakeEvidenceStore struct {
	evidence  []model.Evidence
	relations []model.EvidenceRelation
}

func (s *fakeEvidenceStore) Put(e model.Evidence) (model.EvidenceID, error) {
	if e.ID == "" {
		return model.NewEvidenceID(), nil
	}
	return e.ID, nil
}

func (s *fakeEvidenceStore) PutRelation(model.EvidenceRelation) error        { return nil }
func (s *fakeEvidenceStore) PutRelations([]model.EvidenceRelation) error      { return nil }

func (s *fakeEvidenceStore) Query(f storage.EvidenceFilter) []model.Evidence {
	out := make([]model.Evidence, len(s.evidence))
	copy(out, s.evidence)
	return out
}

func (s *fakeEvidenceStore) FindRelations(topic string) []model.EvidenceRelation {
	evMap := map[model.EvidenceID]string{}
	for _, ev := range s.evidence {
		evMap[ev.ID] = ev.Topic
	}
	var out []model.EvidenceRelation
	for _, rel := range s.relations {
		ft, ok1 := evMap[rel.From]
		tt, ok2 := evMap[rel.To]
		if (ok1 && ft == topic) || (ok2 && tt == topic) {
			out = append(out, rel)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		if out[i].To != out[j].To {
			return out[i].To < out[j].To
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

type fakeSourceRegistry struct {
	profiles map[string]model.SourceProfile
	misses   map[string]bool
}

func (r *fakeSourceRegistry) Lookup(domain string) (*model.SourceProfile, bool) {
	if r.misses[domain] {
		return nil, false
	}
	p, ok := r.profiles[domain]
	if !ok {
		return nil, false
	}
	return &p, true
}

func (r *fakeSourceRegistry) Upsert(model.SourceProfile) error            { return nil }
func (r *fakeSourceRegistry) Deny(string) error                           { return nil }
func (r *fakeSourceRegistry) ProvisionalUpsert(model.SourceProfile) error { return nil }

// ---- helpers ----

func mkState(intent model.Intent, phases []model.Phase, budgetUsed int, reason string, completed map[string]bool) master.ResearchState {
	return master.ResearchState{
		Session: &model.Session{
			ID:     model.SessionID("ses-1"),
			Intent: intent,
			Plan:   model.Plan{Phases: phases},
		},
		BudgetUsed:     budgetUsed,
		TerminalReason: reason,
		PhaseCompleted: completed,
	}
}

// ---- tests ----

func TestReportBuilder_MapsFields(t *testing.T) {
	intent := model.Intent{
		Entity: "Climate Change",
		TimeRange: &model.TimeWindow{
			From: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			To:   time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC),
		},
	}
	phases := []model.Phase{
		{Name: "discovery", Tasks: []model.TaskID{"t1"}, Completed: false},
		{Name: "primary_retrieval", Tasks: []model.TaskID{"t2"}, Completed: false},
	}
	completed := map[string]bool{"discovery": true}
	st := mkState(intent, phases, 42, "research_complete", completed)

	b := NewReportBuilder(&fakeEvidenceStore{}, &fakeSourceRegistry{})
	env := b.Build(st)

	if env.Status != "research_complete" {
		t.Fatalf("Status = %q, want %q", env.Status, "research_complete")
	}
	if env.SessionID != model.SessionID("ses-1") {
		t.Fatalf("SessionID = %q, want %q", env.SessionID, "ses-1")
	}
	if env.BudgetUsed != 42 {
		t.Fatalf("BudgetUsed = %d, want %d", env.BudgetUsed, 42)
	}
	if len(env.PlanPhases) != 2 {
		t.Fatalf("PlanPhases len = %d, want 2", len(env.PlanPhases))
	}
	if env.PlanPhases[0].Name != "discovery" {
		t.Fatalf("phase 0 name = %q, want discovery", env.PlanPhases[0].Name)
	}
	if !env.PlanPhases[0].Completed {
		t.Fatal("phase 0 should be completed")
	}
	if env.PlanPhases[1].Completed {
		t.Fatal("phase 1 should not be completed")
	}
	if env.Payload.Intent.Entity != "Climate Change" {
		t.Fatalf("Intent.Entity = %q, want Climate Change", env.Payload.Intent.Entity)
	}
	if len(env.Payload.Entities) != 1 || env.Payload.Entities[0] != "Climate Change" {
		t.Fatalf("Entities = %v, want [Climate Change]", env.Payload.Entities)
	}
	if !env.Payload.TimeRange.From.Equal(intent.TimeRange.From) {
		t.Fatal("TimeRange From mismatch")
	}
	if !env.Payload.TimeRange.To.Equal(intent.TimeRange.To) {
		t.Fatal("TimeRange To mismatch")
	}
}

func TestReportBuilder_SourcesRollup(t *testing.T) {
	evs := []model.Evidence{
		{ID: "e1", SourceID: "gov.example", Topic: "t1", Claim: "c1", Value: "v1", Confidence: 0.9, Verification: model.VerificationVerified},
		{ID: "e2", SourceID: "news.example", Topic: "t1", Claim: "c1", Value: "v1", Confidence: 0.8, Verification: model.VerificationVerified},
		{ID: "e3", SourceID: "unknown.example", Topic: "t1", Claim: "c1", Value: "v1", Confidence: 0.5, Verification: model.VerificationUnverified},
	}
	es := &fakeEvidenceStore{evidence: evs}

	src := &fakeSourceRegistry{
		profiles: map[string]model.SourceProfile{
			"gov.example":  {Domain: "gov.example", Class: model.SourceClassOfficial, QualityScore: 0.95},
			"news.example": {Domain: "news.example", Class: model.SourceClassNews, QualityScore: 0.6},
		},
		misses: map[string]bool{"unknown.example": true},
	}
	b := NewReportBuilder(es, src)

	st := mkState(model.Intent{}, nil, 0, "", nil)
	env := b.Build(st)

	sources := env.Payload.Sources
	if len(sources) != 3 {
		t.Fatalf("Sources len = %d, want 3", len(sources))
	}
	if sources[0].Name != "gov.example" || sources[0].Quality != 0.95 || sources[0].Class != model.SourceClassOfficial {
		t.Fatalf("source 0 = %+v", sources[0])
	}
	if sources[1].Name != "news.example" || sources[1].Quality != 0.6 || sources[1].Class != model.SourceClassNews {
		t.Fatalf("source 1 = %+v", sources[1])
	}
	if sources[2].Name != "unknown.example" || sources[2].Quality != 0.3 || sources[2].Class != model.SourceClassUnknown {
		t.Fatalf("source 2 = %+v", sources[2])
	}
	for _, u := range sources {
		if u.URL != "https://"+u.Name {
			t.Fatalf("source URL = %q, want https://%s", u.URL, u.Name)
		}
	}
}

func TestReportBuilder_FindingsConsensus(t *testing.T) {
	evs := []model.Evidence{
		{ID: "e1", Topic: "ai", Claim: "what is the answer", Value: "42", Confidence: 0.9, Verification: model.VerificationVerified},
		{ID: "e2", Topic: "ai", Claim: "what is the answer", Value: "41", Confidence: 0.3, Verification: model.VerificationUnverified},
		{ID: "e3", Topic: "ai", Claim: "what is the answer", Value: "42", Confidence: 0.8, Verification: model.VerificationPartiallyVerified},
		{ID: "e4", Topic: "physics", Claim: "what is light", Value: "waves", Confidence: 0.7, Verification: model.VerificationVerified},
		{ID: "e5", Topic: "physics", Claim: "what is light", Value: "particles", Confidence: 0.6, Verification: model.VerificationDisputed},
	}
	es := &fakeEvidenceStore{evidence: evs}
	b := NewReportBuilder(es, &fakeSourceRegistry{})

	st := mkState(model.Intent{}, nil, 0, "", nil)
	env := b.Build(st)

	findings := env.Payload.Findings
	if len(findings) != 2 {
		t.Fatalf("Findings len = %d, want 2", len(findings))
	}

	// "ai" < "physics" so group 0 is the ai topic
	f0 := findings[0]
	if f0.Claim != "what is the answer" {
		t.Fatalf("finding 0 claim = %q", f0.Claim)
	}
	if f0.Value != "42" {
		t.Fatalf("finding 0 value = %q, want 42", f0.Value)
	}
	if len(f0.EvidenceIDs) != 2 || f0.EvidenceIDs[0] != "e1" || f0.EvidenceIDs[1] != "e3" {
		t.Fatalf("finding 0 evidence IDs = %v, want [e1 e3]", f0.EvidenceIDs)
	}
	if f0.Status != string(model.VerificationVerified) {
		t.Fatalf("finding 0 status = %q, want VERIFIED", f0.Status)
	}

	f1 := findings[1]
	if f1.Claim != "what is light" {
		t.Fatalf("finding 1 claim = %q", f1.Claim)
	}
	if f1.Value != "waves" {
		t.Fatalf("finding 1 value = %q, want waves", f1.Value)
	}
	if len(f1.EvidenceIDs) != 1 || f1.EvidenceIDs[0] != "e4" {
		t.Fatalf("finding 1 evidence IDs = %v, want [e4]", f1.EvidenceIDs)
	}
	if f1.Status != string(model.VerificationVerified) {
		t.Fatalf("finding 1 status = %q, want VERIFIED", f1.Status)
	}
}

func TestReportBuilder_Contradictions(t *testing.T) {
	evs := []model.Evidence{
		{ID: "e1", Topic: "t1", Claim: "climate is changing", Value: "yes", Confidence: 0.9, Verification: model.VerificationVerified},
		{ID: "e2", Topic: "t1", Claim: "climate is changing", Value: "no", Confidence: 0.8, Verification: model.VerificationDisputed},
		{ID: "e3", Topic: "t1", Claim: "climate is hot", Value: "yes", Confidence: 0.7, Verification: model.VerificationVerified},
	}
	rels := []model.EvidenceRelation{
		{From: "e1", To: "e2", Kind: model.EvidenceRelationContradicts, Strength: 1.0},
		{From: "e2", To: "e3", Kind: model.EvidenceRelationContradicts, Strength: 0.8},
	}
	es := &fakeEvidenceStore{evidence: evs, relations: rels}
	b := NewReportBuilder(es, &fakeSourceRegistry{})

	st := mkState(model.Intent{}, nil, 0, "", nil)
	env := b.Build(st)

	cs := env.Payload.Contradictions
	if len(cs) != 2 {
		t.Fatalf("Contradictions len = %d, want 2", len(cs))
	}
	// Sorted by (ClaimA, ClaimB, EvidenceIDA):
	//  - e1->e2: ClaimA="climate is changing", ClaimB="climate is changing", IDA="e1"
	//  - e2->e3: ClaimA="climate is changing", ClaimB="climate is hot", IDA="e2"
	if cs[0].EvidenceIDA != "e1" || cs[0].EvidenceIDB != "e2" || cs[0].Strength != 1.0 {
		t.Fatalf("contradiction 0 = %+v", cs[0])
	}
	if cs[1].EvidenceIDA != "e2" || cs[1].EvidenceIDB != "e3" || cs[1].Strength != 0.8 {
		t.Fatalf("contradiction 1 = %+v", cs[1])
	}
	if cs[0].ClaimA != "climate is changing" || cs[0].ClaimB != "climate is changing" {
		t.Fatalf("contradiction 0 claims = %q %q", cs[0].ClaimA, cs[0].ClaimB)
	}
	if cs[1].ClaimB != "climate is hot" {
		t.Fatalf("contradiction 1 ClaimB = %q, want climate is hot", cs[1].ClaimB)
	}
}

func TestReportBuilder_CompletenessConfidence(t *testing.T) {
	phases := []model.Phase{
		{Name: "discovery"}, {Name: "primary_retrieval"}, {Name: "secondary_retrieval"},
	}
	completed := map[string]bool{"discovery": true, "primary_retrieval": true}
	evs := []model.Evidence{
		{ID: "e1", Verification: model.VerificationVerified},
		{ID: "e2", Verification: model.VerificationVerified},
		{ID: "e3", Verification: model.VerificationUnverified},
		{ID: "e4", Verification: model.VerificationDisputed},
	}
	es := &fakeEvidenceStore{evidence: evs}
	b := NewReportBuilder(es, &fakeSourceRegistry{})

	st := mkState(model.Intent{}, phases, 0, "", completed)
	env := b.Build(st)

	if env.Payload.Completeness != 2.0/3.0 {
		t.Fatalf("Completeness = %v, want %v", env.Payload.Completeness, 2.0/3.0)
	}
	if env.Payload.Confidence != 0.5 {
		t.Fatalf("Confidence = %v, want 0.5", env.Payload.Confidence)
	}
}

func TestReportBuilder_Determinism(t *testing.T) {
	evs := []model.Evidence{
		{ID: "e1", SourceID: "gov.example", Topic: "ai", Claim: "what is the answer", Value: "42", Confidence: 0.9, Verification: model.VerificationVerified},
		{ID: "e2", SourceID: "news.example", Topic: "ai", Claim: "what is the answer", Value: "41", Confidence: 0.3, Verification: model.VerificationUnverified},
		{ID: "e3", SourceID: "unknown.example", Topic: "physics", Claim: "what is light", Value: "waves", Confidence: 0.7, Verification: model.VerificationVerified},
		{ID: "e4", SourceID: "gov.example", Topic: "physics", Claim: "what is light", Value: "particles", Confidence: 0.6, Verification: model.VerificationDisputed},
	}
	rels := []model.EvidenceRelation{
		{From: "e1", To: "e2", Kind: model.EvidenceRelationContradicts, Strength: 1.0},
	}
	es := &fakeEvidenceStore{evidence: evs, relations: rels}
	src := &fakeSourceRegistry{
		profiles: map[string]model.SourceProfile{
			"gov.example":  {Domain: "gov.example", Class: model.SourceClassOfficial, QualityScore: 0.95},
			"news.example": {Domain: "news.example", Class: model.SourceClassNews, QualityScore: 0.6},
		},
		misses: map[string]bool{"unknown.example": true},
	}
	phases := []model.Phase{{Name: "discovery"}, {Name: "primary_retrieval"}}
	completed := map[string]bool{"discovery": true}
	st := mkState(model.Intent{Entity: "AI"}, phases, 10, "research_complete", completed)

	b := NewReportBuilder(es, src)

	r1 := b.Build(st)
	r2 := b.Build(st)

	r1.CompletedAt = time.Time{}
	r2.CompletedAt = time.Time{}

	j1, err := json.Marshal(r1)
	if err != nil {
		t.Fatalf("marshal r1: %v", err)
	}
	j2, err := json.Marshal(r2)
	if err != nil {
		t.Fatalf("marshal r2: %v", err)
	}

	if string(j1) != string(j2) {
		t.Fatalf("determinism check failed:\n%s\n!=\n%s", j1, j2)
	}
}
