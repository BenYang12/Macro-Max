package handler

// solve.go — POST /v1/solve, the endpoint this whole project exists to serve.
//
// It's still a thin handler, but it orchestrates more than the others: load the
// target, load the candidate catalog, call the solver over gRPC, translate the
// answer back into my JSON envelope. The rule I'm keeping is that it makes no
// DECISIONS about nutrition or optimization — those live in internal/solver
// (conversions) and in the Python service (the math).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	solverv1 "github.com/BenYang12/Macro-Max/internal/gen/solver/v1"
	"github.com/BenYang12/Macro-Max/internal/solver"
	"github.com/BenYang12/Macro-Max/internal/store"
)

// SolveStore is this handler's slice of the database, same consumer-side
// interface pattern as everywhere else.
type SolveStore interface {
	GetTarget(ctx context.Context, id int64) (store.UserTarget, error)
	ListSolveCandidates(ctx context.Context, storeID string, dietTags []string, excludeFoodIDs []int64) ([]store.Product, error)
	ListFoodsByIDs(ctx context.Context, ids []int64) (map[int64]store.Food, error)
	SaveBasket(ctx context.Context, b *store.Basket, items []store.BasketItem) error
}

// SolveCache is the Redis layer, as an interface so tests can skip it entirely.
// Every method is allowed to be a no-op: a cache must never be load-bearing.
type SolveCache interface {
	Get(ctx context.Context, key string) *solverv1.SolveResponse
	Set(ctx context.Context, key string, resp *solverv1.SolveResponse)
}

// Solver is the solver dependency, declared as an interface for exactly the
// reason FoodStore is: so my handler tests can inject a fake and never start a
// Python process or open a socket.
type Solver interface {
	Solve(ctx context.Context, in solver.SolveInput) (*solverv1.SolveResponse, error)
}

type SolveHandler struct {
	Store  SolveStore
	Solver Solver
	Cache  SolveCache // may be nil; every use is guarded
}

func NewSolveHandler(s SolveStore, sv Solver, c SolveCache) *SolveHandler {
	return &SolveHandler{Store: s, Solver: sv, Cache: c}
}

// solveRequest is the POST body. Pointers again, for the same present-vs-absent
// reason as createTargetRequest.
type solveRequest struct {
	TargetID *int64 `json:"target_id"`

	// Opt into the Phase 4 MILP. Defaults to false, so today every request gets
	// the LP. When Phase 4 lands this becomes the interesting switch, and I can
	// compare the two answers side by side against the same target — which is
	// the demo that justifies Phase 4's existence.
	IntegerPacks bool `json:"integer_packs"`
}

// Solve handles POST /v1/solve
func (h *SolveHandler) Solve(w http.ResponseWriter, r *http.Request) {
	var req solveRequest
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

	// Load the candidate catalog, already filtered by the target's diet tags
	// and exclusions.
	products, err := h.Store.ListSolveCandidates(ctx, target.StoreID, target.DietTags, target.ExcludeFoodIDs)
	if err != nil {
		serverErrorResponse(w, err)
		return
	}
	if len(products) == 0 {
		// Not a 500 and not a 404: the request was fine and the target exists,
		// but the filters left nothing to choose from. 422 is the right shape —
		// the values are unprocessable — and the message names the likely cause
		// rather than making the user guess.
		failedValidationResponse(w, map[string]string{
			"store_id": fmt.Sprintf(
				"no available products at store %q matching diet tags %v",
				target.StoreID, target.DietTags),
		})
		return
	}

	// Collect the distinct food ids so I can load nutrition in ONE query rather
	// than one per product. This is the N+1 query problem, avoided deliberately:
	// with ~43 products, the naive version would be 44 round trips.
	seen := make(map[int64]bool, len(products))
	foodIDs := make([]int64, 0, len(products))
	for _, p := range products {
		if !seen[p.FoodID] {
			seen[p.FoodID] = true
			foodIDs = append(foodIDs, p.FoodID)
		}
	}

	foods, err := h.Store.ListFoodsByIDs(ctx, foodIDs)
	if err != nil {
		serverErrorResponse(w, err)
		return
	}

	input := solver.SolveInput{
		Target:       target,
		Products:     products,
		Foods:        foods,
		IntegerPacks: req.IntegerPacks,
	}

	// THE CACHE LOOKUP. The key is derived from the fully-built request plus a
	// fingerprint of the prices, so any change to targets, catalog, options, or
	// prices produces a different key and therefore a miss. I never have to
	// decide when to invalidate — I only have to make sure everything that
	// affects the answer is in the key.
	//
	// Building the request twice (once here, once inside Solve) is a little
	// wasteful and entirely deliberate: the alternative is hashing my own
	// hand-picked subset of fields, which is how cache-staleness bugs get
	// written.
	var cacheKey string
	if h.Cache != nil {
		if built, err := solver.BuildRequest(input); err == nil {
			if k, err := solver.SolveKey(built, products); err == nil {
				cacheKey = k
				if cached := h.Cache.Get(ctx, k); cached != nil {
					w.Header().Set("X-Cache", "hit")
					// PERSIST ON A CACHE HIT TOO. This line is not an
					// optimization — leaving it out was a real bug, and Phase 7
					// is what exposed it.
					//
					// The mismatch: the cache is CONTENT-addressed (same macros,
					// budget, store, and prices produce the same key), but a
					// basket row is keyed by TARGET. So solving a brand-new
					// target whose numbers happen to match an earlier one is a
					// cache hit, and the early return meant that target ended up
					// with no basket row at all.
					//
					// That was invisible while baskets were only an audit trail.
					// It stopped being invisible the moment /v1/recipes and
					// /v1/kroger/cart started reading "the latest basket for
					// this target" — both would answer "solve first" to someone
					// who had just solved successfully.
					h.persist(ctx, target, cacheKey, cached)
					writeSolveResponse(w, cached)
					return
				}
			}
		}
		w.Header().Set("X-Cache", "miss")
	}

	resp, err := h.Solver.Solve(ctx, input)
	if err != nil {
		// A transport failure or a timeout. The solver being down is MY
		// problem, not the client's, so it's a 500.
		serverErrorResponse(w, err)
		return
	}

	if cacheKey != "" {
		h.Cache.Set(ctx, cacheKey, resp)
	}

	// Persist EVERY solve, including infeasible ones. A record of what was
	// asked and what came back is worth far more than the one insert it costs,
	// and infeasible results are the most interesting ones to look back on.
	//
	// A persistence failure must NOT fail the request: the user already has
	// their answer, and losing the audit row is my problem, not theirs. So this
	// logs and continues rather than returning.
	h.persist(ctx, target, cacheKey, resp)

	writeSolveResponse(w, resp)
}

// persist records the solve. Best-effort by design — see the call site.
func (h *SolveHandler) persist(ctx context.Context, target store.UserTarget, key string, resp *solverv1.SolveResponse) {
	stats, _ := json.Marshal(map[string]any{
		"solve_seconds":             resp.SolveSeconds,
		"message":                   resp.Message,
		"min_feasible_budget_cents": resp.MinFeasibleBudgetCents,
	})

	basket := store.Basket{
		TargetID:       target.ID,
		StoreID:        target.StoreID,
		SolveKey:       key,
		Status:         statusString(resp.Status),
		TotalCostCents: int(resp.TotalCostCents),
		SolverStats:    stats,
	}

	items := make([]store.BasketItem, 0, len(resp.Items))
	for _, it := range resp.Items {
		// The schema requires packs > 0, and an LP solve can legitimately
		// produce a fractional pack count below 1. Rounding up is the honest
		// reading: you cannot buy a fraction of a bag, so any nonzero amount
		// means at least one pack.
		packs := int(it.Packs)
		if float64(packs) < it.Packs {
			packs++
		}
		if packs < 1 {
			continue
		}
		items = append(items, store.BasketItem{
			ProductID: it.ProductId,
			Packs:     packs,
			Grams:     it.Grams,
			CostCents: int(it.CostCents),
		})
	}

	if err := h.Store.SaveBasket(ctx, &basket, items); err != nil {
		log.Printf("warning: failed to persist basket for target %d: %v", target.ID, err)
	}
}

// writeSolveResponse maps the protobuf answer onto my JSON envelope.
//
// I translate rather than marshalling the protobuf directly, for two reasons.
// First, protojson emits enums as SOLVE_STATUS_OPTIMAL, which is fine for a
// machine and ugly for a frontend; I want "optimal". Second, and more
// importantly, my public JSON API should not be coupled to my internal gRPC
// contract — if I renumber a proto field in Phase 4, no frontend should care.
func writeSolveResponse(w http.ResponseWriter, resp *solverv1.SolveResponse) {
	// The INFEASIBLE branch is the interesting one, and it deserves its own
	// status code. 422 says "I understood you, and there's no answer" — which
	// is true — and the body carries the number that makes it actionable.
	if resp.Status == solverv1.SolveStatus_SOLVE_STATUS_INFEASIBLE {
		env := envelope{"error": map[string]any{
			"code":    "infeasible",
			"message": resp.Message,
			// The payoff. A frontend can render "your macros need at least
			// $47/week at this store" and offer to raise the budget.
			"min_feasible_budget_cents": resp.MinFeasibleBudgetCents,
		}}
		if err := writeJSON(w, http.StatusUnprocessableEntity, env); err != nil {
			serverErrorResponse(w, err)
		}
		return
	}

	if resp.Status == solverv1.SolveStatus_SOLVE_STATUS_ERROR ||
		resp.Status == solverv1.SolveStatus_SOLVE_STATUS_UNSPECIFIED {
		serverErrorResponse(w, fmt.Errorf("solver error: %s", resp.Message))
		return
	}

	items := make([]map[string]any, 0, len(resp.Items))
	for _, it := range resp.Items {
		items = append(items, map[string]any{
			"product_id":   it.ProductId,
			"product_name": it.ProductName,
			"food_name":    it.FoodName,
			"packs":        it.Packs,
			"grams":        it.Grams,
			"cost_cents":   it.CostCents,
		})
	}

	env := envelope{"basket": map[string]any{
		"status":           statusString(resp.Status),
		"items":            items,
		"total_cost_cents": resp.TotalCostCents,
		"achieved": map[string]any{
			"protein_g": resp.Achieved.GetProteinG(),
			"carbs_g":   resp.Achieved.GetCarbsG(),
			"fat_g":     resp.Achieved.GetFatG(),
			"calories":  resp.Achieved.GetCalories(),
		},
		"solve_seconds": resp.SolveSeconds,
	}}

	if err := writeJSON(w, http.StatusOK, env); err != nil {
		serverErrorResponse(w, err)
	}
}

// statusString turns the proto enum into the lowercase word my API promises.
func statusString(s solverv1.SolveStatus) string {
	switch s {
	case solverv1.SolveStatus_SOLVE_STATUS_OPTIMAL:
		return "optimal"
	case solverv1.SolveStatus_SOLVE_STATUS_FEASIBLE:
		return "feasible"
	case solverv1.SolveStatus_SOLVE_STATUS_INFEASIBLE:
		return "infeasible"
	default:
		return "error"
	}
}
