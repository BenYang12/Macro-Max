package solver

// The Phase 3 end-to-end test: real Postgres -> my Go conversion layer -> gRPC
// over a real socket -> the Python OR-Tools service -> a basket, back.
//
// Every other test in this project fakes at least one boundary. This one fakes
// nothing, which makes it the only test that can catch the failures that live
// BETWEEN components: a proto field I mapped wrong, a unit conversion that's
// off by 100, a Python import that works in pytest but not over gRPC. I hit
// exactly that last one while building this — my pytest suite passed while the
// container crash-looped, because the tests never imported the transport layer.
//
// It needs two things running, and self-skips without them, so `make test` on a
// bare laptop stays green:
//   TEST_DATABASE_URL  -> a migrated test database
//   SOLVER_ADDR        -> a reachable solver (docker compose up solver)

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	solverv1 "github.com/BenYang12/Macro-Max/internal/gen/solver/v1"
	"github.com/BenYang12/Macro-Max/internal/store"
)

func newE2E(t *testing.T) (*store.Store, *Client) {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping end-to-end test")
	}

	addr := os.Getenv("SOLVER_ADDR")
	if addr == "" {
		addr = "localhost:50051"
	}

	// grpc.NewClient connects LAZILY, so dialing it proves nothing about
	// whether the solver is actually up. I probe the TCP port myself first,
	// because "skipped because the solver isn't running" is a far more useful
	// message than a 10-second deadline exceeded on the first RPC.
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Skipf("solver not reachable at %s (run `docker compose up -d solver`): %v", addr, err)
	}
	conn.Close()

	st, err := store.NewPool(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connecting to test database: %v", err)
	}
	t.Cleanup(st.Close)
	insertE2ECatalog(t, st)

	cl, err := New(addr)
	if err != nil {
		t.Fatalf("creating solver client: %v", err)
	}
	t.Cleanup(func() { cl.Close() })

	return st, cl
}

type fixtureFood struct {
	name, category            string
	tags                      []string
	kcal, protein, carbs, fat float64
	weight                    float64
	price                     int64
}

// insertE2ECatalog provisions the smallest deterministic catalog that can
// exercise nutrition filtering and the solver's variety constraints.
func insertE2ECatalog(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	fixtureTag := "__solver_e2e_" + t.Name()
	allVegan := []string{"vegetarian", "vegan", "gluten_free", "dairy_free", fixtureTag}
	foods := []fixtureFood{
		{"chicken", "protein", []string{"gluten_free", "dairy_free", fixtureTag}, 120, 23, 0, 3, 1000, 900},
		{"tofu", "protein", allVegan, 144, 17, 3, 9, 500, 250},
		{"lentils", "protein", allVegan, 352, 25, 63, 1, 500, 200},
		{"black beans", "protein", allVegan, 341, 22, 62, 1, 500, 200},
		{"spinach", "vegetable", allVegan, 23, 3, 4, 0, 300, 200},
		{"carrots", "vegetable", allVegan, 41, 1, 10, 0, 500, 180},
		{"bananas", "fruit", allVegan, 89, 1, 23, 0, 1000, 250},
		{"rice", "carb", allVegan, 365, 7, 80, 1, 1000, 300},
		{"canola oil", "fat", allVegan, 884, 0, 0, 100, 500, 400},
	}

	tx, err := st.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("beginning fixture transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	foodIDs := make([]int64, 0, len(foods))
	productIDs := make([]int64, 0, len(foods))
	for i, f := range foods {
		var foodID, productID int64
		name := "__solver_e2e_" + t.Name() + "_" + f.name
		if err := tx.QueryRow(ctx, `
			INSERT INTO foods (name, category, tags, kcal_per_100g,
			 protein_g_per_100g, carbs_g_per_100g, fat_g_per_100g)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
			name, f.category, f.tags, f.kcal, f.protein, f.carbs, f.fat).Scan(&foodID); err != nil {
			t.Fatalf("inserting fixture food: %v", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO products (food_id, store_id, external_id, name,
			 pack_size_qty, pack_size_unit, net_weight_g, price_cents, available)
			VALUES ($1, $2, $3, $4, 1, 'each', $5, $6, TRUE) RETURNING id`,
			foodID, store.UniversityPlaceStoreID, name, name, f.weight, f.price).Scan(&productID); err != nil {
			t.Fatalf("inserting fixture product %d: %v", i, err)
		}
		foodIDs = append(foodIDs, foodID)
		productIDs = append(productIDs, productID)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("committing fixture: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = st.Pool.Exec(cleanupCtx, `DELETE FROM products WHERE id = ANY($1)`, productIDs)
		_, _ = st.Pool.Exec(cleanupCtx, `DELETE FROM foods WHERE id = ANY($1)`, foodIDs)
	})
}

// loadCatalog pulls the isolated University Place fixture from Postgres.
func loadCatalog(t *testing.T, st *store.Store) ([]store.Product, map[int64]store.Food) {
	t.Helper()
	ctx := context.Background()

	products, err := st.ListSolveCandidates(ctx, store.UniversityPlaceStoreID, []string{"__solver_e2e_" + t.Name()}, nil)
	if err != nil {
		t.Fatalf("loading solve candidates: %v", err)
	}
	if len(products) == 0 {
		t.Fatal("fixture produced no solve candidates")
	}

	ids := make([]int64, 0, len(products))
	seen := map[int64]bool{}
	for _, p := range products {
		if !seen[p.FoodID] {
			seen[p.FoodID] = true
			ids = append(ids, p.FoodID)
		}
	}

	foods, err := st.ListFoodsByIDs(ctx, ids)
	if err != nil {
		t.Fatalf("loading foods: %v", err)
	}
	return products, foods
}

// realisticTarget is a lifter's cut: 180g protein, 200g carbs, 60g fat daily,
// $75/week. These are the numbers I'd actually type into my own app.
func realisticTarget() store.UserTarget {
	return store.UserTarget{
		Label:             "cutting",
		ProteinGDaily:     180,
		CarbsGDaily:       200,
		FatGDaily:         60,
		BudgetCentsWeekly: 7500,
		StoreID:           store.UniversityPlaceStoreID,
	}
}

// The infeasible path, end to end. An absurdly low budget must come back with
// a real number attached, not just a failure.
func TestE2E_ImpossibleBudgetReturnsMinFeasible(t *testing.T) {
	st, cl := newE2E(t)
	products, foods := loadCatalog(t, st)

	target := realisticTarget()
	target.BudgetCentsWeekly = 100 // one dollar a week

	resp, err := cl.Solve(context.Background(), SolveInput{
		Target: target, Products: products, Foods: foods, IntegerPacks: true,
	})
	if err != nil {
		t.Fatalf("solve: %v", err)
	}

	if resp.Status != solverv1.SolveStatus_SOLVE_STATUS_INFEASIBLE {
		t.Fatalf("status = %v; want INFEASIBLE", resp.Status)
	}
	if resp.MinFeasibleBudgetCents <= 0 {
		t.Fatal("expected min_feasible_budget_cents to be populated — this is the whole point of the infeasible path")
	}
	if resp.MinFeasibleBudgetCents <= 100 {
		t.Errorf("min feasible %d should exceed the rejected budget of 100", resp.MinFeasibleBudgetCents)
	}
	t.Logf("infeasible at 100 cents; needs %d cents (~$%.2f/week)",
		resp.MinFeasibleBudgetCents, float64(resp.MinFeasibleBudgetCents)/100)
}

// Diet filters must survive all the way to the answer. A vegan solve must not
// contain chicken — which tests the SQL filter, not the solver, but end to end
// is the only place that proves the two are wired together.
func TestE2E_VeganFilterExcludesAnimalProducts(t *testing.T) {
	st, cl := newE2E(t)
	ctx := context.Background()

	products, err := st.ListSolveCandidates(ctx, store.UniversityPlaceStoreID, []string{"vegan", "__solver_e2e_" + t.Name()}, nil)
	if err != nil {
		t.Fatalf("loading vegan candidates: %v", err)
	}
	if len(products) == 0 {
		t.Fatal("fixture produced no vegan candidates")
	}

	ids := make([]int64, 0, len(products))
	seen := map[int64]bool{}
	for _, p := range products {
		if !seen[p.FoodID] {
			seen[p.FoodID] = true
			ids = append(ids, p.FoodID)
		}
	}
	foods, err := st.ListFoodsByIDs(ctx, ids)
	if err != nil {
		t.Fatalf("loading foods: %v", err)
	}

	// Every candidate must carry the vegan tag before the solver ever sees it.
	for _, f := range foods {
		vegan := false
		for _, tag := range f.Tags {
			if tag == "vegan" {
				vegan = true
			}
		}
		if !vegan {
			t.Errorf("non-vegan food %q passed the diet filter", f.Name)
		}
	}

	target := realisticTarget()
	target.DietTags = []string{"vegan"}
	// Vegan protein at these targets is expensive, so give it room.
	target.BudgetCentsWeekly = 15000

	resp, err := cl.Solve(ctx, SolveInput{Target: target, Products: products, Foods: foods, IntegerPacks: true})
	if err != nil {
		t.Fatalf("solve: %v", err)
	}

	if resp.Status != solverv1.SolveStatus_SOLVE_STATUS_OPTIMAL {
		t.Fatalf("status = %v; want OPTIMAL. message: %s", resp.Status, resp.Message)
	}

	for _, it := range resp.Items {
		if it.FoodName == "Chicken Breast, raw" || it.FoodName == "Eggs, whole, raw" {
			t.Errorf("vegan solve returned %q", it.FoodName)
		}
	}
	t.Logf("vegan solve: %v, %d items", resp.Status, len(resp.Items))
}

// milpTarget gives the whole-pack solver room for its variety requirements.
func milpTarget() store.UserTarget {
	t := realisticTarget()
	t.BudgetCentsWeekly = 12000 // $120/week
	return t
}

func TestE2E_MILPProducesAnEdibleBasket(t *testing.T) {
	st, cl := newE2E(t)
	products, foods := loadCatalog(t, st)

	resp, err := cl.Solve(context.Background(), SolveInput{
		Target:       milpTarget(),
		Products:     products,
		Foods:        foods,
		IntegerPacks: true, // THE switch
	})
	if err != nil {
		t.Fatalf("solve: %v", err)
	}

	if resp.Status != solverv1.SolveStatus_SOLVE_STATUS_OPTIMAL {
		t.Fatalf("status = %v; want OPTIMAL. message: %s", resp.Status, resp.Message)
	}

	// Group the answer by category so I can check the variety claims.
	byCategory := map[string]map[int64]bool{}
	byFood := map[int64]float64{} // food id -> kcal contributed
	var names []string

	for _, it := range resp.Items {
		var prod *store.Product
		for i := range products {
			if products[i].ID == it.ProductId {
				prod = &products[i]
				break
			}
		}
		if prod == nil {
			t.Fatalf("solver returned product %d, which I never sent", it.ProductId)
		}
		f := foods[prod.FoodID]

		if byCategory[f.Category] == nil {
			byCategory[f.Category] = map[int64]bool{}
		}
		byCategory[f.Category][f.ID] = true
		byFood[f.ID] += (f.KcalPer100g / 100) * it.Grams
		names = append(names, it.FoodName)
	}

	t.Logf("MILP basket (%d items, %d cents, %.3fs): %v",
		len(resp.Items), resp.TotalCostCents, resp.SolveSeconds, names)

	// --- The Phase 4 exit criteria, straight from my plan ---

	if n := len(byCategory["protein"]); n < 3 {
		t.Errorf("protein sources = %d; want >= 3", n)
	}
	if n := len(byCategory["vegetable"]); n < 2 {
		t.Errorf("vegetables = %d; want >= 2", n)
	}
	if n := len(byCategory["fruit"]); n < 1 {
		t.Errorf("fruits = %d; want >= 1", n)
	}

	// No food over 30% of the calorie ceiling. The ceiling is derived as
	// 1.1 x Atwater when unset: 1.1 x (4*1260 + 4*1400 + 9*420) = 15862 kcal.
	ceiling := 1.1 * (4*1260.0 + 4*1400.0 + 9*420.0)
	cap := 0.30 * ceiling
	for foodID, kcal := range byFood {
		if kcal > cap+1 {
			t.Errorf("food %d supplies %.0f kcal, over the %.0f cap (30%%)", foodID, kcal, cap)
		}
	}

	// Whole packs — the dishonesty Phase 3 had.
	for _, it := range resp.Items {
		if it.Packs != float64(int64(it.Packs)) {
			t.Errorf("%q: %v packs is not a whole number", it.ProductName, it.Packs)
		}
	}

	// Still a correct answer: macros met, budget respected.
	if resp.Achieved.ProteinG < 1260-0.01 {
		t.Errorf("protein = %.1f; want >= 1260", resp.Achieved.ProteinG)
	}
	if resp.TotalCostCents > 12000 {
		t.Errorf("cost %d exceeds the budget", resp.TotalCostCents)
	}
}

// The other Phase 4 exit criterion: an impossible budget must come back with a
// real number, now computed against the FULL model including variety.
func TestE2E_MILPInfeasibleBudgetReportsMinimum(t *testing.T) {
	st, cl := newE2E(t)
	products, foods := loadCatalog(t, st)

	target := milpTarget()
	target.BudgetCentsWeekly = 500

	resp, err := cl.Solve(context.Background(), SolveInput{
		Target: target, Products: products, Foods: foods, IntegerPacks: true,
	})
	if err != nil {
		t.Fatalf("solve: %v", err)
	}

	if resp.Status != solverv1.SolveStatus_SOLVE_STATUS_INFEASIBLE {
		t.Fatalf("status = %v; want INFEASIBLE", resp.Status)
	}
	if resp.MinFeasibleBudgetCents <= 500 {
		t.Errorf("min feasible = %d; should exceed the rejected 500",
			resp.MinFeasibleBudgetCents)
	}
	t.Logf("MILP infeasible at 500c; needs %d cents (~$%.2f/week): %s",
		resp.MinFeasibleBudgetCents,
		float64(resp.MinFeasibleBudgetCents)/100, resp.Message)
}
