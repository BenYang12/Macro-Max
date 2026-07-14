// Package server wires HTTP handlers into a running server. It's the "glue"
// layer between individual handlers and the operating system's network stack.
// Keeping this in its own package (rather than in main.go) makes it easy to
// test the routing setup and to swap out the entry point later — for example,
// if we want to run the same server code from an integration test.
package server

import (
	"net/http"
	"time"

	"github.com/BenYang12/Macro-Max/internal/handler"
)

// New returns an *http.Server configured with our routes and sensible
// production timeouts. It doesn't call ListenAndServe — the caller decides
// when and how to start it. This separation makes the server testable: a
// test can construct the server, poke at its Handler, and never open a port.
func New(addr string) *http.Server {
	// http.NewServeMux is the standard library's built-in HTTP router. As of
	// Go 1.22 it supports method-based routing (e.g. "GET /v1/healthcheck"),
	// which used to be the main reason people reached for third-party
	// routers like chi. For now, the stdlib mux is enough. We'll switch to
	// chi later, when we want features like middleware chains and URL
	// parameter extraction, but there's no reason to add a dependency
	// before we need one.
	mux := http.NewServeMux()

	// Register the health check route. The "GET " prefix (with a space) is
	// the Go 1.22 syntax that restricts this route to GET requests. Without
	// it, the mux would match any method — a subtle source of bugs.
	mux.HandleFunc("GET /v1/healthcheck", handler.Health)

	// Construct the server with explicit timeouts. The zero-value
	// http.Server has NO timeouts, which is a well-known footgun: a slow or
	// malicious client can hold a connection open forever, exhausting your
	// file descriptors. Always set these.
	//
	// - ReadTimeout: max time to read the full request (headers + body).
	// - WriteTimeout: max time to write the response.
	// - IdleTimeout: max time a keep-alive connection sits idle before
	//   being closed.
	//
	// The values below are reasonable defaults for a JSON API. Tune them
	// later based on real traffic patterns.
	return &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
}