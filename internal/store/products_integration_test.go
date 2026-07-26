package store

// products_integration_test.go — real SQL against real Postgres, same
// self-skip contract as the foods integration test.
//
// New technique: these tests INSERT their own fixture data instead of assuming
// what's in the database. That makes them independent of the seeder and lets
// me assert exact values (a specific promo price) rather than vague shapes.

import (
	"context"
	"errors"
	"testing"
)

// insertTestProduct creates a food and a product for one test, and registers
// cleanup that deletes them afterward.
//
// The DELETE order matters and is not arbitrary: products.food_id is a foreign
// key REFERENCING foods(id), so deleting the food first would be refused by
// the database ("still referenced"). Children die before parents.
//
// It returns both ids so the test can query for exactly what it created.
func insertTestProduct(t *testing.T, st *Store, promoCents *int64) (foodID, productID int64) {
	t.Helper()
	ctx := context.Background()

	// RETURNING id is a Postgres feature worth knowing: an INSERT that hands
	// back a column from the row it just created. Without it I'd have to
	// insert, then run a second SELECT to discover the generated id — a race
	// and an extra round trip. Because it returns a row, this is QueryRow.
	err := st.Pool.QueryRow(ctx, `
		INSERT INTO foods (name, category, kcal_per_100g,
		                   protein_g_per_100g, carbs_g_per_100g, fat_g_per_100g)
		VALUES ('__test_food__', 'protein', 100, 20, 0, 2)
		RETURNING id`).Scan(&foodID)
	if err != nil {
		t.Fatalf("inserting test food: %v", err)
	}

	err = st.Pool.QueryRow(ctx, `
		INSERT INTO products (food_id, store_id, external_id, name,
		                      pack_size_qty, pack_size_unit, net_weight_g,
		                      price_cents, promo_price_cents)
		VALUES ($1, '__TEST__', 'ext-1', 'Test Chicken 2.5lb',
		        2.5, 'lb', 1134.0, 899, $2)
		RETURNING id`, foodID, promoCents).Scan(&productID)
	if err != nil {
		t.Fatalf("inserting test product: %v", err)
	}

	// Cleanup runs even if the test fails, so a failed run doesn't poison the
	// next one with leftover rows. Children (products) first, then parent.
	t.Cleanup(func() {
		ctx := context.Background()
		st.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, productID)
		st.Pool.Exec(ctx, `DELETE FROM foods WHERE id = $1`, foodID)
	})

	return foodID, productID
}

// TestListProducts_FiltersAndJoinsFoodName checks three things at once: the
// store filter works, the JOIN populated food_name, and the filter is narrow
// enough that only my fixture comes back.
func TestListProducts_FiltersAndJoinsFoodName(t *testing.T) {
	st := newTestStore(t) // reused from foods_integration_test.go — same package
	ctx := context.Background()

	foodID, _ := insertTestProduct(t, st, nil) // nil = no promo price

	products, err := st.ListProducts(ctx, ProductFilter{
		StoreID: "__TEST__",
		FoodID:  &foodID, // & takes the address, giving the *int64 the filter wants
	})
	if err != nil {
		t.Fatalf("ListProducts returned an error: %v", err)
	}

	if len(products) != 1 {
		t.Fatalf("got %d products; want exactly 1", len(products))
	}

	p := products[0]

	// The JOIN's payoff: a column from the OTHER table came back.
	if p.FoodName != "__test_food__" {
		t.Errorf("food_name = %q; want %q (did the JOIN work?)", p.FoodName, "__test_food__")
	}

	// No promo -> COALESCE falls through to the regular price.
	if p.EffectivePriceCents != 899 {
		t.Errorf("effective price = %d; want 899 (the regular price)", p.EffectivePriceCents)
	}
}

// TestListProducts_EffectivePriceUsesPromo is the other half of COALESCE:
// when a promo price EXISTS, it must win.
func TestListProducts_EffectivePriceUsesPromo(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// A *int64 needs an addressable variable — I can't write &699 in Go,
	// because a literal has no address. Hence the named variable.
	promo := int64(699)
	foodID, _ := insertTestProduct(t, st, &promo)

	products, err := st.ListProducts(ctx, ProductFilter{StoreID: "__TEST__", FoodID: &foodID})
	if err != nil {
		t.Fatalf("ListProducts returned an error: %v", err)
	}
	if len(products) != 1 {
		t.Fatalf("got %d products; want exactly 1", len(products))
	}

	if products[0].EffectivePriceCents != 699 {
		t.Errorf("effective price = %d; want 699 (the promo price should win)",
			products[0].EffectivePriceCents)
	}
}

// TestGetProduct_UnknownIDIsNotFound — same sentinel contract as foods.
func TestGetProduct_UnknownIDIsNotFound(t *testing.T) {
	st := newTestStore(t)

	_, err := st.GetProduct(context.Background(), 999_999_999)

	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for a missing id; got %v", err)
	}
}
