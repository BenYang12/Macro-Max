// Package config centralizes all runtime configuration for the app.
//
// Why a whole package for this? Two reasons:
//  1. One place to look. Every knob the app has (port, database, redis, and
//     later: API keys, solver address) is declared in the Config struct below.
//     No hunting through the codebase for stray os.Getenv calls.
//  2. Testability. LoadFromEnv is a PURE-ish function: env vars in, Config
//     struct out. That makes it trivial to unit test (see config_test.go),
//     whereas config reads scattered through main() are untestable.
//
// The convention here is the "12-factor app" style: config comes from
// environment variables, so the same compiled binary runs locally, in CI,
// and in production with no code changes — only the environment differs.
package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds every setting the application needs at startup.
//
// The fields are plain strings — we keep parsing/validation minimal here and
// let each subsystem interpret its own value (pgx parses DatabaseURL, the
// redis client parses RedisURL). The one thing we DO validate is Port,
// because a bad port fails in a confusing way otherwise.
type Config struct {
	// Port is the TCP port the HTTP server listens on, e.g. "4000".
	// Stored as a string because that's how it's used (":" + Port), but
	// validated as a number in LoadFromEnv.
	Port string

	// DatabaseURL is a Postgres connection string (a "DSN"), e.g.
	// postgres://user:password@host:5432/dbname?sslmode=disable
	// The default points at the Postgres container from docker-compose.yml.
	DatabaseURL string

	// RedisURL is the Redis connection string, e.g. redis://localhost:6379/0
	// (the trailing /0 selects Redis's database number 0 — Redis has 16
	// numbered keyspaces by default). Unused until we add caching; declared
	// now so the config surface is complete from day one.
	RedisURL string
}

// Addr returns the listen address for http.Server, e.g. ":4000".
// The ":" prefix means "every network interface on this machine".
//
// Note the receiver syntax: `func (c Config) Addr()` declares a METHOD on
// Config. `c` is like `self` in Python, except you name it yourself (short
// names are idiomatic). This is a value receiver — the method gets a copy of
// the struct, which is fine because it only reads.
func (c Config) Addr() string {
	return ":" + c.Port
}

// LoadFromEnv builds a Config from environment variables, applying local-dev
// defaults for anything unset. It returns an error (instead of calling
// log.Fatal itself) so the CALLER decides what to do — main() will exit, but
// a test can just assert on the error. Libraries return errors; only main
// gets to kill the process.
func LoadFromEnv() (Config, error) {
	cfg := Config{
		Port: envOr("PORT", "4000"),
		// This default matches the credentials in docker-compose.yml.
		// sslmode=disable is fine for localhost; production DSNs come from
		// the environment and will require TLS.
		DatabaseURL: envOr("DATABASE_URL", "postgres://macrocart:macrocart@localhost:5432/macrocart?sslmode=disable"),
		RedisURL:    envOr("REDIS_URL", "redis://localhost:6379/0"),
	}

	// Validate the port: must parse as an integer in the valid TCP port
	// range. strconv.Atoi ("ASCII to integer") returns two values — the
	// number and an error — which is Go's universal pattern for "this
	// operation can fail".
	port, err := strconv.Atoi(cfg.Port)
	if err != nil || port < 1 || port > 65535 {
		// fmt.Errorf builds an error value from a format string. %q wraps
		// the value in quotes, which makes empty/whitespace values visible
		// in the message. Returning Config{} (the zero value) alongside the
		// error is conventional: never make callers use a half-built value.
		return Config{}, fmt.Errorf("invalid PORT %q: must be a number between 1 and 65535", cfg.Port)
	}

	return cfg, nil
}

// envOr reads an environment variable and falls back to a default when it's
// unset or empty. (Moved here from cmd/api/main.go — config concerns live in
// the config package now.)
//
// os.Getenv returns "" both when the variable is unset and when it's set to
// an empty string — for our purposes those mean the same thing: "use the
// default". Note the lowercase name: in Go, lowercase identifiers are
// UNEXPORTED (private to this package). Only Config, Addr, and LoadFromEnv
// are part of this package's public API.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
