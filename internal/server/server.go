package server

import (
	"net/http"
	"time"

	"github.com/BenYang12/Macro-Max/internal/handler"
	"github.com/BenYang12/Macro-Max/internal/solver"
	"github.com/BenYang12/Macro-Max/internal/store"
)

// New now takes the *store.Store so it can build the data-backed handlers
// and register their routes
// still doesn't call ListenAndServe -> delegate to main
// goal of New is to be the assembly point that wires
// everything
// New now also takes the solver client. It's a POINTER that may be nil: if the
// solver couldn't be dialed, every other endpoint should still work, and only
// /v1/solve should fail. A hard dependency here would mean my whole API refuses
// to start because an optional microservice is down.
func New(addr string, st *store.Store, sv *solver.Client) *http.Server {
	// multiplexer = router
	// looks at incoming request's method + path -> decides WHICH handler function should run
	mux := http.NewServeMux()

	// Health check, unchanged.
	// mux.HandleFunc registers a route and returns nothing
	// Health check. It now pings the database, so like every other handler it
	// gets the store injected rather than being a bare function.
	health := handler.NewHealthHandler(st)
	mux.HandleFunc("GET /v1/healthcheck", health.Check)

	// Foods endpoints
	// Build ONE FoodsHandler and hand it the store
	// FoodsHandler contains a store, which implements FoodStore interface
	foods := handler.NewFoodsHandler(st)

	// {id} in pattern is a WILDCARD
	// it matches one path segment and becomes r.PathValue("id") inside the handler
	// register the specific /{id} route and collection routes separately;
	// the mux picks the most specific match
	mux.HandleFunc("GET /v1/foods", foods.List)
	mux.HandleFunc("GET /v1/foods/{id}", foods.Get)

	// Products endpoints. The SAME *store.Store satisfies ProductStore as
	// well as FoodStore — one concrete type, two narrow interface views of it.
	// That's structural typing paying off: adding methods in step 7 required
	// no change to any existing wiring.
	products := handler.NewProductsHandler(st)

	mux.HandleFunc("GET /v1/products", products.List)
	mux.HandleFunc("GET /v1/products/{id}", products.Get)

	// Targets. Note POST here — the first non-GET route in the app. The mux
	// matches on method, so POST /v1/targets and a hypothetical GET
	// /v1/targets are entirely separate registrations.
	targets := handler.NewTargetsHandler(st)

	mux.HandleFunc("POST /v1/targets", targets.Create)
	mux.HandleFunc("GET /v1/targets/{id}", targets.Get)

	// The solve endpoint — the reason the rest of this exists.
	// Registered only when a solver client was built, so a missing solver is a
	// 404 on this one route rather than a nil-pointer panic on first request.
	if sv != nil {
		solve := handler.NewSolveHandler(st, sv)
		mux.HandleFunc("POST /v1/solve", solve.Solve)
	}

	return &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
}
