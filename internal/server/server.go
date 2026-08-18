package server

import (
	"fmt"
	"net/http"
	"net/netip"
	"time"

	"github.com/BenYang12/Macro-Max/internal/handler"
	"github.com/BenYang12/Macro-Max/internal/kroger"
	"github.com/BenYang12/Macro-Max/internal/recipes"
	"github.com/BenYang12/Macro-Max/internal/solver"
	"github.com/BenYang12/Macro-Max/internal/store"
)

// Deps names the server's required and optional dependencies.
type Deps struct {
	Addr  string
	Store *store.Store

	// Nil optional clients remove only their associated routes.
	Solver *solver.Client
	Cache  *solver.Cache
	Kroger *kroger.Client

	Recipes           *recipes.Client
	RecipeAccessKey   string
	TrustedProxyCIDRs []netip.Prefix

	KrogerClientSecret string
	WebAppURL          string
}

// New wires routes without starting the listener.
func New(d Deps) (*http.Server, error) {
	if d.Recipes != nil && d.RecipeAccessKey == "" {
		return nil, fmt.Errorf("recipe access key is required when recipes are enabled")
	}
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

	// Recipes. Registered only with an Anthropic key, which enforces the
	// architectural claim in internal/recipes' doc comment at the ROUTING
	// layer: the LLM is a finishing touch, and the solver — the actual
	// product — has to work without it.
	if d.Recipes != nil {
		rec := handler.NewRecipesHandler(d.Store, d.Recipes, d.RecipeAccessKey)
		mux.HandleFunc("POST /v1/recipes", rec.Generate)
	}

	// Cart tokens are used only during the OAuth callback and are never stored.
	if d.Kroger != nil {
		cart, err := handler.NewCartHandler(d.Store, d.Kroger, d.KrogerClientSecret, d.WebAppURL)
		if err != nil {
			return nil, err
		}
		mux.HandleFunc("POST /v1/kroger/authorize", cart.Authorize)
		mux.HandleFunc("GET /v1/kroger/callback", cart.Callback)
	}

	return &http.Server{
		Addr:    d.Addr,
		Handler: newRateLimiter(d.TrustedProxyCIDRs).middleware(mux),
		// WriteTimeout is generous because of /v1/recipes: an LLM call with a
		// long structured response genuinely takes tens of seconds, and the old
		// 10s would have severed the connection mid-generation — after paying
		// for every token spent. Every other endpoint answers in milliseconds
		// and is unaffected by a ceiling it never approaches.
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 150 * time.Second,
		IdleTimeout:  120 * time.Second,
	}, nil
}
