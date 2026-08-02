package handler

// recipes.go — POST /v1/recipes.
//
// The flow is: target id in, latest solved basket out of Postgres, basket into
// Claude, meal plan back. Same thin-handler discipline as everywhere else — it
// translates and orchestrates, it doesn't decide anything.
//
// A note on why this takes a TARGET id rather than a basket id, since it looks
// like the wrong choice at first: the frontend never sees a basket id. The
// solve response is the basket's CONTENTS, not its row, because that endpoint
// answers from Redis on a cache hit and there's no database row to name. The
// target id is the one identifier the client demonstrably holds — it just
// posted it to /v1/solve — so asking for it costs the client nothing, whereas
// a basket id would need a whole new field plumbed through the solve response.

import (
	"context"
	"errors"
	"net/http"

	"github.com/BenYang12/Macro-Max/internal/recipes"
	"github.com/BenYang12/Macro-Max/internal/store"
)

// RecipeStore is this handler's slice of the database — two reads, nothing
// else. Same consumer-side interface pattern as every other handler, and the
// reason the test below needs neither Postgres nor a network.
type RecipeStore interface {
	GetTarget(ctx context.Context, id int64) (store.UserTarget, error)
	LatestBasketForTarget(ctx context.Context, targetID int64) (store.Basket, []store.BasketLine, error)
}

// RecipeGenerator is the Claude dependency behind an interface, for exactly the
// reason Solver is: so handler tests inject a fake and never spend a token.
// A test that costs money is a test that stops being run.
type RecipeGenerator interface {
	Generate(ctx context.Context, req recipes.Request) (recipes.Plan, error)
}

type RecipesHandler struct {
	Store     RecipeStore
	Generator RecipeGenerator
}

func NewRecipesHandler(s RecipeStore, g RecipeGenerator) *RecipesHandler {
	return &RecipesHandler{Store: s, Generator: g}
}

// recipesRequest is the POST body. Pointer for the same present-vs-absent
// reason as everywhere else: a plain int64 can't tell "target_id: 0" from
// "target_id omitted", and those deserve different errors.
type recipesRequest struct {
	TargetID *int64 `json:"target_id"`
}

// Generate handles POST /v1/recipes.
func (h *RecipesHandler) Generate(w http.ResponseWriter, r *http.Request) {
	var req recipesRequest
	if err := readJSON(w, r, &req); err != nil {
		badRequestResponse(w, err)
		return
	}
	if req.TargetID == nil {
		failedValidationResponse(w, map[string]string{"target_id": "must be provided"})
		return
	}

	ctx := r.Context()

	target, err := h.Store.GetTarget(ctx, *req.TargetID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFoundResponse(w)
			return
		}
		serverErrorResponse(w, err)
		return
	}

	_, lines, err := h.Store.LatestBasketForTarget(ctx, target.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// NOT a bare 404. The target exists — it's the basket that doesn't,
			// and "not found" alone would send someone hunting for a bad target
			// id when the real answer is "solve first, then ask for recipes".
			// 422 with a message that names the next action.
			failedValidationResponse(w, map[string]string{
				"target_id": "this target has no solved basket yet — POST /v1/solve first",
			})
			return
		}
		serverErrorResponse(w, err)
		return
	}

	// AGGREGATE BY FOOD before handing anything to the model.
	//
	// A basket can hold two products of the same food — the MILP picks pack
	// sizes, so "2 x 400g black beans" and "1 x 800g black beans" are both
	// normal outcomes, and both can appear at once. A cook does not care that
	// their lentils arrived in two bag sizes; they care that they have 1.2kg of
	// lentils. Sending the raw lines would produce recipes that treat one food
	// as two ingredients and double-count the total.
	byFood := make(map[string]float64, len(lines))
	order := make([]string, 0, len(lines))
	for _, l := range lines {
		if _, seen := byFood[l.FoodName]; !seen {
			// Track first-appearance order separately. Go map iteration is
			// deliberately RANDOMIZED, so ranging a map to build the prompt
			// would reorder the ingredients on every request — which changes
			// the prompt, which changes the output, for no reason at all.
			order = append(order, l.FoodName)
		}
		byFood[l.FoodName] += l.Grams
	}

	ingredients := make([]recipes.Ingredient, 0, len(order))
	for _, name := range order {
		ingredients = append(ingredients, recipes.Ingredient{Food: name, Grams: byFood[name]})
	}

	plan, err := h.Generator.Generate(ctx, recipes.Request{
		Ingredients:   ingredients,
		ProteinGDaily: target.ProteinGDaily,
		CarbsGDaily:   target.CarbsGDaily,
		FatGDaily:     target.FatGDaily,
		DietTags:      target.DietTags,
	})
	if err != nil {
		switch {
		case errors.Is(err, recipes.ErrNoBasket):
			// The basket row exists but has no lines. Shouldn't happen given
			// the status filter in LatestBasketForTarget, which is exactly why
			// it's worth handling rather than letting it become a 500 with a
			// baffling message.
			failedValidationResponse(w, map[string]string{
				"target_id": "the latest basket for this target is empty",
			})
		case errors.Is(err, recipes.ErrRefused):
			// 422, not 500. The model worked correctly and declined; nothing on
			// my side failed, and a retry would reach the same answer. 502
			// would be a lie about an upstream failure that didn't happen.
			errorResponse(w, http.StatusUnprocessableEntity, "model_refused", err.Error())
		default:
			// Network failure, rate limit, unparseable output. All genuinely my
			// problem from the client's point of view, and serverErrorResponse
			// already logs the detail while telling the client nothing
			// specific — the API key must never reach a response body.
			serverErrorResponse(w, err)
		}
		return
	}

	if err := writeJSON(w, http.StatusOK, envelope{"plan": plan}); err != nil {
		serverErrorResponse(w, err)
	}
}
