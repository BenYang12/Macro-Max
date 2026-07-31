package handler

// Unit tests for POST /v1/solve. Both dependencies are faked: no database, and
// no Python process. What I'm testing here is orchestration and translation —
// does the handler load the right things, in the right order, and turn the
// solver's protobuf answer into the JSON my API promises?

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	solverv1 "github.com/BenYang12/Macro-Max/internal/gen/solver/v1"
	"github.com/BenYang12/Macro-Max/internal/solver"
	"github.com/BenYang12/Macro-Max/internal/store"
)

type fakeSolveStore struct {
	target      store.UserTarget
	targetErr   error
	products    []store.Product
	productsErr error
	foods       map[int64]store.Food

	gotStoreID  string
	gotDietTags []string
	gotExcluded []int64

	savedBasket *store.Basket
	savedItems  []store.BasketItem
	saveErr     error
}

func (f *fakeSolveStore) GetTarget(ctx context.Context, id int64) (store.UserTarget, error) {
	return f.target, f.targetErr
}

func (f *fakeSolveStore) ListSolveCandidates(ctx context.Context, storeID string, dietTags []string, exclude []int64) ([]store.Product, error) {
	f.gotStoreID, f.gotDietTags, f.gotExcluded = storeID, dietTags, exclude
	return f.products, f.productsErr
}

func (f *fakeSolveStore) ListFoodsByIDs(ctx context.Context, ids []int64) (map[int64]store.Food, error) {
	return f.foods, nil
}

func (f *fakeSolveStore) SaveBasket(ctx context.Context, b *store.Basket, items []store.BasketItem) error {
	f.savedBasket = b
	f.savedItems = items
	return f.saveErr
}

// fakeCache records what was asked for and can be primed with a hit.
type fakeCache struct {
	stored map[string]*solverv1.SolveResponse
	hit    *solverv1.SolveResponse
	gets   int
	sets   int
}

func (c *fakeCache) Get(ctx context.Context, key string) *solverv1.SolveResponse {
	c.gets++
	return c.hit
}

func (c *fakeCache) Set(ctx context.Context, key string, resp *solverv1.SolveResponse) {
	c.sets++
	if c.stored == nil {
		c.stored = map[string]*solverv1.SolveResponse{}
	}
	c.stored[key] = resp
}

type fakeSolver struct {
	resp *solverv1.SolveResponse
	err  error
	got  solver.SolveInput
}

func (f *fakeSolver) Solve(ctx context.Context, in solver.SolveInput) (*solverv1.SolveResponse, error) {
	f.got = in
	return f.resp, f.err
}

func okStore() *fakeSolveStore {
	return &fakeSolveStore{
		target: store.UserTarget{
			ID: 1, Label: "cutting", StoreID: "SEED",
			ProteinGDaily: 180, CarbsGDaily: 200, FatGDaily: 60,
			BudgetCentsWeekly: 7500,
			DietTags:          []string{}, ExcludeFoodIDs: []int64{},
		},
		products: []store.Product{
			{ID: 10, FoodID: 1, NetWeightG: 1000, EffectivePriceCents: 500, Available: true},
		},
		foods: map[int64]store.Food{
			1: {ID: 1, Name: "Chicken", Category: "protein", ProteinGPer100g: 22.5, KcalPer100g: 120},
		},
	}
}

func okSolver() *fakeSolver {
	return &fakeSolver{resp: &solverv1.SolveResponse{
		Status: solverv1.SolveStatus_SOLVE_STATUS_OPTIMAL,
		Items: []*solverv1.BasketItem{
			{ProductId: 10, Packs: 4.0, Grams: 4000, CostCents: 2000, FoodName: "Chicken"},
		},
		TotalCostCents: 2000,
		Achieved:       &solverv1.MacroTotals{ProteinG: 1260, CarbsG: 0, FatG: 0, Calories: 4800},
		SolveSeconds:   0.012,
	}}
}

func postSolve(t *testing.T, h *SolveHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/solve", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Solve(rr, req)
	return rr
}

func TestSolve_HappyPathReturnsBasket(t *testing.T) {
	h := NewSolveHandler(okStore(), okSolver(), nil)

	rr := postSolve(t, h, `{"target_id": 1}`)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200. body: %s", rr.Code, rr.Body.String())
	}

	var body struct {
		Basket struct {
			Status         string `json:"status"`
			TotalCostCents int64  `json:"total_cost_cents"`
			Items          []struct {
				ProductID int64   `json:"product_id"`
				Grams     float64 `json:"grams"`
			} `json:"items"`
			Achieved struct {
				ProteinG float64 `json:"protein_g"`
			} `json:"achieved"`
		} `json:"basket"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}

	// The enum became the lowercase word my API promises, not the protobuf
	// spelling SOLVE_STATUS_OPTIMAL.
	if body.Basket.Status != "optimal" {
		t.Errorf("status = %q; want %q", body.Basket.Status, "optimal")
	}
	if body.Basket.TotalCostCents != 2000 {
		t.Errorf("total = %d; want 2000", body.Basket.TotalCostCents)
	}
	if len(body.Basket.Items) != 1 || body.Basket.Items[0].ProductID != 10 {
		t.Errorf("unexpected items: %+v", body.Basket.Items)
	}
	if body.Basket.Achieved.ProteinG != 1260 {
		t.Errorf("achieved protein = %v; want 1260", body.Basket.Achieved.ProteinG)
	}
}

// The target's diet filters must actually reach the query. If they didn't, a
// vegan user would silently be offered chicken.
func TestSolve_PassesDietFiltersToTheQuery(t *testing.T) {
	st := okStore()
	st.target.DietTags = []string{"vegan"}
	st.target.ExcludeFoodIDs = []int64{7, 9}
	h := NewSolveHandler(st, okSolver(), nil)

	if rr := postSolve(t, h, `{"target_id": 1}`); rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rr.Code)
	}

	if st.gotStoreID != "SEED" {
		t.Errorf("store_id = %q; want SEED", st.gotStoreID)
	}
	if len(st.gotDietTags) != 1 || st.gotDietTags[0] != "vegan" {
		t.Errorf("diet tags = %v; want [vegan]", st.gotDietTags)
	}
	if len(st.gotExcluded) != 2 {
		t.Errorf("excluded = %v; want 2 ids", st.gotExcluded)
	}
}

// INFEASIBLE is not a server error and not a 404. It's a 422 carrying the
// number that makes it actionable — the single most valuable response this
// endpoint produces.
func TestSolve_InfeasibleReturns422WithMinBudget(t *testing.T) {
	sv := &fakeSolver{resp: &solverv1.SolveResponse{
		Status:                 solverv1.SolveStatus_SOLVE_STATUS_INFEASIBLE,
		MinFeasibleBudgetCents: 4700,
		Message:                "these macros need at least 4700 cents at this store",
	}}
	h := NewSolveHandler(okStore(), sv, nil)

	rr := postSolve(t, h, `{"target_id": 1}`)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 422", rr.Code)
	}

	var body struct {
		Error struct {
			Code      string `json:"code"`
			MinBudget int64  `json:"min_feasible_budget_cents"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Error.Code != "infeasible" {
		t.Errorf("code = %q; want infeasible", body.Error.Code)
	}
	if body.Error.MinBudget != 4700 {
		t.Errorf("min budget = %d; want 4700", body.Error.MinBudget)
	}
}

func TestSolve_SolverErrorStatusBecomes500(t *testing.T) {
	sv := &fakeSolver{resp: &solverv1.SolveResponse{
		Status:  solverv1.SolveStatus_SOLVE_STATUS_ERROR,
		Message: "GLOP exploded",
	}}
	h := NewSolveHandler(okStore(), sv, nil)

	if rr := postSolve(t, h, `{"target_id": 1}`); rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500", rr.Code)
	}
}

// The solver being unreachable is my problem, not the caller's.
func TestSolve_TransportFailureBecomes500(t *testing.T) {
	sv := &fakeSolver{err: errors.New("connection refused")}
	h := NewSolveHandler(okStore(), sv, nil)

	if rr := postSolve(t, h, `{"target_id": 1}`); rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500", rr.Code)
	}
}

func TestSolve_UnknownTargetReturns404(t *testing.T) {
	st := okStore()
	st.targetErr = store.ErrNotFound
	h := NewSolveHandler(st, okSolver(), nil)

	if rr := postSolve(t, h, `{"target_id": 999}`); rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404", rr.Code)
	}
}

func TestSolve_MissingTargetIDReturns422(t *testing.T) {
	h := NewSolveHandler(okStore(), okSolver(), nil)

	if rr := postSolve(t, h, `{}`); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 422", rr.Code)
	}
}

func TestSolve_MalformedJSONReturns400(t *testing.T) {
	h := NewSolveHandler(okStore(), okSolver(), nil)

	if rr := postSolve(t, h, `{"target_id":`); rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rr.Code)
	}
}

// An empty catalog after filtering is a 422, not a 500 — nothing broke, the
// filters just left nothing. The message has to name the likely cause.
func TestSolve_NoCandidatesReturns422(t *testing.T) {
	st := okStore()
	st.products = nil
	h := NewSolveHandler(st, okSolver(), nil)

	rr := postSolve(t, h, `{"target_id": 1}`)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 422", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "no available products") {
		t.Errorf("body should explain the empty catalog: %s", rr.Body.String())
	}
}

// The Phase 4 switch has to reach the solver.
func TestSolve_IntegerPacksFlagReachesTheSolver(t *testing.T) {
	sv := okSolver()
	h := NewSolveHandler(okStore(), sv, nil)

	if rr := postSolve(t, h, `{"target_id": 1, "integer_packs": true}`); rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rr.Code)
	}
	if !sv.got.IntegerPacks {
		t.Error("integer_packs did not reach the solver")
	}
}

// Deduplication: two products of the same food must produce ONE food id, not
// two. This is the N+1 avoidance working.
func TestSolve_DeduplicatesFoodIDs(t *testing.T) {
	st := okStore()
	st.products = append(st.products,
		store.Product{ID: 11, FoodID: 1, NetWeightG: 2000, EffectivePriceCents: 900, Available: true})
	sv := okSolver()
	h := NewSolveHandler(st, sv, nil)

	if rr := postSolve(t, h, `{"target_id": 1}`); rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rr.Code)
	}
	if len(sv.got.Products) != 2 {
		t.Errorf("got %d products; want 2", len(sv.got.Products))
	}
}

// ------------------------------------------------------------- cache and audit

// A cache hit must skip the solver entirely — that's the whole point.
func TestSolve_CacheHitSkipsTheSolver(t *testing.T) {
	sv := okSolver()
	cache := &fakeCache{hit: &solverv1.SolveResponse{
		Status:         solverv1.SolveStatus_SOLVE_STATUS_OPTIMAL,
		TotalCostCents: 999,
		Achieved:       &solverv1.MacroTotals{},
	}}
	h := NewSolveHandler(okStore(), sv, cache)

	rr := postSolve(t, h, `{"target_id": 1}`)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rr.Code)
	}
	if rr.Header().Get("X-Cache") != "hit" {
		t.Errorf("X-Cache = %q; want hit", rr.Header().Get("X-Cache"))
	}
	// The fake solver records its input; an untouched zero value proves it was
	// never called.
	if len(sv.got.Products) != 0 {
		t.Error("the solver ran despite a cache hit")
	}
	if !strings.Contains(rr.Body.String(), "999") {
		t.Error("the cached response was not returned")
	}
}

// A miss must compute AND store, so the next identical request hits.
func TestSolve_CacheMissComputesAndStores(t *testing.T) {
	cache := &fakeCache{} // nil hit = miss
	h := NewSolveHandler(okStore(), okSolver(), cache)

	rr := postSolve(t, h, `{"target_id": 1}`)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rr.Code)
	}
	if rr.Header().Get("X-Cache") != "miss" {
		t.Errorf("X-Cache = %q; want miss", rr.Header().Get("X-Cache"))
	}
	if cache.sets != 1 {
		t.Errorf("cache.Set called %d times; want 1", cache.sets)
	}
}

// A nil cache must be completely harmless — the degraded-Redis path.
func TestSolve_NilCacheStillWorks(t *testing.T) {
	h := NewSolveHandler(okStore(), okSolver(), nil)

	if rr := postSolve(t, h, `{"target_id": 1}`); rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 with no cache", rr.Code)
	}
}

// Every solve gets recorded, and the line items come with it.
func TestSolve_PersistsTheBasket(t *testing.T) {
	st := okStore()
	h := NewSolveHandler(st, okSolver(), nil)

	postSolve(t, h, `{"target_id": 1}`)

	if st.savedBasket == nil {
		t.Fatal("no basket was persisted")
	}
	if st.savedBasket.Status != "optimal" {
		t.Errorf("status = %q; want optimal", st.savedBasket.Status)
	}
	if st.savedBasket.TotalCostCents != 2000 {
		t.Errorf("total = %d; want 2000", st.savedBasket.TotalCostCents)
	}
	if len(st.savedItems) != 1 {
		t.Fatalf("got %d items; want 1", len(st.savedItems))
	}
	// 4.0 packs from the fake solver must survive as an integer.
	if st.savedItems[0].Packs != 4 {
		t.Errorf("packs = %d; want 4", st.savedItems[0].Packs)
	}
}

// An infeasible solve is still worth recording — arguably the most worth it.
func TestSolve_PersistsInfeasibleSolves(t *testing.T) {
	st := okStore()
	sv := &fakeSolver{resp: &solverv1.SolveResponse{
		Status:                 solverv1.SolveStatus_SOLVE_STATUS_INFEASIBLE,
		MinFeasibleBudgetCents: 4700,
		Message:                "needs more money",
	}}
	h := NewSolveHandler(st, sv, nil)

	postSolve(t, h, `{"target_id": 1}`)

	if st.savedBasket == nil {
		t.Fatal("infeasible solves must still be recorded")
	}
	if st.savedBasket.Status != "infeasible" {
		t.Errorf("status = %q; want infeasible", st.savedBasket.Status)
	}
	if len(st.savedItems) != 0 {
		t.Errorf("got %d items; an infeasible basket has none", len(st.savedItems))
	}
}

// Losing the audit row must not cost the user their answer.
func TestSolve_PersistFailureDoesNotFailTheRequest(t *testing.T) {
	st := okStore()
	st.saveErr = errors.New("disk on fire")
	h := NewSolveHandler(st, okSolver(), nil)

	if rr := postSolve(t, h, `{"target_id": 1}`); rr.Code != http.StatusOK {
		t.Fatalf("status = %d; a persistence failure must not fail the request", rr.Code)
	}
}
