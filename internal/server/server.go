package server

import (
	"net/http"
	"time"

	"github.com/BenYang12/Macro-Max/internal/crypt"
	"github.com/BenYang12/Macro-Max/internal/handler"
	"github.com/BenYang12/Macro-Max/internal/kroger"
	"github.com/BenYang12/Macro-Max/internal/recipes"
	"github.com/BenYang12/Macro-Max/internal/solver"
	"github.com/BenYang12/Macro-Max/internal/store"
)

// Deps is everything the server can be built from.
//
// WHY A STRUCT NOW, when five positional parameters were fine before: Phase 7
// would have made it eight, and `server.New(addr, st, sv, cache, kr, rc, box,
// redirect)` is a call nobody can read. Worse, most of them are nilable
// pointers, so transposing two arguments is a compile error only if I'm lucky.
//
// A struct fixes both: every value is NAMED at the call site, and adding a
// dependency in Phase 8 won't touch a single existing line. This is the
// standard Go answer to a constructor that has outgrown its parameter list, and
// the moment to reach for it is exactly this one — when the list stops being
// readable, not when it stops compiling.
type Deps struct {
	Addr  string
	Store *store.Store

	// EVERY FIELD BELOW MAY BE NIL, and that's the design rather than sloppiness.
	// The rule this project follows: Postgres is load-bearing, and everything
	// else degrades. A missing dependency removes ITS routes and leaves the rest
	// of the API working, because a solve endpoint that 500s on every call is a
	// worse signal than a solve endpoint that isn't there.
	Solver *solver.Client
	Cache  *solver.Cache
	Kroger *kroger.Client

	// Phase 7.
	Recipes *recipes.Client

	// CryptoBox and KrogerRedirectURI are the cart feature's extra
	// requirements. The cart routes need Kroger AND CryptoBox together — see
	// the registration below, where that pairing is enforced in one place
	// rather than checked defensively inside the handler.
	CryptoBox         *crypt.Box
	KrogerRedirectURI string
}

// New wires the routes and returns a configured server. It still doesn't call
// ListenAndServe — that stays in main, the only thing allowed to block or exit.
func New(d Deps) *http.Server {
	// multiplexer = router
	// looks at incoming request's method + path -> decides WHICH handler function should run
	mux := http.NewServeMux()

	// Health check. It pings the database, so like every other handler it gets
	// the store injected rather than being a bare function.
	health := handler.NewHealthHandler(d.Store)
	mux.HandleFunc("GET /v1/healthcheck", health.Check)

	// Foods endpoints. Build ONE FoodsHandler and hand it the store.
	foods := handler.NewFoodsHandler(d.Store)

	// {id} in the pattern is a WILDCARD: it matches one path segment and
	// becomes r.PathValue("id") inside the handler.
	mux.HandleFunc("GET /v1/foods", foods.List)
	mux.HandleFunc("GET /v1/foods/{id}", foods.Get)

	// Products. The SAME *store.Store satisfies ProductStore as well as
	// FoodStore — one concrete type, many narrow interface views of it. That's
	// structural typing paying off: every phase since has added store methods
	// without changing a line of this wiring.
	products := handler.NewProductsHandler(d.Store)

	mux.HandleFunc("GET /v1/products", products.List)
	mux.HandleFunc("GET /v1/products/{id}", products.Get)

	// Targets. The mux matches on METHOD as well as path, so POST /v1/targets
	// and GET /v1/targets are entirely separate registrations.
	targets := handler.NewTargetsHandler(d.Store)

	mux.HandleFunc("POST /v1/targets", targets.Create)
	mux.HandleFunc("GET /v1/targets/{id}", targets.Get)

	// The solve endpoint — the reason the rest of this exists.
	if d.Solver != nil {
		// The cache may be nil (Redis down or misconfigured); the handler
		// guards every use, so a missing cache just means every solve computes.
		var c handler.SolveCache
		if d.Cache != nil {
			c = d.Cache
		}
		solve := handler.NewSolveHandler(d.Store, d.Solver, c)
		mux.HandleFunc("POST /v1/solve", solve.Solve)
	}

	// Store lookup, proxied through my API so Kroger credentials never reach a
	// browser.
	if d.Kroger != nil {
		stores := handler.NewStoresHandler(d.Kroger)
		mux.HandleFunc("GET /v1/stores", stores.List)
	}

	// -------------------------------------------------------------- Phase 7

	// Recipes. Registered only with an Anthropic key, which enforces the
	// architectural claim in internal/recipes' doc comment at the ROUTING
	// layer: the LLM is a finishing touch, and the solver — the actual
	// product — has to work without it.
	if d.Recipes != nil {
		rec := handler.NewRecipesHandler(d.Store, d.Recipes)
		mux.HandleFunc("POST /v1/recipes", rec.Generate)
	}

	// Cart. Needs Kroger credentials AND an encryption key, because these
	// routes store a real person's refresh token and I will not write one to
	// Postgres in plaintext. Checking both HERE rather than inside the handler
	// means the handler can assume its Box is non-nil — a missing key removes
	// the feature instead of producing a runtime error on the one request that
	// matters.
	if d.Kroger != nil && d.CryptoBox != nil {
		cart := handler.NewCartHandler(d.Store, d.Kroger, d.CryptoBox, d.KrogerRedirectURI)
		mux.HandleFunc("GET /v1/kroger/authorize", cart.Authorize)
		mux.HandleFunc("GET /v1/kroger/callback", cart.Callback)
		mux.HandleFunc("POST /v1/kroger/cart", cart.AddBasket)
	}

	return &http.Server{
		Addr:    d.Addr,
		Handler: mux,
		// WriteTimeout is generous because of /v1/recipes: an LLM call with a
		// long structured response genuinely takes tens of seconds, and the old
		// 10s would have severed the connection mid-generation — after paying
		// for every token spent. Every other endpoint answers in milliseconds
		// and is unaffected by a ceiling it never approaches.
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 150 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
}
