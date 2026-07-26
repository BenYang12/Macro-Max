// handler does not take a *store.Store
// takes a small interface it defines itself -> lets the unit test in foods_test.go inject a fake with zero database

// foods.go - HTTP handlers for /v1/foods.
// handlers stay THIN: parse input, call the store, format output
// No SQL or business logic here.
package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/BenYang12/Macro-Max/internal/store"
)

// FoodStore is a CONSUMER-SIDE interface: an interface defined by the package that uses a dependency rather than the package that provides it
// the handler declares the exact slice of the store it needs,
// right here in the package that USES it.

// Why not just accept a *store.Store? Two payoffs:
//  1. Testability. The unit test in foods_test.go injects a hand-written fake
//     that implements these two methods and touches no database. Handler
//     logic (routing, status codes, JSON) gets tested in microseconds.
//  2. Honesty. The signature documents that THIS handler reads foods and
//     nothing else — it literally cannot call GetProduct.

// FoodStore interface
// "whoever I am, I can do these two things"
type FoodStore interface {
	ListFoods(ctx context.Context, filter store.FoodFilter) ([]store.Food, error)
	GetFood(ctx context.Context, id int64) (store.Food, error)
}

// FoodsHandler struct bundles the dependencies its routes need -> just a FoodStore
// Methods hang off of it (h.List, h.Get), so server.go can register them as routes
type FoodsHandler struct {
	Store FoodStore
}

// NewFoodsHandler is a tiny constructor. It buys nothing today but gives me
// one obvious place to add dependencies later (a logger, a cache) without
// changing every caller.
func NewFoodsHandler(s FoodStore) *FoodsHandler {
	return &FoodsHandler{Store: s}
}

// List handles GET /v1/foods?category=&tag=
// Method value: List is a METHOD on *FoodsHandler
// register it as h.List and it carries h (and thus the store) with it.
func (h *FoodsHandler) List(w http.ResponseWriter, r *http.Request) {
	// r.URL.Query() parses the ?key=value string into a lookup map
	// for a request to GET /v1/foods?category=protein&tag=vegan:
	// values := r.URL.Query() -> values now holds: {"category": ["protein"], "tag": ["vegan"]}
	filter := store.FoodFilter{
		Category: r.URL.Query().Get("category"),
		Tag:      r.URL.Query().Get("tag"),
	}

	// r.Context() ties DB query to the REQUEST's lifetime:
	// client disconnects -> context is cancelled and pgx abandons the query
	foods, err := h.Store.ListFoods(r.Context(), filter)

	if err != nil {
		// A store error here is never the client's fault (input was already
		// validated as "anything goes"), so it's a 500. serverErrorResponse
		// logs the real error and tells the client nothing sensitive.
		serverErrorResponse(w, err)
		return
	}

	// The success envelope from json.go: {"foods": [...]}. writeJSON handles
	// the Content-Type header, status, and trailing newline.
	if err := writeJSON(w, http.StatusOK, envelope{"foods": foods}); err != nil {
		serverErrorResponse(w, err)
	}
}

// Get handles GET /v1/foods/{id}
func (h *FoodsHandler) Get(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id") // r.PathValue("id") extracts the {id} wildcard
	// reads from the path, not the query string

	// path segment is text; store wants an int64
	// ParseInt(s, base, bitSize): base 10, 64-bit
	// A non-numeric id like /v1/foods/banana can
	// never match a real row, so it's a client mistake -> 404 (not a 500).
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		notFoundResponse(w)
		return
	}

	food, err := h.Store.GetFood(r.Context(), id)

	if err != nil {
		// The sentinel finally pays off: a missing row -> 404 envelope. Any
		// OTHER error is a real server fault -> 500. errors.Is walks the wrap
		// chain so the check survives fmt.Errorf("...: %w", ...).
		if errors.Is(err, store.ErrNotFound) {
			notFoundResponse(w)
			return
		}
		serverErrorResponse(w, err)
		return
	}
	if err := writeJSON(w, http.StatusOK, envelope{"food": food}); err != nil {
		serverErrorResponse(w, err)
	}
}
