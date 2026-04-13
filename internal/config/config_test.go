package config

import (
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("ETHICGUARD_ENV", "dev")
	t.Setenv("ETHICGUARD_HTTP_ADDR", "")
	t.Setenv("ETHICGUARD_WORKER_CONCURRENCY", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr default = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.AnthropicModel != "claude-sonnet-4-6" {
		t.Errorf("AnthropicModel default = %q, want claude-sonnet-4-6", cfg.AnthropicModel)
	}
	if cfg.WorkerConcurrency != 4 {
		t.Errorf("WorkerConcurrency default = %d, want 4", cfg.WorkerConcurrency)
	}
}

func TestLoadRequiresSecretsOutsideDev(t *testing.T) {
	t.Setenv("ETHICGUARD_ENV", "prod")
	t.Setenv("ETHICGUARD_DATABASE_URL", "")
	t.Setenv("ETHICGUARD_ANTHROPIC_API_KEY", "")

	if _, err := Load(); err == nil {
		t.Fatal("expected error when DATABASE_URL missing in prod")
	}
}

func TestLoadRejectsBadConcurrency(t *testing.T) {
	t.Setenv("ETHICGUARD_ENV", "dev")
	t.Setenv("ETHICGUARD_WORKER_CONCURRENCY", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for concurrency=0")
	}
}
