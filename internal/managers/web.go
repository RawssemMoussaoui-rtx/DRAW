package managers

import (
	"context"
	"time"

	"draw/internal/manager"
	"draw/internal/model"
)

const defaultWebTimeout = 10 * time.Second

type httpResult struct {
	result model.TaskResult
	err    error
}

// runHTTP invokes the worker with a bounded timeout.
//
// If the worker implements manager.ContextWorker (e.g. HTTPWorker), RunContext
// is called directly with a deadline-bound context. This eliminates the
// unmanaged goroutine that could leak when a non-context-aware Run blocked
// past the timeout. The worker is responsible for respecting ctx.Done().
//
// If the worker does NOT implement ContextWorker, the previous behavior is
// preserved: Run is invoked in a goroutine raced against the context deadline.
func runHTTP(w manager.Worker, t model.Task, timeout time.Duration) (model.TaskResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if cw, ok := w.(manager.ContextWorker); ok {
		r, err := cw.RunContext(ctx, t)
		if err != nil {
			return model.TaskResult{Status: model.RetrievalStatusInvalidContent, Error: err}, nil
		}
		return r, nil
	}

	// Fallback for non-context-aware workers: race Run against the context
	// deadline. Note: a worker that ignores the deadline may leak its goroutine
	// — the Execute-level context (cancelled on Master.Stop) is not propagated
	// here because Execute receives no ctx (master.go:246 is locked). This is a
	// Phase-H limitation; the ContextWorker path above avoids the leak for
	// context-aware workers.
	done := make(chan httpResult, 1)
	go func() {
		r, e := w.Run(t)
		done <- httpResult{result: r, err: e}
	}()

	select {
	case <-ctx.Done():
		return model.TaskResult{Status: model.RetrievalStatusInvalidContent, Error: ctx.Err()}, nil
	case res := <-done:
		if res.err != nil {
			return model.TaskResult{Status: model.RetrievalStatusInvalidContent, Error: res.err}, nil
		}
		return res.result, nil
	}
}

type WebManager struct {
	worker manager.Worker
}

func NewWebManager(w manager.Worker) *WebManager {
	return &WebManager{worker: w}
}

func (m *WebManager) Capabilities() []manager.Capability {
	return []manager.Capability{manager.CapHTTP}
}

func (m *WebManager) Execute(t model.Task) (model.TaskResult, error) {
	return runHTTP(m.worker, t, defaultWebTimeout)
}
