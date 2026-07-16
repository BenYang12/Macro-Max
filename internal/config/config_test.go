package config

import "testing"

// TestLoadFromEnvDefaults checks that when NO environment variables are set,
// we get sensible local-development defaults. This is the "zero config"
// experience: clone the repo, `make run`, and it just works.
func TestLoadFromEnvDefaults(t *testing.T) {
	// t.Setenv sets an environment variable FOR THIS TEST ONLY and restores
	// the old value when the test finishes. This matters because env vars are
	// process-global state — without t.Setenv, one test could leak PORT=9999
	// into the next test and cause flaky, order-dependent failures.
	//
	// Setting them to "" simulates "unset" for our purposes (os.Getenv can't
	// tell the difference between unset and empty, and we treat both as
	// "use the default").
	t.Setenv("PORT", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("REDIS_URL", "")

	cfg, err := LoadFromEnv()
	if err != nil {
		// t.Fatalf stops this test immediately — if loading failed, checking
		// individual fields would just produce noise.
		t.Fatalf("LoadFromEnv() with defaults should not error, got: %v", err)
	}

	if cfg.Port != "4000" {
		t.Errorf("default Port: want %q, got %q", "4000", cfg.Port)
	}
	// The default DSN points at the Docker Compose Postgres (see
	// docker-compose.yml): user/password/db are all "macrocart",
	// listening on localhost:5432.
	wantDB := "postgres://macrocart:macrocart@localhost:5432/macrocart?sslmode=disable"
	if cfg.DatabaseURL != wantDB {
		t.Errorf("default DatabaseURL: want %q, got %q", wantDB, cfg.DatabaseURL)
	}
	if cfg.RedisURL != "redis://localhost:6379/0" {
		t.Errorf("default RedisURL: want %q, got %q", "redis://localhost:6379/0", cfg.RedisURL)
	}
}

// TestLoadFromEnvOverrides checks that environment variables win over the
// defaults — the whole point of env-var config is that deploys can change
// behavior without recompiling.
func TestLoadFromEnvOverrides(t *testing.T) {
	t.Setenv("PORT", "9000")
	t.Setenv("DATABASE_URL", "postgres://prod-db/macrocart")
	t.Setenv("REDIS_URL", "redis://prod-redis:6379/1")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() with valid overrides should not error, got: %v", err)
	}

	if cfg.Port != "9000" {
		t.Errorf("Port override: want %q, got %q", "9000", cfg.Port)
	}
	if cfg.DatabaseURL != "postgres://prod-db/macrocart" {
		t.Errorf("DatabaseURL override: want %q, got %q", "postgres://prod-db/macrocart", cfg.DatabaseURL)
	}
	if cfg.RedisURL != "redis://prod-redis:6379/1" {
		t.Errorf("RedisURL override: want %q, got %q", "redis://prod-redis:6379/1", cfg.RedisURL)
	}
}

// TestLoadFromEnvRejectsBadPort checks input validation: PORT must be a
// number. Catching a typo like PORT=40o0 at startup (with a clear error)
// beats a confusing failure later when the server tries to listen.
func TestLoadFromEnvRejectsBadPort(t *testing.T) {
	// A "table-driven test": instead of copy-pasting the same test three
	// times, we put the varying inputs in a slice and loop. This is THE
	// idiomatic Go testing pattern — you'll see it everywhere.
	badPorts := []string{"40o0", "-1", "70000", "http"}

	for _, port := range badPorts {
		// t.Run creates a named subtest. `go test -v` shows each one
		// individually, and a failure tells you exactly which input broke.
		t.Run(port, func(t *testing.T) {
			t.Setenv("PORT", port)

			_, err := LoadFromEnv()
			if err == nil {
				t.Errorf("LoadFromEnv() with PORT=%q should return an error, got nil", port)
			}
		})
	}
}

// TestAddr checks the little helper that turns a port into a listen address.
func TestAddr(t *testing.T) {
	cfg := Config{Port: "4000"}
	if got := cfg.Addr(); got != ":4000" {
		t.Errorf("Addr(): want %q, got %q", ":4000", got)
	}
}
