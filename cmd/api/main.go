// Package main is the entry point for the API server binary. In Go, every
// runnable program has a `main` package with a `main` function. Everything
// else is a library.

//Package main is entry point for the API server binary.
//In Go, every runnable program must be package "main", and it must have a func() main.
//When I run go run ./cmd.api, the runtime calls main() and the program lives until main() returns.
//EVERYTHING else is a LIBRARY

// Keep main.go small: its job is to read config, construct dependencies, and start
package main

import (
	"log"
	"os"

	// Import path for our own package. This must match the module name in
	// go.mod exactly: module name + path from the repo root to the package
	// directory. Go resolves "github.com/BenYang12/Macro-Max/internal/server"
	// to the local ./internal/server directory because go.mod says this repo
	// IS that module — no network involved.
	"github.com/BenYang12/Macro-Max/internal/server"
)

// envOr reads an environment variable and falls back to a default when it's
// unset or empty. Environment variables are how deployed apps get their
// config (the "12-factor app" convention): the same compiled binary can run
// on port 4000 locally and port 8080 in production, with no code change.
//
// os.Getenv returns "" both when the variable is unset and when it's set to
// an empty string — for our purposes those mean the same thing: "use the
// default".

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != ""{
		return v
	}
	return fallback
}


func main() {
	// Read the port from the PORT environment variable, defaulting to 4000.
	// Try it: `PORT=9000 go run ./cmd/api` starts the server on :9000.
	// The ":" prefix means "listen on this port on every network interface".
	addr := ":" + envOr("PORT", "4000")

	srv := server.New(addr)

	log.Printf("starting server on %s", addr)

	// ListenAndServe blocks until the server stops. If it returns, something
	// went wrong (usually: the port is already in use). log.Fatal prints the
	// error and calls os.Exit(1), which is the right behavior at startup —
	// there's nothing to recover to.
	log.Fatal(srv.ListenAndServe())
}