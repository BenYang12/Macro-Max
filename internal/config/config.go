// Package config loads runtime settings from environment variables.
package config

import (
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
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
	RecipeAccessKey    string
	TrustedProxyCIDRs  []netip.Prefix
	// WebAppURL is the browser origin the Kroger OAuth callback redirects to.
	// It has no code fallback, so a production deployment cannot silently
	// redirect OAuth to localhost. The requirement is enforced where it
	// applies — handler.NewCartHandler, reached via server.New — rather than
	// here, because loading config is not the same thing as serving the cart
	// routes. cmd/krogeringest also needs Kroger credentials and never serves
	// an OAuth callback; enforcing the pair here made every scheduled price
	// refresh exit before its first request.
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
		RecipeAccessKey:    os.Getenv("RECIPE_ACCESS_KEY"),
		WebAppURL:          os.Getenv("WEB_APP_URL"),
	}
	if cfg.AnthropicAPIKey != "" && cfg.RecipeAccessKey == "" {
		return Config{}, fmt.Errorf("RECIPE_ACCESS_KEY is required when ANTHROPIC_API_KEY is configured")
	}
	for _, raw := range strings.Split(os.Getenv("TRUSTED_PROXY_CIDRS"), ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid TRUSTED_PROXY_CIDRS entry %q: %w", raw, err)
		}
		cfg.TrustedProxyCIDRs = append(cfg.TrustedProxyCIDRs, prefix)
	}
	port, err := strconv.Atoi(cfg.Port)
	if err != nil || port < 1 || port > 65535 {
		return Config{}, fmt.Errorf("invalid PORT %q: must be a number between 1 and 65535", cfg.Port)
	}

	return cfg, nil
}
