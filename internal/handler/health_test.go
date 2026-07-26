package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakePinger is the smallest fake in the project — one method, one field.
// That's the payoff of the Pinger interface being one method wide.
type fakePinger struct {
	err error // nil = database healthy
}

func (f *fakePinger) Ping(ctx context.Context) error { return f.err }

// TestHealth verifies that the Health handler returns a 200 status code, a
// JSON content type, and a body with the expected shape. Note the test
// function signature: TestXxx(t *testing.T). This is what `go test` looks
// for — the "Test" prefix and the *testing.T parameter.
func TestHealth(t *testing.T) {
	// A healthy database: the fake's Ping returns nil.
	h := NewHealthHandler(&fakePinger{})

	// httptest.NewRequest builds a fake *http.Request without touching the
	// network. The first arg is the HTTP method, the second is the target
	// URL (the path is what our handler will see), and the third is the
	// request body (nil for a GET).
	req := httptest.NewRequest(http.MethodGet, "/v1/healthcheck", nil)

	// httptest.NewRecorder gives us an http.ResponseWriter implementation
	// that records everything written to it, so we can inspect the response
	// after the handler runs.
	rr := httptest.NewRecorder()

	// Call the handler directly. This is just a method call — no server,
	// no ports, no goroutines. Fast and deterministic.
	h.Check(rr, req)

	// Now inspect what the handler wrote.

	// Check the status code. http.StatusOK is just the constant 200, but
	// using the named constant makes the intent obvious and catches typos
	// (you can't misspell a constant name and have it compile).
	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	// Check the content type header.
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", got)
	}

	// Decode the response body into a map so we can check its contents.
	// We use a map[string]string rather than defining a struct because the
	// test doesn't need to enforce a schema — it just needs to peek at a
	// couple of fields.
	var body map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		// t.Fatalf stops the test immediately. Use it when subsequent
		// assertions can't run meaningfully — here, if we can't decode the
		// body, checking its fields makes no sense.
		t.Fatalf("failed to decode response body: %v", err)
	}

	if body["status"] != "ok" {
		t.Errorf("expected status field 'ok', got %q", body["status"])
	}
	if body["version"] == "" {
		t.Errorf("expected non-empty version field")
	}
	if body["database"] != "ok" {
		t.Errorf("expected database field 'ok', got %q", body["database"])
	}
}

// The case that makes the endpoint worth having: the process is up, but the
// database is not. A healthcheck that returned 200 here would tell a load
// balancer to keep routing traffic into a broken instance.
func TestHealth_DatabaseDownReturns503(t *testing.T) {
	h := NewHealthHandler(&fakePinger{err: errors.New("connection refused")})

	req := httptest.NewRequest(http.MethodGet, "/v1/healthcheck", nil)
	rr := httptest.NewRecorder()

	h.Check(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status %d, got %d", http.StatusServiceUnavailable, rr.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if body["status"] != "degraded" {
		t.Errorf("expected status field 'degraded', got %q", body["status"])
	}
	if body["database"] != "unavailable" {
		t.Errorf("expected database field 'unavailable', got %q", body["database"])
	}
}
