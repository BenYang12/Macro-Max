//package handler contains HTTP handlers for the API
//Each handler is a function that reads an *http.Request and writes to an http.ResponseWriter
//Handlers should be thin: they parse input, call into business logic, and format response
//They should not contain business logic themselves

package handler //handler package holds functions that respond to invidual HTTP routes

import (
	"context"
	"net/http"
	"time"
)

// version is reported by the healthcheck. One constant, one place to bump.
const version = "0.0.1"

// Pinger is the narrowest consumer-side interface in the project: the
// healthcheck needs exactly one thing from the store, so it asks for exactly
// one method. *store.Store satisfies it structurally, and a test can satisfy
// it with four lines of fake.
type Pinger interface {
	Ping(ctx context.Context) error
}

// HealthHandler now carries a dependency, so Health became a METHOD on a
// struct instead of a free function — the same shape as FoodsHandler and the
// rest. The endpoint's job grew: it no longer just proves the process is
// running, it proves the process can still reach its database.
type HealthHandler struct {
	Store Pinger
}

func NewHealthHandler(p Pinger) *HealthHandler {
	return &HealthHandler{Store: p}
}

// Check handles GET /v1/healthcheck.
//
// WHY A LIVE DB PING: a process that is running but cannot reach Postgres is
// useless — every real endpoint would 500. A healthcheck that only reports "I
// am running" would tell a load balancer to keep sending traffic to a broken
// instance. Checking the dependency is what makes the answer actionable.
func (h *HealthHandler) Check(w http.ResponseWriter, r *http.Request) {
	// A healthcheck must be FAST and must never hang — it gets polled
	// constantly by deploy platforms, and a slow one gets read as a failure
	// anyway. 2 seconds is generous for a localhost round trip, and the
	// derived context guarantees we answer either way.
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	dbStatus := "ok"
	status := "ok"
	code := http.StatusOK

	if err := h.Store.Ping(ctx); err != nil {
		dbStatus = "unavailable"
		status = "degraded"
		// 503 Service Unavailable, not 200. The STATUS CODE is what
		// orchestrators actually read — a body saying "degraded" under a 200
		// would be ignored by every load balancer on earth. Say it in the
		// protocol, not just the payload.
		code = http.StatusServiceUnavailable
	}

	// Deliberately flat, not wrapped in a resource envelope: a healthcheck is
	// a status report, not a resource, and monitoring tools expect top-level
	// keys. writeJSON still handles the Content-Type header and encoding.
	if err := writeJSON(w, code, envelope{
		"status":   status,
		"version":  version,
		"database": dbStatus,
	}); err != nil {
		serverErrorResponse(w, err)
	}
}
