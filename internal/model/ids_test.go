package model

import "testing"

func TestIDsMonotonicAndPrefixed(t *testing.T) {
	a := NewTaskID()
	b := NewTaskID()
	if a == b {
		t.Fatalf("task IDs must be unique: %s", a)
	}
	if !startsWith(string(a), "tsk_") {
		t.Fatalf("task id must be prefixed tsk_: %s", a)
	}
	intentA := NewIntentID()
	if !startsWith(string(intentA), "int_") {
		t.Fatalf("intent id must be prefixed int_: %s", intentA)
	}
	if NewURLID() == "" {
		t.Fatal("url id must not be empty")
	}
	if NewEvidenceID() == "" {
		t.Fatal("evidence id must not be empty")
	}
	if NewSourceID("example.com") != "example.com" {
		t.Fatal("source id must equal its domain")
	}
}

func TestTaskTypesComplete(t *testing.T) {
	want := map[TaskType]bool{
		TaskTypeDiscover: true, TaskTypeFetchHTTP: true,
		TaskTypeFetchBrowser: true, TaskTypeVerify: true, TaskTypeReconcile: true,
	}
	if len(TaskTypes) != len(want) {
		t.Fatalf("TaskTypes has %d entries, want %d", len(TaskTypes), len(want))
	}
	for k, tt := range TaskTypes {
		if !want[tt] {
			t.Errorf("unexpected TaskType key %q -> %s", k, tt)
		}
		if TaskType(k) != tt {
			t.Errorf("key %q does not round-trip", k)
		}
	}
}

func TestSourceClassesValid(t *testing.T) {
	classes := []SourceClass{SourceClassOfficial, SourceClassNews, SourceClassSocial, SourceClassSpecialized, SourceClassUnknown}
	if len(classes) != 5 {
		t.Fatalf("expected 5 source classes, got %d", len(classes))
	}
}

func TestRetrievalStatusesValid(t *testing.T) {
	want := []RetrievalStatus{
		RetrievalStatusSuccess, RetrievalStatusPartial, RetrievalStatusEmpty,
		RetrievalStatusJavascriptRequired, RetrievalStatusAuthRequired,
		RetrievalStatusTimeout, RetrievalStatusBlocked, RetrievalStatusInvalidContent,
	}
	if len(want) != 8 {
		t.Fatalf("expected 8 retrieval statuses, got %d", len(want))
	}
}

func TestErrorWeightsValid(t *testing.T) {
	want := []ErrorWeight{
		ErrorWeightLow, ErrorWeightModerate, ErrorWeightHigh, ErrorWeightBlocking,
	}
	if len(want) != 4 {
		t.Fatalf("expected 4 error weights, got %d", len(want))
	}
}

func TestTaskIsTerminal(t *testing.T) {
	cases := map[TaskState]bool{
		TaskStateCompleted: true, TaskStateCancelled: true, TaskStateExpired: true,
		TaskStateReady: false, TaskStateRunning: false, TaskStateRetryWait: false,
		TaskStateFailed: false, TaskStateBlocked: false,
	}
	for st, want := range cases {
		got := Task{State: st}.IsTerminal()
		if got != want {
			t.Errorf("state %s isTerminal=%v want %v", st, got, want)
		}
	}
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
