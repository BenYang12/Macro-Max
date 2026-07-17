// Package main is the entry point for the API server binary. In Go, every
// runnable program has a `main` package with a `main` function. Everything
// else is a library.

// Keep main.go small: its job is to read config, construct dependencies, and
// start the server. All the interesting logic lives in internal/ packages
// where it can be tested. main() itself is nearly untestable (it runs
// forever and calls log.Fatal), so the less it does, the better.
package main

import (
	"log"

	// Import paths for our own packages. These must match the module name in
	// go.mod exactly: module name + path from the repo root to the package
	// directory. Go resolves "github.com/BenYang12/Macro-Max/internal/config"
	// to the local ./internal/config directory because go.mod says this repo
	// IS that module — no network involved.
	"github.com/BenYang12/Macro-Max/internal/config"
	"github.com/BenYang12/Macro-Max/internal/server"
)

func main() {

	// Load all config fron env vars (with local-dev defaults)
	// Load all configuration from environment variables (with local-dev defaults)

	// Execute: `PORT=9000 go run ./cmd/api` starts on :9000.
	
	// This is the ONLY place in the whole program that's allowed to react to
	// a config error by exiting. log.Fatal prints the message and calls
	// os.Exit(1) — the right behavior at startup, where there's nothing to
	// recover to.
	cfg,err := config.LoadFromEnv()
	if err != nil {
		log.Fatal(err)
	}

	//server.New() initializes and returns a new server instance
	srv := server.New(cfg.Addr())
	log.Printf("starting server on %s", cfg.Addr())

	// ListenAndServe blocks until the server stops. If it returns, something
	// went wrong (usually: the port is already in use).
	log.Fatal(srv.ListenAndServe())
}
