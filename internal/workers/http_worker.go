package workers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"draw/internal/manager"
	"draw/internal/model"
	"draw/internal/storage"
)

const defaultWorkerTimeout = 30 * time.Second

type HTTPWorker struct {
	client manager.FetchClient
	opts   WorkerOpts
	src    storage.SourceRegistry
}

type WorkerOpts struct {
	UserAgentPool []string
	MaxAttempts   int
	Timeout       time.Duration
	MaxRedirects  int
}

func NewHTTPWorker(client manager.FetchClient, opts WorkerOpts) *HTTPWorker {
	return &HTTPWorker{client: client, opts: opts}
}

// WithSourceRegistry attaches a source registry used for fetch-boundary
// denylist enforcement. It propagates the registry to the underlying client
// when the client is a *netHTTPClient, keeping NewHTTPWorker's signature
// backward compatible for existing positional call sites.
func (w *HTTPWorker) WithSourceRegistry(reg storage.SourceRegistry) *HTTPWorker {
	w.src = reg
	if nc, ok := w.client.(*netHTTPClient); ok {
		nc.src = reg
	}
	return w
}

func (w *HTTPWorker) Run(t model.Task) (model.TaskResult, error) {
	return w.RunContext(context.Background(), t)
}

func (w *HTTPWorker) RunContext(ctx context.Context, t model.Task) (model.TaskResult, error) {
	timeout := w.opts.Timeout
	if timeout <= 0 {
		timeout = defaultWorkerTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req := manager.FetchRequest{
		URL:          t.URL,
		Method:       http.MethodGet,
		Timeout:      timeout,
		MaxRedirects: w.opts.MaxRedirects,
	}

	resp, err := w.client.Fetch(ctx, req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) {
			return model.TaskResult{Status: model.RetrievalStatusTimeout, Error: err}, err
		}
		return model.TaskResult{Status: model.RetrievalStatusInvalidContent, Error: err}, err
	}

	var status model.RetrievalStatus
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		status = model.RetrievalStatusSuccess
	case resp.StatusCode == http.StatusUnauthorized:
		status = model.RetrievalStatusAuthRequired
	case resp.StatusCode == http.StatusForbidden:
		status = model.RetrievalStatusBlocked
	case resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode >= 500 && resp.StatusCode < 600):
		status = model.RetrievalStatusTimeout
	default:
		status = model.RetrievalStatusInvalidContent
	}

	return model.TaskResult{
		Status:  status,
		Data:    resp.Body,
		Headers: resp.Headers,
	}, nil
}

func (w *HTTPWorker) Close() error {
	return nil
}
