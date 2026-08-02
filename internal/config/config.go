// Package config centralizes all runtime configuration for the app.

// Why a whole package?
// 1. One place to look. Every knob the app has is declared in the Config struct below...
// 2. Testability. LoadFromEnv is simply env vars in, Config struct out. That makes it trivial to unit test (see config_test.go),

// My design choice: "12-factor app" style -> config comes from environment variables, so the same compiled binary runs locally, in CI, and in production with no code changes.
package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds every setting the application needs at startup.
// fields -> plain strings
// keep parsing/validation minimal here and let each subsystem interpret its own value
// e.g. pgx parses DatabaseURL, redis client parses RedisURL
// ONLY validate Port
type Config struct {
	// store as string, (":" + Port)
	// capital name = public
	Port string

	// Postgres connection string (e.g. postgres://user:password@host:5432/dbname?sslmode=disable)
	// default points at the Postgres container from docker-compose.yml.
	DatabaseURL string

	// RedisURL is the Redis connection string e.g. redis://localhost:6379/0 -> recall, the trailing /0 select's Redis's database number 0
	// Redis has 16 numbered keyspaces by default. Unused until we add caching; declared now so the config surface is complete from day one.
	// Each keyspace represents the key for one database, and there are essentially 16 databases.
	RedisURL string

	// FDCAPIKey is the USDA FoodData Central key (Phase 2), used only by
	// cmd/fdcimport.
	//
	// NOTE THE MISSING DEFAULT. Every field above falls back to a working
	// local value, because a wrong Postgres URL is obvious and harmless. A
	// SECRET is different: there is no safe placeholder, and inventing one
	// would turn "you forgot to set the key" into a confusing 403 from USDA.
	// So it defaults to "" and the ONE command that needs it checks for
	// emptiness and exits with a clear message. Validate secrets where they
	// are used, not where they are loaded — otherwise `make run` would refuse
	// to start the API over a key the API never touches.
	FDCAPIKey string

	// SolverAddr is host:port of the Python OR-Tools gRPC service (Phase 3).
	// Unlike the API key this DOES get a default, because it isn't a secret and
	// there is one obviously-right local value — the same reasoning as
	// DATABASE_URL. In Compose this becomes "solver:50051" (the service name is
	// the hostname on a Docker network); on Fly it becomes a .internal address.
	SolverAddr string

	// Kroger developer app credentials (Phase 5), used only by
	// cmd/krogeringest. Secrets, so no defaults — same rule as FDCAPIKey.
	KrogerClientID     string
	KrogerClientSecret string

	// KrogerZip is the default zip for store lookup. NOT a secret, so it gets a
	// default; override in .env with the store I actually shop at.
	KrogerZip string

	// KrogerLocationID is the resolved store to solve against. Empty until I've
	// run `krogeringest -zip` once and picked one.
	KrogerLocationID string

	// ---------------------------------------------------------------- Phase 7

	// AnthropicAPIKey powers POST /v1/recipes. Secret, so no default — and the
	// same graceful-degradation rule as the solver applies at the routing
	// layer: if this is empty, the recipes route simply isn't registered and
	// everything else keeps working. Recipes are a FINISHING layer; the solver
	// is the product, and the product must not depend on an LLM being reachable.
	AnthropicAPIKey string

	// KrogerRedirectURI is the callback Kroger sends the user back to after
	// they authorize cart access. It must match the value registered on the
	// developer app EXACTLY — scheme, host, port, path, trailing slash. A
	// mismatch is the single most common OAuth failure, and Kroger reports it
	// as a generic error rather than telling you which part disagreed.
	//
	// Not a secret (it appears in a browser URL bar), so it gets a local default.
	KrogerRedirectURI string

	// TokenEncryptionKey encrypts the Kroger user refresh token before it goes
	// in Postgres. 32 bytes, hex-encoded (64 hex characters):
	//
	//     openssl rand -hex 32
	//
	// WHY ENCRYPT AT ALL: a client-credentials token (Phase 5) grants access to
	// a public product catalog. A user's authorization-code refresh token
	// grants long-lived write access to a REAL PERSON'S CART. Those are not the
	// same risk, and a database dump should not hand over the second one.
	//
	// No default, and deliberately no auto-generated fallback: a key that
	// changes on restart would silently make every stored token undecryptable,
	// which fails as confusing "invalid token" errors much later.
	TokenEncryptionKey string
}

// http.Server is a struct in Go's built-in net/http package that a program that listens for web requests from clients (like a browser) and sends back responses
// workflow:
// 1. Handler: I first write a function or struct that implements the http.Handler interface to process the incoming requests. http.Handler receives two parameters: http.ResponseWriter (to send reply) and *http.Request (which contains details of the clients request)
// 2. Multiplexer: server uses a router to map specific URL paths (like /hello) to my handler
// 3. Listen and ListenAndServe: I pass my router and network address to http.ListenAndServe() to start the server, which listens for requests and automatically handles each one in its own goroutine

// Addr() is a method that returns the listen address for http.Server, e.g. ":4000".
// value receiver -> method receives copy. read on small structs, everything else, pass in a pointer -> func (c *Config)
func (c Config) Addr() string {
	return ":" + c.Port
}

// envOr reads an env variable and falls back to a default when its null
// I originally had this in main, but moved it here to enforce separation of concerns
// os.Getenv returns "" both when the variable is unset and when it's set to
// an empty string.
// Note the lowercase name: in Go, lowercase identifiers are UNEXPORTED (private to this package). Only Config, Addr, and LoadFromEnv
// are part of this package's public API.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback

}

// LoadFromEnv builds a Config from env vars, applying local-dev defaults for anything unset (uphold 12-factor app style)
// I want it to return an error, so CALLER decides what to do -> main() will exit, but a I want to use a test to assert on the error
// General pattern: Libraries return errors; only main can kill the process
func LoadFromEnv() (Config, error) {
	cfg := Config{
		Port: envOr("PORT", "4000"),
		// This default matches the credentials in docker-compose.yml.
		// sslmode=disable is fine for localhost; production DSNs come from the environment and will require TLS.
		DatabaseURL: envOr("DATABASE_URL", "postgres://macrocart:macrocart@localhost:5432/macrocart?sslmode=disable"),
		RedisURL:    envOr("REDIS_URL", "redis://localhost:6379/0"),

		// No fallback: a secret has no sensible default. os.Getenv returns ""
		// when unset, which is exactly the "absent" value cmd/fdcimport checks.
		FDCAPIKey:  os.Getenv("FDC_API_KEY"),
		SolverAddr: envOr("SOLVER_ADDR", "localhost:50051"),

		KrogerClientID:     os.Getenv("KROGER_CLIENT_ID"),
		KrogerClientSecret: os.Getenv("KROGER_CLIENT_SECRET"),
		// Chapel Hill, NC — my campus. The nearby stores are Harris Teeter,
		// which Kroger owns; see the note in .env.example about why that's
		// worth verifying rather than assuming.
		KrogerZip: envOr("KROGER_ZIP", "27514"),
		// Harris Teeter University Place, 2110 S Estes Dr — the store closest
		// to campus, and the one my users would actually walk to. Verified to
		// return live prices through the Kroger API; see .env.example.
		KrogerLocationID: envOr("KROGER_LOCATION_ID", "09700117"),

		// Phase 7. Both secrets get no default, for the reason spelled out on
		// FDCAPIKey; the redirect URI isn't a secret, so it gets the local one.
		AnthropicAPIKey:    os.Getenv("ANTHROPIC_API_KEY"),
		KrogerRedirectURI:  envOr("KROGER_REDIRECT_URI", "http://localhost:4000/v1/kroger/callback"),
		TokenEncryptionKey: os.Getenv("TOKEN_ENCRYPTION_KEY"),
	}

	// recall: I need to validate the port
	// I must parse as an integer in the valid TCP port
	// range . strconv.Atoi ("ASCII to integer") returns the number and an error
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
