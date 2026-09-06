package manager

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"draw/internal/model"
)

type Capability string

const (
	CapHTTP        Capability = "HTTP"
	CapBrowserAnon Capability = "BROWSER_ANON"
	CapBrowserAuth Capability = "BROWSER_AUTH"
	CapRSS         Capability = "RSS"
)

type WorkerSlot struct {
	ID string
}

type Manager interface {
	Capabilities() []Capability
	Execute(model.Task) (model.TaskResult, error)
}

type Worker interface {
	Run(model.Task) (model.TaskResult, error)
	Close() error
}

type ContextWorker interface {
	RunContext(ctx context.Context, t model.Task) (model.TaskResult, error)
}

type FetchClient interface {
	Fetch(ctx context.Context, req FetchRequest) (FetchResponse, error)
}

type FetchRequest struct {
	URL          *url.URL
	Method       string
	Headers      http.Header
	Timeout      time.Duration
	MaxRedirects int
	UserAgent    string
}

type FetchResponse struct {
	StatusCode  int
	Headers     http.Header
	Body        []byte
	FinalURL    string
	ContentType string
}
