package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
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
	JWTAudience         string
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
