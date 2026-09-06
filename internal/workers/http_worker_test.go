package workers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"draw/internal/model"
)

func TestHTTPWorker_Run_StatusMapping(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		want     model.RetrievalStatus
		wantData bool
	}{
		{"success", http.StatusOK, "hello", model.RetrievalStatusSuccess, true},
		{"auth", http.StatusUnauthorized, "", model.RetrievalStatusAuthRequired, false},
		{"forbidden", http.StatusForbidden, "", model.RetrievalStatusBlocked, false},
		{"server_error", http.StatusServiceUnavailable, "", model.RetrievalStatusTimeout, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(tc.status)
				if tc.body != "" {
					w.Write([]byte(tc.body))
				}
			}))
			defer srv.Close()

			u, _ := url.Parse(srv.URL)
			task := model.Task{URL: u}
			w := NewHTTPWorker(NewFetchClient(FetchClientOpts{
				MaxAttempts:  1,
				BaseTimeout:  5 * time.Second,
				MaxRedirects: 5,
			}), WorkerOpts{
				MaxAttempts:  1,
				Timeout:      5 * time.Second,
				MaxRedirects: 5,
			})
			result, err := w.Run(task)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Status != tc.want {
				t.Fatalf("status = %q, want %q", result.Status, tc.want)
			}
			if tc.wantData {
				if string(result.Data) != tc.body {
					t.Fatalf("data = %q, want %q", string(result.Data), tc.body)
				}
				if result.Headers == nil {
					t.Fatal("expected non-nil headers")
				}
			}
		})
	}
}

func TestHTTPWorker_Run_ConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	u, _ := url.Parse(srv.URL)
	task := model.Task{URL: u}
	w := NewHTTPWorker(NewFetchClient(FetchClientOpts{
		MaxAttempts:  1,
		BaseTimeout:  5 * time.Second,
		MaxRedirects: 5,
	}), WorkerOpts{
		MaxAttempts:  1,
		Timeout:      5 * time.Second,
		MaxRedirects: 5,
	})
	result, err := w.Run(task)
	if result.Status != model.RetrievalStatusInvalidContent {
		t.Fatalf("status = %q, want %q", result.Status, model.RetrievalStatusInvalidContent)
	}
	if err == nil {
		t.Fatal("expected non-nil error for connection refused")
	}
}

func TestHTTPWorker_Run_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	task := model.Task{URL: u}
	w := NewHTTPWorker(NewFetchClient(FetchClientOpts{
		MaxAttempts:  1,
		BaseTimeout:  5 * time.Second,
		MaxRedirects: 5,
	}), WorkerOpts{
		MaxAttempts:  1,
		Timeout:      200 * time.Millisecond,
		MaxRedirects: 5,
	})
	start := time.Now()
	result, err := w.Run(task)
	elapsed := time.Since(start)
	if result.Status != model.RetrievalStatusTimeout {
		t.Fatalf("status = %q, want %q", result.Status, model.RetrievalStatusTimeout)
	}
	if err == nil {
		t.Fatal("expected non-nil error for timeout")
	}
	if elapsed > 1*time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
}

func TestHTTPWorker_Run_Determinism(t *testing.T) {
	codes := []int{http.StatusOK, http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable, http.StatusNotFound}
	for _, code := range codes {
		var first model.RetrievalStatus
		for i := 0; i < 3; i++ {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
			}))
			u, _ := url.Parse(srv.URL)
			task := model.Task{URL: u}
			w := NewHTTPWorker(NewFetchClient(FetchClientOpts{
				MaxAttempts:  1,
				BaseTimeout:  5 * time.Second,
				MaxRedirects: 5,
			}), WorkerOpts{
				MaxAttempts:  1,
				Timeout:      5 * time.Second,
				MaxRedirects: 5,
			})
			result, _ := w.Run(task)
			srv.Close()
			if i == 0 {
				first = result.Status
			} else if result.Status != first {
				t.Fatalf("status %d run %d: %q != first %q", code, i, result.Status, first)
			}
		}
	}
}

func TestHTTPWorker_DenylistBlocked(t *testing.T) {
	u, _ := url.Parse("https://denied.test/x")
	task := model.Task{URL: u}
	w := NewHTTPWorker(NewFetchClient(FetchClientOpts{
		MaxAttempts:  1,
		BaseTimeout:  5 * time.Second,
		MaxRedirects: 5,
	}), WorkerOpts{
		MaxAttempts:  1,
		Timeout:      5 * time.Second,
		MaxRedirects: 5,
	}).WithSourceRegistry(newDeniedSourceRegistry("denied.test"))

	result, err := w.Run(task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != model.RetrievalStatusBlocked {
		t.Fatalf("status = %q, want %q", result.Status, model.RetrievalStatusBlocked)
	}
}

func TestHTTPWorker_RunContext_Cancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	task := model.Task{URL: u}
	w := NewHTTPWorker(NewFetchClient(FetchClientOpts{
		MaxAttempts:  1,
		BaseTimeout:  5 * time.Second,
		MaxRedirects: 5,
	}), WorkerOpts{
		MaxAttempts:  1,
		Timeout:      5 * time.Second,
		MaxRedirects: 5,
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	result, err := w.RunContext(ctx, task)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if elapsed > 1*time.Second {
		t.Fatalf("expected cancellation to abort quickly, took %v", elapsed)
	}
	if result.Status != model.RetrievalStatusInvalidContent {
		t.Fatalf("status = %q, want %q", result.Status, model.RetrievalStatusInvalidContent)
	}
}
