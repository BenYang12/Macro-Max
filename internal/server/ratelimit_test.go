package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestRateLimiterAppliesTighterRecipeLimit(t *testing.T) {
	limiter := newRateLimiter(nil)
	now := time.Unix(1, 0)
	limiter.now = func() time.Time { return now }
	h := limiter.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for i := 0; i < 5; i++ {
		if i == 4 {
			now = now.Add(15 * time.Second)
		}
		r := httptest.NewRequest(http.MethodPost, "/v1/recipes", nil)
		r.RemoteAddr = "203.0.113.1:1234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := http.StatusNoContent
		if i == 4 {
			want = http.StatusTooManyRequests
		}
		if w.Code != want {
			t.Fatalf("request %d status = %d; want %d", i+1, w.Code, want)
		}
		if i == 4 && w.Header().Get("Retry-After") != "45" {
			t.Fatalf("Retry-After = %q; want 45", w.Header().Get("Retry-After"))
		}
	}
}

func TestRateLimiterResetsWindow(t *testing.T) {
	now := time.Unix(1, 0)
	limiter := newRateLimiter(nil)
	limiter.now = func() time.Time { return now }
	rule := rateLimit{limit: 1, window: time.Minute}
	first, _ := limiter.allow("client", "test", rule)
	second, retry := limiter.allow("client", "test", rule)
	if !first || second || retry != time.Minute {
		t.Fatal("limit was not enforced")
	}
	now = now.Add(time.Minute)
	allowed, _ := limiter.allow("client", "test", rule)
	if !allowed {
		t.Fatal("limit did not reset")
	}
}

func TestClientIPTrustsForwardingOnlyFromConfiguredProxy(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.2:1234"
	r.Header.Set("X-Forwarded-For", "198.51.100.4, 203.0.113.9")
	if got := newRateLimiter(nil).clientIP(r); got != "10.0.0.2" {
		t.Fatalf("private direct peer spoofed client IP: %q", got)
	}
	trusted := newRateLimiter([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	if got := trusted.clientIP(r); got != "203.0.113.9" {
		t.Fatalf("clientIP = %q", got)
	}

	r.RemoteAddr = "8.8.8.8:1234"
	if got := trusted.clientIP(r); got != "8.8.8.8" {
		t.Fatalf("untrusted forwarding header used: %q", got)
	}
}

// The limiter sits in front of the mux, so its rejection is the one error a
// client can receive without ever reaching a handler. It must still speak the
// project's JSON envelope: the browser client parses `error.code` and treats
// an unparseable body as "the API is unreachable", which turned a 429 into
// "Is the Go server running?" — a wrong diagnosis for a healthy server.
func TestRateLimitRejectionUsesTheJSONErrorEnvelope(t *testing.T) {
	limiter := newRateLimiter(nil)
	limiter.now = func() time.Time { return time.Unix(1, 0) }
	h := limiter.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	var w *httptest.ResponseRecorder
	for i := 0; i < 11; i++ {
		r := httptest.NewRequest(http.MethodPost, "/v1/solve", nil)
		r.RemoteAddr = "203.0.113.9:1234"
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
	}

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d; want 429", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q; want application/json", ct)
	}

	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("rejection body must be JSON, got %q: %v", w.Body.String(), err)
	}
	if body.Error.Code != "rate_limited" {
		t.Errorf("error.code = %q; want rate_limited", body.Error.Code)
	}
	if body.Error.Message == "" {
		t.Error("error.message must not be empty")
	}
}
