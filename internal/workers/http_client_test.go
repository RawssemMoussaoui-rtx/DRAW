package workers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"draw/internal/manager"
)

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFetch_RetriesOn500(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(atomic.AddInt32(&attempts, 1))
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	client := NewFetchClient(FetchClientOpts{
		MaxAttempts:  3,
		BaseTimeout:  5 * time.Second,
		MaxRedirects: 5,
	})
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Fetch(context.Background(), manager.FetchRequest{URL: u, Method: http.MethodGet})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if atomic.LoadInt32(&attempts) != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}

func TestFetch_RetriesOn429(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(atomic.AddInt32(&attempts, 1))
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewFetchClient(FetchClientOpts{
		MaxAttempts:  3,
		BaseTimeout:  5 * time.Second,
		MaxRedirects: 5,
	})
	u, _ := url.Parse(srv.URL)
	resp, err := client.Fetch(context.Background(), manager.FetchRequest{URL: u, Method: http.MethodGet})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after 429 retry, got %d", resp.StatusCode)
	}
	if atomic.LoadInt32(&attempts) != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
}

func TestFetch_UARotatesRoundRobin(t *testing.T) {
	var mu sync.Mutex
	var got []string
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(atomic.AddInt32(&attempts, 1))
		mu.Lock()
		got = append(got, r.Header.Get("User-Agent"))
		mu.Unlock()
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewFetchClient(FetchClientOpts{
		UserAgentPool: []string{"uaA", "uaB"},
		MaxAttempts:   3,
		BaseTimeout:   5 * time.Second,
		MaxRedirects:  5,
	})
	u, _ := url.Parse(srv.URL)
	resp, err := client.Fetch(context.Background(), manager.FetchRequest{URL: u, Method: http.MethodGet})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	want := []string{"uaA", "uaB", "uaA"}
	if !equalSlice(got, want) {
		t.Fatalf("UA sequence = %v, want %v", got, want)
	}
}

func TestFetch_TimeoutEnforced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewFetchClient(FetchClientOpts{
		MaxAttempts:  1,
		BaseTimeout:  5 * time.Second,
		MaxRedirects: 5,
	})
	u, _ := url.Parse(srv.URL)
	start := time.Now()
	_, err := client.Fetch(context.Background(), manager.FetchRequest{URL: u, Method: http.MethodGet, Timeout: 200 * time.Millisecond})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed > 1*time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
}

func TestFetch_RedirectFollowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("done"))
			return
		}
		http.Redirect(w, r, "/final", http.StatusFound)
	}))
	defer srv.Close()

	client := NewFetchClient(FetchClientOpts{
		MaxAttempts:  1,
		BaseTimeout:  5 * time.Second,
		MaxRedirects: 5,
	})
	u, _ := url.Parse(srv.URL)
	resp, err := client.Fetch(context.Background(), manager.FetchRequest{URL: u, Method: http.MethodGet})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !strings.HasSuffix(resp.FinalURL, "/final") {
		t.Fatalf("expected FinalURL to end with /final, got %q", resp.FinalURL)
	}
}

func TestFetch_MaxRedirectsStops(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(atomic.AddInt32(&attempts, 1))
		if n > 20 {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/", http.StatusFound)
	}))
	defer srv.Close()

	client := NewFetchClient(FetchClientOpts{
		MaxAttempts:  1,
		BaseTimeout:  5 * time.Second,
		MaxRedirects: 2,
	})
	u, _ := url.Parse(srv.URL)
	resp, err := client.Fetch(context.Background(), manager.FetchRequest{URL: u, Method: http.MethodGet})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302 (redirect stopped at limit), got %d", resp.StatusCode)
	}
	if atomic.LoadInt32(&attempts) != 3 {
		t.Fatalf("expected 3 server hits (original + 2 redirects), got %d", attempts)
	}
}

func TestFetch_Determinism(t *testing.T) {
	runOnce := func() ([]string, int, error) {
		var mu sync.Mutex
		var got []string
		var attempts int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := int(atomic.AddInt32(&attempts, 1))
			mu.Lock()
			got = append(got, r.Header.Get("User-Agent"))
			mu.Unlock()
			if n <= 2 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		client := NewFetchClient(FetchClientOpts{
			UserAgentPool: []string{"uaA", "uaB"},
			MaxAttempts:   3,
			BaseTimeout:   5 * time.Second,
			MaxRedirects:  5,
		})
		u, _ := url.Parse(srv.URL)
		resp, err := client.Fetch(context.Background(), manager.FetchRequest{URL: u, Method: http.MethodGet})
		if err != nil {
			return nil, 0, err
		}
		return got, resp.StatusCode, nil
	}
	first, code, err := runOnce()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		uas, c, err := runOnce()
		if err != nil {
			t.Fatal(err)
		}
		if !equalSlice(uas, first) {
			t.Fatalf("run %d UA sequence %v != first %v", i+2, uas, first)
		}
		if c != code {
			t.Fatalf("status mismatch run %d: %d != %d", i+2, c, code)
		}
	}
}

func TestFetch_DenylistBlocked(t *testing.T) {
	client := NewFetchClient(FetchClientOpts{
		MaxAttempts:    1,
		BaseTimeout:    5 * time.Second,
		MaxRedirects:   5,
		SourceRegistry: newDeniedSourceRegistry("denied.test"),
	})
	u, _ := url.Parse("https://denied.test/x")
	resp, err := client.Fetch(context.Background(), manager.FetchRequest{URL: u, Method: http.MethodGet})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestFetch_AllowedPassesThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewFetchClient(FetchClientOpts{
		MaxAttempts:    1,
		BaseTimeout:    5 * time.Second,
		MaxRedirects:   5,
		SourceRegistry: newAllowedSourceRegistry("allowed.test"),
	})
	u, _ := url.Parse(srv.URL)
	resp, err := client.Fetch(context.Background(), manager.FetchRequest{URL: u, Method: http.MethodGet})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestFetch_NilRegistryPassesThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewFetchClient(FetchClientOpts{
		MaxAttempts:  1,
		BaseTimeout:  5 * time.Second,
		MaxRedirects: 5,
	})
	u, _ := url.Parse(srv.URL)
	resp, err := client.Fetch(context.Background(), manager.FetchRequest{URL: u, Method: http.MethodGet})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}
