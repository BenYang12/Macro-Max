package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestBasketLookupsSelectAndOrderLines(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()

	makeTarget := func(label string) UserTarget {
		t.Helper()
		target := UserTarget{
			Label: label, ProteinGDaily: 100, CarbsGDaily: 100, FatGDaily: 50,
			BudgetCentsWeekly: 5000, StoreID: "__BASKET_TEST__",
			DietTags: []string{}, ExcludeFoodIDs: []int64{}, CapabilityDigest: make([]byte, 32),
		}
		if err := st.CreateTarget(ctx, &target); err != nil {
			t.Fatalf("CreateTarget: %v", err)
		}
		return target
	}
	target := makeTarget(fmt.Sprintf("__basket_target_%d", suffix))
	otherTarget := makeTarget(fmt.Sprintf("__basket_other_target_%d", suffix))

	productIDs := make([]int64, 0, 2)
	foodIDs := make([]int64, 0, 2)
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		st.Pool.Exec(cleanupCtx, `DELETE FROM baskets WHERE target_id = ANY($1)`, []int64{target.ID, otherTarget.ID})
		st.Pool.Exec(cleanupCtx, `DELETE FROM user_targets WHERE id = ANY($1)`, []int64{target.ID, otherTarget.ID})
		st.Pool.Exec(cleanupCtx, `DELETE FROM products WHERE id = ANY($1)`, productIDs)
		st.Pool.Exec(cleanupCtx, `DELETE FROM foods WHERE id = ANY($1)`, foodIDs)
	})
	for i := 0; i < 2; i++ {
		var foodID, productID int64
		if err := st.Pool.QueryRow(ctx, `
			INSERT INTO foods (name, category, kcal_per_100g,
			                   protein_g_per_100g, carbs_g_per_100g, fat_g_per_100g)
			VALUES ($1, 'protein', 100, 20, 0, 2)
			RETURNING id`, fmt.Sprintf("__basket_food_%d_%d", suffix, i)).Scan(&foodID); err != nil {
			t.Fatalf("inserting food: %v", err)
		}
		foodIDs = append(foodIDs, foodID)
		if err := st.Pool.QueryRow(ctx, `
			INSERT INTO products (food_id, store_id, external_id, name,
			                      pack_size_qty, pack_size_unit, net_weight_g, price_cents)
			VALUES ($1, '__BASKET_TEST__', $2, $3, 1, 'kg', 1000, 500)
			RETURNING id`, foodID, fmt.Sprintf("basket-ext-%d-%d", suffix, i),
			fmt.Sprintf("Basket product %d", i)).Scan(&productID); err != nil {
			t.Fatalf("inserting product: %v", err)
		}
		productIDs = append(productIDs, productID)
	}

	older := Basket{TargetID: target.ID, StoreID: target.StoreID, SolveKey: fmt.Sprintf("older-%d", suffix), Status: "optimal", TotalCostCents: 1000}
	if err := st.SaveBasket(ctx, &older, []BasketItem{
		{ProductID: productIDs[0], Packs: 1, Grams: 500, CostCents: 300},
		{ProductID: productIDs[1], Packs: 1, Grams: 500, CostCents: 700},
	}); err != nil {
		t.Fatalf("SaveBasket older: %v", err)
	}
	newer := Basket{TargetID: target.ID, StoreID: target.StoreID, SolveKey: fmt.Sprintf("newer-%d", suffix), Status: "feasible", TotalCostCents: 500}
	if err := st.SaveBasket(ctx, &newer, []BasketItem{{ProductID: productIDs[0], Packs: 1, Grams: 400, CostCents: 500}}); err != nil {
		t.Fatalf("SaveBasket newer: %v", err)
	}
	if _, err := st.Pool.Exec(ctx, `UPDATE baskets SET created_at = created_at + interval '1 second' WHERE id = $1`, newer.ID); err != nil {
		t.Fatalf("ordering basket fixtures: %v", err)
	}

	latest, latestLines, err := st.LatestBasketForTarget(ctx, target.ID)
	if err != nil {
		t.Fatalf("LatestBasketForTarget: %v", err)
	}
	if latest.ID != newer.ID || len(latestLines) != 1 || latestLines[0].ProductID != productIDs[0] {
		t.Fatalf("latest basket = %d with lines %+v; want basket %d and its one line", latest.ID, latestLines, newer.ID)
	}

	exact, exactLines, err := st.BasketByIDForTarget(ctx, older.ID, target.ID)
	if err != nil {
		t.Fatalf("BasketByIDForTarget: %v", err)
	}
	if exact.ID != older.ID {
		t.Fatalf("exact basket id = %d; want %d", exact.ID, older.ID)
	}
	if len(exactLines) != 2 || exactLines[0].CostCents != 700 || exactLines[1].CostCents != 300 {
		t.Fatalf("exact basket lines = %+v; want descending costs 700, 300", exactLines)
	}

	if _, _, err := st.BasketByIDForTarget(ctx, older.ID, otherTarget.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("basket paired with wrong target: got %v; want ErrNotFound", err)
	}
}
