package workers

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"draw/internal/manager"
	"draw/internal/storage"
)

const (
	defaultBackoffBase = 100 * time.Millisecond
	defaultBackoffMax  = 1 * time.Second
)

type netHTTPClient struct {
	uaPool       []string
	maxAttempts  int
	baseTimeout  time.Duration
	maxRedirects int
	src          storage.SourceRegistry
}

type FetchClientOpts struct {
	UserAgentPool  []string
	MaxAttempts    int
	BaseTimeout    time.Duration
	MaxRedirects   int
	SourceRegistry storage.SourceRegistry
}

func NewFetchClient(opts FetchClientOpts) *netHTTPClient {
	c := &netHTTPClient{
		uaPool:       opts.UserAgentPool,
		maxAttempts:  opts.MaxAttempts,
		baseTimeout:  opts.BaseTimeout,
		maxRedirects: opts.MaxRedirects,
		src:          opts.SourceRegistry,
	}
	if c.maxAttempts < 1 {
		c.maxAttempts = 1
	}
	return c
}

func (c *netHTTPClient) Fetch(ctx context.Context, req manager.FetchRequest) (manager.FetchResponse, error) {
	attempts := c.maxAttempts
	maxRedirects := c.maxRedirects
	if req.MaxRedirects > 0 {
		maxRedirects = req.MaxRedirects
	}
	if ShouldDeny(req.URL, c.src) {
		return manager.FetchResponse{StatusCode: http.StatusForbidden, FinalURL: req.URL.String()}, nil
	}

	httpClient := &http.Client{
		Timeout: c.clientTimeout(req),
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return manager.FetchResponse{}, err
		}

		httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL.String(), nil)
		if err != nil {
			return manager.FetchResponse{}, err
		}
		for k, vs := range req.Headers {
			for _, v := range vs {
				httpReq.Header.Add(k, v)
			}
		}
		ua := req.UserAgent
		if ua == "" && len(c.uaPool) > 0 {
			ua = c.uaPool[attempt%len(c.uaPool)]
		}
		if ua != "" {
			httpReq.Header.Set("User-Agent", ua)
		}

		resp, err := httpClient.Do(httpReq)
		if err != nil {
			lastErr = err
			if isRetryableError(err) && attempt < attempts-1 {
				if werr := waitBackoff(ctx, attempt); werr != nil {
					return manager.FetchResponse{}, werr
				}
				continue
			}
			return manager.FetchResponse{}, err
		}

		if isRetryableStatus(resp.StatusCode) && attempt < attempts-1 {
			resp.Body.Close()
			if werr := waitBackoff(ctx, attempt); werr != nil {
				return manager.FetchResponse{}, werr
			}
			continue
		}

		body, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if rerr != nil {
			return manager.FetchResponse{}, rerr
		}
		finalURL := req.URL.String()
		if resp.Request != nil && resp.Request.URL != nil {
			finalURL = resp.Request.URL.String()
		}
		contentType := resp.Header.Get("Content-Type")
		return manager.FetchResponse{
			StatusCode:  resp.StatusCode,
			Headers:     resp.Header,
			Body:        body,
			FinalURL:    finalURL,
			ContentType: contentType,
		}, nil
	}
	return manager.FetchResponse{}, lastErr
}

func (c *netHTTPClient) clientTimeout(req manager.FetchRequest) time.Duration {
	if req.Timeout > 0 {
		return req.Timeout
	}
	return c.baseTimeout
}

func isRetryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || (code >= 500 && code < 600)
}

func isRetryableError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		if ne.Timeout() {
			return true
		}
	}
	return false
}

func waitBackoff(ctx context.Context, attempt int) error {
	base := defaultBackoffBase
	backoff := base << attempt
	if backoff > defaultBackoffMax {
		backoff = defaultBackoffMax
	}
	timer := time.NewTimer(backoff)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
