package managers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"draw/internal/manager"
	"draw/internal/model"
)

// Compile-time assertions that the fakes satisfy the manager interfaces.
var (
	_ manager.Worker        = (*ctxWorkerForTest)(nil)
	_ manager.ContextWorker = (*ctxWorkerForTest)(nil)
	_ manager.Worker        = (*legacyWorkerForTest)(nil)
)

// ctxWorkerForTest implements both manager.Worker and manager.ContextWorker.
// RunContext issues an HTTP GET whose request context is honoured, so ctx
// cancellation propagates to the underlying HTTP client.
type ctxWorkerForTest struct {
	client *http.Client
}

func (w *ctxWorkerForTest) Run(t model.Task) (model.TaskResult, error) {
	return w.RunContext(context.Background(), t)
}

func (w *ctxWorkerForTest) RunContext(ctx context.Context, t model.Task) (model.TaskResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL.String(), nil)
	if err != nil {
		return model.TaskResult{Status: model.RetrievalStatusInvalidContent, Error: err}, nil
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return model.TaskResult{Status: model.RetrievalStatusInvalidContent, Error: err}, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return model.TaskResult{Status: model.RetrievalStatusSuccess, Data: body}, nil
}

func (w *ctxWorkerForTest) Close() error { return nil }

// legacyWorkerForTest implements ONLY manager.Worker (not ContextWorker).
// Run blocks on a channel that the test controls, so the goroutine spawned by
// runHTTP's fallback path is released at test exit rather than leaking.
type legacyWorkerForTest struct {
	unblock chan struct{}
}

func (w *legacyWorkerForTest) Run(model.Task) (model.TaskResult, error) {
	<-w.unblock
	return model.TaskResult{Status: model.RetrievalStatusSuccess}, nil
}

func (w *legacyWorkerForTest) Close() error { return nil }

// waitForStableGoroutines gives the runtime a moment to settle goroutines
// (GC + idle cleanup) before a NumGoroutine baseline is captured.
func waitForStableGoroutines(t *testing.T) {
	t.Helper()
	for i := 0; i < 10; i++ {
		runtime.GC()
		time.Sleep(20 * time.Millisecond)
	}
}

// assertNoGoroutineLeak polls runtime.NumGoroutine until it settles to within
// delta of `before` (tolerance <= 1), or fails after 2s.
func assertNoGoroutineLeak(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var after int
	for time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(20 * time.Millisecond)
		after = runtime.NumGoroutine()
		if after <= before+1 {
			return
		}
	}
	after = runtime.NumGoroutine()
	if after > before+1 {
		t.Errorf("possible goroutine leak: before=%d after=%d (delta=%d)", before, after, after-before)
	}
}

// TestRunHTTP_NoGoroutineLeakOnContextCancel verifies the Batch-2 Q10 fix:
// when the worker implements manager.ContextWorker, runHTTP calls
// RunContext directly (no unmanaged goroutine) and cancellation of runHTTP's
// bounded context propagates to the worker, causing prompt return with no
// residual goroutines.
func TestRunHTTP_NoGoroutineLeakOnContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	task := model.Task{URL: mustURL(t, srv.URL)}
	worker := &ctxWorkerForTest{client: srv.Client()}

	waitForStableGoroutines(t)
	before := runtime.NumGoroutine()

	start := time.Now()
	result, err := runHTTP(worker, task, 100*time.Millisecond)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if result.Status != model.RetrievalStatusInvalidContent {
		t.Fatalf("expected InvalidContent on context cancel, got %q", result.Status)
	}
	// Context cancels at ~100ms and RunContext honors ctx.Done(), so runHTTP
	// should return well within ~250ms (vs the 5s server delay).
	if elapsed > 250*time.Millisecond {
		t.Fatalf("runHTTP should return promptly on ctx cancel, took %v", elapsed)
	}

	assertNoGoroutineLeak(t, before)
}

// TestRunHTTP_FallsBackForNonContextWorker verifies the backward-compatible
// path: a worker implementing only manager.Worker (no RunContext) still
// completes — its Run is raced against the deadline and runHTTP returns on
// ctx.Done().
func TestRunHTTP_FallsBackForNonContextWorker(t *testing.T) {
	worker := &legacyWorkerForTest{unblock: make(chan struct{})}
	defer close(worker.unblock)

	task := model.Task{URL: mustURL(t, "https://example.com")}

	start := time.Now()
	result, err := runHTTP(worker, task, 100*time.Millisecond)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != model.RetrievalStatusInvalidContent {
		t.Fatalf("expected InvalidContent (deadline), got %q", result.Status)
	}
	if elapsed > 1*time.Second {
		t.Fatalf("runHTTP should return on deadline, took %v", elapsed)
	}
}
