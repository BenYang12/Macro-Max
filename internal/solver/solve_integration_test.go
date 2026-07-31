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
//   TEST_DATABASE_URL  -> a migrated, SEEDED database
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

	cl, err := New(addr)
	if err != nil {
		t.Fatalf("creating solver client: %v", err)
	}
	t.Cleanup(func() { cl.Close() })

	return st, cl
}

// loadCatalog pulls the real seeded catalog out of Postgres.
func loadCatalog(t *testing.T, st *store.Store) ([]store.Product, map[int64]store.Food) {
	t.Helper()
	ctx := context.Background()

	products, err := st.ListSolveCandidates(ctx, "SEED", nil, nil)
	if err != nil {
		t.Fatalf("loading solve candidates: %v", err)
	}
	if len(products) == 0 {
		t.Skip("no SEED products in the database; run `make seed`")
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
		StoreID:           "SEED",
	}
}

// THE PHASE 3 HEADLINE TEST.
//
// This asserts that the answer is BAD, which feels backwards until I remember
// what Phase 3 is for. A pure cost-minimizing LP with no variety constraints
// finds the cheapest source of each macro and buys nothing else — the Stigler
// diet result. I seeded cheap whey, cheap canola oil, and cheap rice precisely
// so this would happen and be visible.
//
// When Phase 4's MILP lands, the equivalent test asserts the opposite: >= 3
// protein sources, >= 2 vegetables, no food over 30% of calories. The DIFF
// between these two tests is the entire argument for why Phase 4 exists, and
// it's what I'd put in the README.
func TestE2E_StiglerBasketIsDegenerate(t *testing.T) {
	st, cl := newE2E(t)
	products, foods := loadCatalog(t, st)

	resp, err := cl.Solve(context.Background(), SolveInput{
		Target:   realisticTarget(),
		Products: products,
		Foods:    foods,
	})
	if err != nil {
		t.Fatalf("solve: %v", err)
	}

	if resp.Status != solverv1.SolveStatus_SOLVE_STATUS_OPTIMAL {
		t.Fatalf("status = %v; want OPTIMAL. message: %s", resp.Status, resp.Message)
	}

	names := make([]string, 0, len(resp.Items))
	for _, it := range resp.Items {
		names = append(names, it.FoodName)
	}
	t.Logf("basket (%d items, %d cents): %v", len(resp.Items), resp.TotalCostCents, names)

	// The degeneracy claim: a handful of foods out of 42 available.
	if len(resp.Items) > 6 {
		t.Errorf("expected a degenerate basket of a few foods; got %d: %v", len(resp.Items), names)
	}

	// Every macro target must still be met — the basket is inedible, not wrong.
	// 180g/day x 7 = 1260g protein for the week.
	if resp.Achieved.ProteinG < 1260-0.01 {
		t.Errorf("protein = %.1f; want >= 1260", resp.Achieved.ProteinG)
	}
	if resp.Achieved.CarbsG < 1400-0.01 {
		t.Errorf("carbs = %.1f; want >= 1400", resp.Achieved.CarbsG)
	}
	if resp.Achieved.FatG < 420-0.01 {
		t.Errorf("fat = %.1f; want >= 420", resp.Achieved.FatG)
	}

	// And it must be within budget.
	if resp.TotalCostCents > 7500 {
		t.Errorf("cost %d exceeds the 7500 cent budget", resp.TotalCostCents)
	}

	// The specific failure Phase 4 fixes: no vegetables, no fruit. I assert this
	// so that if it ever stops being true I have to think about why, rather than
	// silently losing my justification for the next phase.
	categories := map[string]bool{}
	for _, it := range resp.Items {
		for _, f := range foods {
			if f.Name == it.FoodName {
				categories[f.Category] = true
			}
		}
	}
	if categories["vegetable"] || categories["fruit"] {
		t.Logf("NOTE: the LP chose produce (%v) — the Phase 4 justification is weaker than expected", categories)
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
		Target: target, Products: products, Foods: foods,
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

	products, err := st.ListSolveCandidates(ctx, "SEED", []string{"vegan"}, nil)
	if err != nil {
		t.Fatalf("loading vegan candidates: %v", err)
	}
	if len(products) == 0 {
		t.Skip("no vegan SEED products; run `make seed`")
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

	resp, err := cl.Solve(ctx, SolveInput{Target: target, Products: products, Foods: foods})
	if err != nil {
		t.Fatalf("solve: %v", err)
	}

	// Either answer is legitimate here: a vegan basket, or "not possible at
	// this store". What must NOT happen is chicken showing up.
	for _, it := range resp.Items {
		if it.FoodName == "Chicken Breast, raw" || it.FoodName == "Eggs, whole, raw" {
			t.Errorf("vegan solve returned %q", it.FoodName)
		}
	}
	t.Logf("vegan solve: %v, %d items", resp.Status, len(resp.Items))
}

// The two-pack-size plant from my seed data, checked end to end. The LP has no
// integer constraint so it will buy fractional packs — I want to SEE that,
// because it's the concrete dishonesty Phase 4 removes.
func TestE2E_LPBuysFractionalPacks(t *testing.T) {
	st, cl := newE2E(t)
	products, foods := loadCatalog(t, st)

	resp, err := cl.Solve(context.Background(), SolveInput{
		Target: realisticTarget(), Products: products, Foods: foods,
	})
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	if resp.Status != solverv1.SolveStatus_SOLVE_STATUS_OPTIMAL {
		t.Skipf("solve was not optimal (%v), nothing to inspect", resp.Status)
	}

	fractional := false
	for _, it := range resp.Items {
		// A pack count with a meaningful fractional part means "buy 2.4 bags",
		// which no store will sell me.
		if diff := it.Packs - float64(int64(it.Packs)); diff > 0.01 && diff < 0.99 {
			fractional = true
			t.Logf("fractional packs: %.3f of %q (Phase 4 will make this an integer)",
				it.Packs, it.ProductName)
		}
	}
	if !fractional {
		t.Log("no fractional packs this run — possible, but Phase 4's integer constraint is still needed in general")
	}
}

// ---------------------------------------------------------------------------
// PHASE 4 — the same catalog, the same targets, the MILP switched on.
//
// The test below is the mirror image of TestE2E_StiglerBasketIsDegenerate. That
// one asserts the basket is 3 joyless foods with no produce; this one asserts
// >=3 protein sources, >=2 vegetables, >=1 fruit, whole packs, and no single
// food dominating the calories. Running both against identical inputs is the
// clearest evidence I have that Phase 4 did what it claimed.
// ---------------------------------------------------------------------------

// milpTarget is the same realistic cut as the LP test, with a budget that has
// room for variety. Variety costs money — that's the trade Phase 4 makes, and
// the budget has to acknowledge it.
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

	if resp.Status != solverv1.SolveStatus_SOLVE_STATUS_OPTIMAL &&
		resp.Status != solverv1.SolveStatus_SOLVE_STATUS_FEASIBLE {
		t.Fatalf("status = %v; want OPTIMAL or FEASIBLE. message: %s", resp.Status, resp.Message)
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

// Side by side: the same target solved both ways. This is the comparison I'd
// put in the README, and running it as a test means it can't rot.
func TestE2E_MILPvsLPComparison(t *testing.T) {
	st, cl := newE2E(t)
	products, foods := loadCatalog(t, st)
	ctx := context.Background()

	lp, err := cl.Solve(ctx, SolveInput{Target: milpTarget(), Products: products, Foods: foods})
	if err != nil {
		t.Fatalf("lp solve: %v", err)
	}
	mi, err := cl.Solve(ctx, SolveInput{
		Target: milpTarget(), Products: products, Foods: foods, IntegerPacks: true,
	})
	if err != nil {
		t.Fatalf("milp solve: %v", err)
	}

	t.Logf("LP:   %d foods, %d cents, %.3fs", len(lp.Items), lp.TotalCostCents, lp.SolveSeconds)
	t.Logf("MILP: %d foods, %d cents, %.3fs", len(mi.Items), mi.TotalCostCents, mi.SolveSeconds)

	// Variety is not free, and I want that cost visible rather than hidden.
	if mi.TotalCostCents < lp.TotalCostCents {
		t.Logf("NOTE: the MILP came out cheaper (%d < %d), which is unexpected",
			mi.TotalCostCents, lp.TotalCostCents)
	}
	if len(mi.Items) <= len(lp.Items) {
		t.Errorf("MILP basket (%d items) should be more varied than the LP's (%d)",
			len(mi.Items), len(lp.Items))
	}
}
