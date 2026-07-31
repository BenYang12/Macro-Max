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
	"errors"
	"fmt"
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
}

func NewSolveHandler(s SolveStore, sv Solver) *SolveHandler {
	return &SolveHandler{Store: s, Solver: sv}
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

	resp, err := h.Solver.Solve(ctx, solver.SolveInput{
		Target:       target,
		Products:     products,
		Foods:        foods,
		IntegerPacks: req.IntegerPacks,
	})
	if err != nil {
		// A transport failure or a timeout. The solver being down is MY
		// problem, not the client's, so it's a 500.
		serverErrorResponse(w, err)
		return
	}

	writeSolveResponse(w, resp)
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
