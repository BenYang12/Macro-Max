// Package main is the entry point for the API server binary. In Go, every
// runnable program has a `main` package with a `main` function. Everything
// else is a library.
//
// Keep main.go small: its job is to read configuration, construct
// dependencies, and start the server. Business logic doesn't live here.
package main

import (
	"log"

	"github.com/yourusername/budget-macro-optimizer/internal/server"
)

func main() {
	// Hardcoded address for now. In Phase 1's next step we'll read this from
	// an environment variable so we can change it without recompiling.
	addr := ":4000"

	srv := server.New(addr)

	log.Printf("starting server on %s", addr)

	// ListenAndServe blocks until the server stops. If it returns, something
	// went wrong (usually: the port is already in use). log.Fatal prints the
	// error and calls os.Exit(1), which is the right behavior at startup —
	// there's nothing to recover to.
	log.Fatal(srv.ListenAndServe())
}