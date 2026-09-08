package master

import (
	"testing"

	"draw/internal/config"
	"draw/internal/model"
)

// TestReseedTask_SetsCreatedByTaskID verifies G1: reseedTask sets
// CreatedByTaskID to a non-nil value matching the Upgrade-path semantics.
// The Upgrade path (applyDecision, master.go:616) sets CreatedByTaskID = &t.ID;
// reseedTask (master.go:693) must do the same for provenance parity.
func TestReseedTask_SetsCreatedByTaskID(t *testing.T) {
	m := NewMaster(config.Defaults(), newFakeScheduler(&fakeManager{}, 4))

	orig := model.Task{
		ID:          model.NewTaskID(),
		SessionID:   model.NewSessionID(),
		Type:        model.TaskTypeFetchHTTP,
		State:       model.TaskStateCompleted,
		SourceClass: model.SourceClassUnknown,
		URL:         mustURL("https://example.com"),
		TaskKey:     "seed:orig:0:example.com",
	}

	result := m.reseedTask(orig, "rediscover")

	if result.CreatedByTaskID == nil {
		t.Fatal("expected CreatedByTaskID to be non-nil (matches Upgrade-path semantics)")
	}
	if *result.CreatedByTaskID != orig.ID {
		t.Errorf("CreatedByTaskID = %s, want %s (orig.ID)", *result.CreatedByTaskID, orig.ID)
	}
	if result.ParentTaskID == nil {
		t.Fatal("expected ParentTaskID to be non-nil")
	}
	if *result.ParentTaskID != orig.ID {
		t.Errorf("ParentTaskID = %s, want %s", *result.ParentTaskID, orig.ID)
	}
	if result.ID == orig.ID {
		t.Error("reseeded task should have a new ID, not reuse orig.ID")
	}
}

// TestReseedTask_CreatedByTaskID_EqualsOrigID verifies the provenance link
// holds for both reseed kinds ("rediscover" → DISCOVER, "verify" → VERIFY).
func TestReseedTask_CreatedByTaskID_EqualsOrigID(t *testing.T) {
	m := NewMaster(config.Defaults(), newFakeScheduler(&fakeManager{}, 4))

	for _, tc := range []struct {
		kind      string
		wantType  model.TaskType
	}{
		{"rediscover", model.TaskTypeDiscover},
		{"verify", model.TaskTypeVerify},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			orig := model.Task{
				ID:          model.NewTaskID(),
				SessionID:   model.NewSessionID(),
				Type:        model.TaskTypeFetchHTTP,
				State:       model.TaskStateCompleted,
				URL:         mustURL("https://example.com"),
				TaskKey:     "seed:orig:0:example.com",
			}

			result := m.reseedTask(orig, tc.kind)

			if result.CreatedByTaskID == nil {
				t.Fatal("expected CreatedByTaskID non-nil")
			}
			if *result.CreatedByTaskID != orig.ID {
				t.Errorf("CreatedByTaskID = %s, want %s", *result.CreatedByTaskID, orig.ID)
			}
			if result.ParentTaskID == nil || *result.ParentTaskID != orig.ID {
				t.Errorf("ParentTaskID should equal orig.ID %s", orig.ID)
			}
			if result.Type != tc.wantType {
				t.Errorf("Type = %s, want %s", result.Type, tc.wantType)
			}
		})
	}
}
