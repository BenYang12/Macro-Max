// Package config loads runtime settings from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds the application settings used at startup.
type Config struct {
	Port               string
	DatabaseURL        string
	RedisURL           string
	FDCAPIKey          string
	SolverAddr         string
	KrogerClientID     string
	KrogerClientSecret string
	AnthropicAPIKey    string
	// WebAppURL is intentionally required with Kroger credentials. It has no
	// code fallback so a production deployment cannot redirect OAuth to localhost.
	WebAppURL string
}

// Addr returns the HTTP listen address.
func (c Config) Addr() string {
	return ":" + c.Port
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// LoadFromEnv applies local defaults to non-secret settings and validates them.
func LoadFromEnv() (Config, error) {
	cfg := Config{
		Port:        envOr("PORT", "4000"),
		DatabaseURL: envOr("DATABASE_URL", "postgres://macrocart:macrocart@localhost:5432/macrocart?sslmode=disable"),
		RedisURL:    envOr("REDIS_URL", "redis://localhost:6379/0"),

		FDCAPIKey:  os.Getenv("FDC_API_KEY"),
		SolverAddr: envOr("SOLVER_ADDR", "localhost:50051"),

		KrogerClientID:     os.Getenv("KROGER_CLIENT_ID"),
		KrogerClientSecret: os.Getenv("KROGER_CLIENT_SECRET"),
		AnthropicAPIKey:    os.Getenv("ANTHROPIC_API_KEY"),
		WebAppURL:          os.Getenv("WEB_APP_URL"),
	}
	if cfg.KrogerClientID != "" && cfg.KrogerClientSecret != "" && cfg.WebAppURL == "" {
		return Config{}, fmt.Errorf("WEB_APP_URL is required when Kroger cart credentials are configured")
	}

	port, err := strconv.Atoi(cfg.Port)
	if err != nil || port < 1 || port > 65535 {
		return Config{}, fmt.Errorf("invalid PORT %q: must be a number between 1 and 65535", cfg.Port)
	}

	return cfg, nil
}
