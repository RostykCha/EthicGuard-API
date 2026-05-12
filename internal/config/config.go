// Package config is the single source of truth for every environment
// variable EthicGuard-API reads. main.go calls Load() once at startup and
// passes the resulting struct down; nothing else in the codebase should
// read os.Getenv directly.
//
// Invariant: when in env != "dev", required fields (database URL, Anthropic
// key, installer secret) must be present — Load returns an error rather
// than silently defaulting.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Env                 string
	HTTPAddr            string
	LogLevel            string
	DatabaseURL         string
	AnthropicAPIKey     string
	AnthropicModel      string
	AnthropicModelHeavy string
	WorkerConcurrency   int
	// JobTimeout bounds how long a single analysis.Run call may take inside
	// a worker goroutine. Without it a stuck Anthropic call would pin a
	// worker forever. Configurable via ETHICGUARD_JOB_TIMEOUT (default 90s).
	JobTimeout  time.Duration
	JWTAudience string
	// InstallerSecret is the pre-shared HS256 signing key for Forge lifecycle
	// webhooks. The Forge app and the API both know this; it bootstraps auth
	// before any installation-specific shared secret exists.
	InstallerSecret string
}

func Load() (*Config, error) {
	cfg := &Config{
		Env:                 getenv("ETHICGUARD_ENV", "dev"),
		HTTPAddr:            httpAddrFromEnv(),
		LogLevel:            getenv("ETHICGUARD_LOG_LEVEL", "info"),
		DatabaseURL:         os.Getenv("ETHICGUARD_DATABASE_URL"),
		AnthropicAPIKey:     os.Getenv("ETHICGUARD_ANTHROPIC_API_KEY"),
		AnthropicModel:      getenv("ETHICGUARD_ANTHROPIC_MODEL", "claude-sonnet-4-6"),
		AnthropicModelHeavy: getenv("ETHICGUARD_ANTHROPIC_MODEL_HEAVY", "claude-opus-4-6"),
		JWTAudience:         getenv("ETHICGUARD_JWT_AUDIENCE", "ethicguard-api"),
		InstallerSecret:     os.Getenv("ETHICGUARD_INSTALLER_SECRET"),
	}

	concurrency, err := strconv.Atoi(getenv("ETHICGUARD_WORKER_CONCURRENCY", "4"))
	if err != nil {
		return nil, fmt.Errorf("invalid ETHICGUARD_WORKER_CONCURRENCY: %w", err)
	}
	if concurrency < 1 {
		return nil, errors.New("ETHICGUARD_WORKER_CONCURRENCY must be >= 1")
	}
	cfg.WorkerConcurrency = concurrency

	// JobTimeout: bounds analysis.Run inside a worker. Accept Go duration
	// strings (e.g. "90s", "2m"). Default 90s — comfortable for Claude
	// sonnet on a typical AC, well below the Render request budget.
	jobTimeoutStr := getenv("ETHICGUARD_JOB_TIMEOUT", "90s")
	jobTimeout, err := time.ParseDuration(jobTimeoutStr)
	if err != nil {
		return nil, fmt.Errorf("invalid ETHICGUARD_JOB_TIMEOUT: %w", err)
	}
	if jobTimeout <= 0 {
		return nil, errors.New("ETHICGUARD_JOB_TIMEOUT must be > 0")
	}
	cfg.JobTimeout = jobTimeout

	if cfg.Env != "dev" {
		if cfg.DatabaseURL == "" {
			return nil, errors.New("ETHICGUARD_DATABASE_URL is required outside dev")
		}
		if cfg.AnthropicAPIKey == "" {
			return nil, errors.New("ETHICGUARD_ANTHROPIC_API_KEY is required outside dev")
		}
		if cfg.InstallerSecret == "" {
			return nil, errors.New("ETHICGUARD_INSTALLER_SECRET is required outside dev")
		}
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// httpAddrFromEnv resolves the listen address. ETHICGUARD_HTTP_ADDR wins; if
// unset, fall back to PORT (Render and most managed PaaS inject this); finally
// default to :8080 for local dev.
func httpAddrFromEnv() string {
	if v, ok := os.LookupEnv("ETHICGUARD_HTTP_ADDR"); ok && v != "" {
		return v
	}
	if v, ok := os.LookupEnv("PORT"); ok && v != "" {
		return ":" + v
	}
	return ":8080"
}
