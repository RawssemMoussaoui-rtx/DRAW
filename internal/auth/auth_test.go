package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminTokenFromEnv(t *testing.T) {
	os.Setenv(EnvAdminToken, "super-secret-token")
	defer os.Unsetenv(EnvAdminToken)
	c := LoadConfig()
	if !c.AdminEnabled() {
		t.Fatal("expected AdminEnabled")
	}
	if !c.IsAdmin("super-secret-token") {
		t.Error("valid token should be accepted")
	}
	if c.IsAdmin("wrong-token") {
		t.Error("invalid token should be rejected")
	}
	if c.IsAdmin("") {
		t.Error("empty token should be rejected")
	}
}

func TestFailClosedWhenNoEnvToken(t *testing.T) {
	os.Unsetenv(EnvAdminToken)
	c := LoadConfig()
	if c.AdminEnabled() {
		t.Fatal("AdminEnabled must be false when no token")
	}
	if c.IsAdmin("anything") {
		t.Fatal("must fail-closed: no admin with empty token")
	}
}

func TestRequireAdmin(t *testing.T) {
	os.Setenv(EnvAdminToken, "valid-token")
	defer os.Unsetenv(EnvAdminToken)
	c := LoadConfig()

	called := false
	h := c.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	cases := []struct {
		name        string
		auth        string
		wantCode    int
		wantCalled  bool
	}{
		{"no bearer", "", http.StatusUnauthorized, false},
		{"wrong bearer", "Bearer nope", http.StatusForbidden, false},
		{"empty bearer", "Bearer ", http.StatusUnauthorized, false},
		{"valid bearer", "Bearer valid-token", http.StatusOK, true},
		{"wrong scheme", "Basic valid-token", http.StatusUnauthorized, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called = false
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			rec := httptest.NewRecorder()
			h(rec, req)
			if rec.Code != tc.wantCode {
				t.Errorf("code: got %d want %d", rec.Code, tc.wantCode)
			}
			if called != tc.wantCalled {
				t.Errorf("handler called: got %v want %v", called, tc.wantCalled)
			}
		})
	}
}

func TestNoPlaintextTokenFromConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "secrets.toml")
	if err := os.WriteFile(p, []byte(`AdminToken = "plaintext-in-file"\n`), 0600); err != nil {
		t.Fatal(err)
	}
	os.Setenv("DRAW_CONFIG", p)
	defer os.Unsetenv("DRAW_CONFIG")
	os.Unsetenv(EnvAdminToken)
	c := LoadConfig()
	if c.AdminEnabled() {
		t.Fatal("admin token must NOT be read from a TOML/file; ENV-only")
	}
	if c.IsAdmin("plaintext-in-file") {
		t.Fatal("plaintext token from file must not authorize")
	}
}

func TestRedactDoesNotLeakSecret(t *testing.T) {
	secret := "a-very-long-secret-value"
	red := Redact(secret)
	if strings.Contains(red, secret) {
		t.Fatalf("redacted form must not contain the full secret: %q", red)
	}
	if !strings.Contains(red, "***") {
		t.Fatalf("redacted form should mask: %q", red)
	}
	if Redact("ab") != "**" {
		t.Errorf("short token redaction wrong: %q", Redact("ab"))
	}
}

func TestDisabledSessionAuthDenies(t *testing.T) {
	s := DisabledSessionAuth()
	if s.Authorized("any-user") {
		t.Error("disabled session auth must deny all users")
	}
}
