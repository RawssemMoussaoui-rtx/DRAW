package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"draw/internal/model"
)

func TestDefaultsMatchLockedSpec(t *testing.T) {
	c := Defaults()
	eq(t, c.MaxGlobalConcurrency, 10, "MAX_GLOBAL_CONCURRENCY start ~10")
	eq(t, c.CPUThreshold, 0.8, "CPU threshold ~0.8")
	eq(t, c.RAMThreshold, 0.8, "RAM threshold ~0.8")
	eq(t, c.Replan.KContradictions, 2, "K=2")
	eq(t, c.Replan.MMissingPrimary, 2, "M=2")
	eq(t, c.Replan.SStaleSources, 2, "S=2")
	eq(t, int(c.CostModel[model.TaskTypeDiscover]), 1, "DISCOVER cost 1")
	eq(t, int(c.CostModel[model.TaskTypeFetchHTTP]), 1, "HTTP cost 1")
	eq(t, int(c.CostModel[model.TaskTypeFetchBrowser]), 10, "Browser cost 10")
	eq(t, int(c.CostModel[model.TaskTypeVerify]), 1, "VERIFY cost 1")
	eq(t, int(c.CostModel[model.TaskTypeReconcile]), 3, "RECONCILE cost 3")
	eq(t, c.Upgrade.BrowserMaxConcurrency, 4, "browser cap")
	eq(t, c.Retry.MaxAttempts, 3, "retry attempts")
	eq(t, c.Retry.BackoffBase, time.Second, "retry base")
	eq(t, c.Retry.BackoffMax, time.Minute, "retry max")
	eq(t, c.HysteresisWindow, 10*time.Second, "hysteresis")
	eq(t, c.Cooldown, 5*time.Second, "cooldown")
	eq(t, c.SessionRetention, 24*time.Hour, "session retention")
}

func TestLoadDefaultsWhenNoFile(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\") err: %v", err)
	}
	eq(t, c.MaxGlobalConcurrency, 10, "default concurrency")
}

func TestEnvOverride(t *testing.T) {
	os.Setenv(EnvMaxGlobalConcurrency, "25")
	os.Setenv(EnvCPUThreshold, "0.65")
	os.Setenv(EnvRAMThreshold, "0.7")
	os.Setenv(EnvSessionRetention, "120h")
	os.Setenv(EnvBrowserMaxConcurrency, "8")
	defer os.Unsetenv(EnvMaxGlobalConcurrency)
	defer os.Unsetenv(EnvCPUThreshold)
	defer os.Unsetenv(EnvRAMThreshold)
	defer os.Unsetenv(EnvSessionRetention)
	defer os.Unsetenv(EnvBrowserMaxConcurrency)
	c, err := Load("")
	if err != nil {
		t.Fatalf("Load err: %v", err)
	}
	eq(t, c.MaxGlobalConcurrency, 25, "env concurrency")
	eq(t, c.CPUThreshold, 0.65, "env cpu")
	eq(t, c.RAMThreshold, 0.7, "env ram")
	eq(t, c.SessionRetention, 120*time.Hour, "env retention")
	eq(t, c.Upgrade.BrowserMaxConcurrency, 8, "env browser cap")
}

func TestTOMLOverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.toml")
	content := "MaxGlobalConcurrency = 7\n" +
		"SessionRetention = \"48h\"\n" +
		"[ScoringWeights]\n" +
		"ContradictionValue = 2.0\n" +
		"[Retry]\n" +
		"MaxAttempts = 5\n" +
		"BackoffBase = \"2s\"\n" +
		"BackoffMax = \"5m\"\n" +
		"[CostModel]\n" +
		"DISCOVER = 2\n" +
		"FETCH_HTTP = 3\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load err: %v", err)
	}
	eq(t, c.MaxGlobalConcurrency, 7, "toml concurrency")
	eq(t, c.ScoringWeights.ContradictionValue, 2.0, "toml weight")
	eq(t, c.Retry.MaxAttempts, 5, "toml attempts")
	eq(t, c.Retry.BackoffBase, 2*time.Second, "toml backoff base")
	eq(t, c.Retry.BackoffMax, 5*time.Minute, "toml backoff max")
	eq(t, int(c.CostModel[model.TaskTypeDiscover]), 2, "toml discover cost override")
	eq(t, int(c.CostModel[model.TaskTypeFetchHTTP]), 3, "toml http cost override")
	eq(t, int(c.CostModel[model.TaskTypeFetchBrowser]), 10, "default browser cost retained")
	eq(t, c.SessionRetention, 48*time.Hour, "toml retention")
}

func TestLoadRejectsUnknownCostKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.toml")
	content := "[CostModel]\nBADKEY = 1\n"
	os.WriteFile(path, []byte(content), 0600)
	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error for unknown TaskType cost key")
	}
}

func TestValidateRejectsInvalid(t *testing.T) {
	c := Defaults()
	c.MaxGlobalConcurrency = 0
	if err := c.Validate(); err == nil {
		t.Error("expected error for zero concurrency")
	}
	c = Defaults()
	c.CPUThreshold = 1.5
	if err := c.Validate(); err == nil {
		t.Error("expected error for bad cpu threshold")
	}
	c = Defaults()
	c.SessionRetention = 0
	if err := c.Validate(); err == nil {
		t.Error("expected error for zero retention")
	}
}

func TestCostModelNeverNilAfterLoad(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.CostModel == nil {
		t.Fatal("CostModel must not be nil")
	}
	if c.DomainLimits == nil {
		t.Fatal("DomainLimits must not be nil")
	}
}

func eq[T comparable](t *testing.T, got, want T, msg string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %v want %v", msg, got, want)
	}
}
