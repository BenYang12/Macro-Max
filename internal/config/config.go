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
type Config struct{
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
}


// http.Server is a struct in Go's built-in net/http package that a program that listens for web requests from clients (like a browser) and sends back responses
// workflow:
// 1. Handler: I first write a function or struct that implements the http.Handler interface to process the incoming requests. http.Handler receives two parameters: http.ResponseWriter (to send reply) and *http.Request (which contains details of the clients request)
// 2. Multiplexer: server uses a router to map specific URL paths (like /hello) to my handler
// 3. Listen and ListenAndServe: I pass my router and network address to http.ListenAndServe() to start the server, which listens for requests and automatically handles each one in its own goroutine

// Addr() is a method that returns the listen address for http.Server, e.g. ":4000".
// value receiver -> method receives copy. read on small structs, everything else, pass in a pointer -> func (c *Config)
func (c Config) Addr() string{
	return ":" + c.Port
}


// envOr reads an env variable and falls back to a default when its null
// I originally had this in main, but moved it here to enforce separation of concerns 
// os.Getenv returns "" both when the variable is unset and when it's set to
// an empty string.
// Note the lowercase name: in Go, lowercase identifiers are UNEXPORTED (private to this package). Only Config, Addr, and LoadFromEnv
// are part of this package's public API.
func envOr(key, fallback string) string{
	if v := os.Getenv(key); v != ""{
		return v
	}
	return fallback

}




// LoadFromEnv builds a Config from env vars, applying local-dev defaults for anything unset (uphold 12-factor app style)
// I want it to return an error, so CALLER decides what to do -> main() will exit, but a I want to use a test to assert on the error
// General pattern: Libraries return errors; only main can kill the process
func LoadFromEnv() (Config, error){
	cfg := Config{
		Port: envOr("PORT", "4000"),
		// This default matches the credentials in docker-compose.yml.
		// sslmode=disable is fine for localhost; production DSNs come from the environment and will require TLS.
		DatabaseURL: envOr("DATABASE_URL", "postgres://macrocart:macrocart@localhost:5432/macrocart?sslmode=disable"),
		RedisURL:    envOr("REDIS_URL", "redis://localhost:6379/0"),
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
