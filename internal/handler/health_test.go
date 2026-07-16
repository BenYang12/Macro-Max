package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHealth verifies that the Health handler returns a 200 status code, a
// JSON content type, and a body with the expected shape. Note the test
// function signature: TestXxx(t *testing.T). This is what `go test` looks
// for — the "Test" prefix and the *testing.T parameter.
func TestHealth(t *testing.T) {
	// httptest.NewRequest builds a fake *http.Request without touching the
	// network. The first arg is the HTTP method, the second is the target
	// URL (the path is what our handler will see), and the third is the
	// request body (nil for a GET).
	req := httptest.NewRequest(http.MethodGet, "/v1/healthcheck", nil)

	// httptest.NewRecorder gives us an http.ResponseWriter implementation
	// that records everything written to it, so we can inspect the response
	// after the handler runs.
	rr := httptest.NewRecorder()

	// Call the handler directly. This is just a function call — no server,
	// no ports, no goroutines. Fast and deterministic.
	Health(rr, req)

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
}
