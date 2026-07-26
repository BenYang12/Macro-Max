package handler

// products.go -> HTTP handlers for v1/products.
// same three layer discipline: parse, delegate, format
import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/BenYang12/Macro-Max/internal/store"
)

// ProductStore -> A second consumer-side interface, declaring only what these two handlers need.
// Deliberately SEAPARATE from FoodStore rather than one big "Store" interface: separation of concerns
// The products handler cannot call ListFoods, and its test fake only has to implement two methods
// instead of four.

type ProductStore interface {
	ListProducts(ctx context.Context, filter store.ProductFilter) ([]store.Product, error)
	GetProduct(ctx context.Context, id int64) (store.Product, error)
}

type ProductsHandler struct {
	Store ProductStore
}

func NewProductsHandler(s ProductStore) *ProductsHandler {
	return &ProductsHandler{Store: s}
}

// List handles GET /v1/products?store_id=&food_id=
func (h *ProductsHandler) List(w http.ResponseWriter, r *http.Request) {

	q := r.URL.Query() // reaches inside the request and pulls out just the query-string parameters (the ?key=value part)

	filter := store.ProductFilter{
		StoreID: q.Get("store_id"),
	}

	// PARSING AN OPTIONAL INTEGER
	// Three distinct cases, and conflating them is a real bug:
	//   1. absent       -> "" -> leave FoodID nil (no filter)
	//   2. present, valid   -> point FoodID at the parsed number
	//   3. present, garbage -> 400, because the client asked for something
	//      meaningless. This differs from the /foods/{id} path param, where
	//      garbage meant 404 ("no such resource"). Here the resource is the
	//      COLLECTION, which definitely exists — it's the QUERY that's
	//      malformed. Wrong request vs missing thing: 400 vs 404.

	if raw := q.Get("food_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			badRequestResponse(w, errors.New("food_id must be an integer"))
			return
		}
		// &id takes the address of the loop-free local, giving the *int64 the filter needs.
		filter.FoodID = &id
	}

	products, err := h.Store.ListProducts(r.Context(), filter)

	if err != nil {
		serverErrorResponse(w, err)
		return
	}

	if err := writeJSON(w, http.StatusOK, envelope{"products": products}); err != nil {
		serverErrorResponse(w, err)
	}

}

// Get handles GET /v1/products/{id}
func (h *ProductsHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		notFoundResponse(w)
		return
	}

	product, err := h.Store.GetProduct(r.Context(), id)

	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFoundResponse(w)
			return
		}
		serverErrorResponse(w, err)
		return
	}
	if err := writeJSON(w, http.StatusOK, envelope{"product": product}); err != nil {
		serverErrorResponse(w, err)
	}
}
