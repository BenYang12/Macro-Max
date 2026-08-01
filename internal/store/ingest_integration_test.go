package store

// Integration tests for the ingestion write path. These need a real Postgres
// because the whole point is the interaction between the upsert, the history
// table, and the foreign keys — none of which a fake can model.

import (
	"context"
	"strings"
	"testing"
)

func ingestFixture(t *testing.T, st *Store, foodID int64, price, promo int64) IngestProduct {
	t.Helper()
	return IngestProduct{
		FoodID:          foodID,
		StoreID:         "__TEST_STORE__",
		ExternalID:      "ext-ingest-1",
		Name:            "Test Chicken 16 oz",
		Brand:           "Kroger",
		PackSizeQty:     16,
		PackSizeUnit:    "oz",
		NetWeightG:      453.6,
		PriceCents:      price,
		PromoPriceCents: promo,
		Available:       true,
	}
}

func cleanupStore(t *testing.T, st *Store, storeID string) {
	t.Cleanup(func() {
		ctx := context.Background()
		// Children before parents: price_history and basket_items reference
		// products, and products references foods.
		st.Pool.Exec(ctx, `DELETE FROM price_history WHERE product_id IN
			(SELECT id FROM products WHERE store_id = $1)`, storeID)
		st.Pool.Exec(ctx, `DELETE FROM products WHERE store_id = $1`, storeID)
	})
}

func TestUpsertProduct_InsertsThenUpdates(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	foodID := insertTestFood(t, st, "__test_ingest_food__", "protein")
	cleanupStore(t, st, "__TEST_STORE__")

	// First run: a brand new product.
	res, err := st.UpsertProduct(ctx, ingestFixture(t, st, foodID, 399, 0))
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if !res.Inserted {
		t.Error("first upsert should report Inserted")
	}

	// Second run, same price: an update, and NO new history row.
	res, err = st.UpsertProduct(ctx, ingestFixture(t, st, foodID, 399, 0))
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if !res.Updated {
		t.Error("second upsert should report Updated")
	}
	if res.PriceChanged {
		t.Error("price did not change; PriceChanged should be false")
	}
}

// THE APPEND-ON-CHANGE RULE, which is the whole reason price_history exists in
// a useful form. Running the ingester ten times at the same price must leave
// exactly one history row.
func TestUpsertProduct_HistoryOnlyGrowsOnPriceChange(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	foodID := insertTestFood(t, st, "__test_history_food__", "protein")
	cleanupStore(t, st, "__TEST_STORE__")

	// Three runs at the same price.
	for i := 0; i < 3; i++ {
		if _, err := st.UpsertProduct(ctx, ingestFixture(t, st, foodID, 399, 0)); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}

	var productID int64
	err := st.Pool.QueryRow(ctx,
		`SELECT id FROM products WHERE store_id='__TEST_STORE__' AND external_id='ext-ingest-1'`).
		Scan(&productID)
	if err != nil {
		t.Fatalf("finding product: %v", err)
	}

	n, err := st.PriceHistoryCount(ctx, productID)
	if err != nil {
		t.Fatalf("counting history: %v", err)
	}
	if n != 1 {
		t.Errorf("history rows = %d after 3 identical runs; want 1", n)
	}

	// Now the price moves: exactly one more row.
	if _, err := st.UpsertProduct(ctx, ingestFixture(t, st, foodID, 449, 0)); err != nil {
		t.Fatalf("price change run: %v", err)
	}
	n, _ = st.PriceHistoryCount(ctx, productID)
	if n != 2 {
		t.Errorf("history rows = %d after a price change; want 2", n)
	}
}

// A promo starting is a price event even though the regular price is unchanged.
// This is the case a naive `oldPrice != newPrice` check misses entirely.
func TestUpsertProduct_PromoChangeCountsAsAPriceChange(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	foodID := insertTestFood(t, st, "__test_promo_food__", "protein")
	cleanupStore(t, st, "__TEST_STORE__")

	st.UpsertProduct(ctx, ingestFixture(t, st, foodID, 399, 0))   // no promo
	st.UpsertProduct(ctx, ingestFixture(t, st, foodID, 399, 299)) // sale starts

	var productID int64
	st.Pool.QueryRow(ctx,
		`SELECT id FROM products WHERE store_id='__TEST_STORE__' AND external_id='ext-ingest-1'`).
		Scan(&productID)

	n, _ := st.PriceHistoryCount(ctx, productID)
	if n != 2 {
		t.Errorf("history rows = %d; want 2 (a sale starting is a price event)", n)
	}

	// And the sale ENDING is another one.
	st.UpsertProduct(ctx, ingestFixture(t, st, foodID, 399, 0))
	n, _ = st.PriceHistoryCount(ctx, productID)
	if n != 3 {
		t.Errorf("history rows = %d; want 3 (a sale ending is also an event)", n)
	}
}

func TestMarkMissingUnavailable_FlipsTheFlagWithoutDeleting(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	foodID := insertTestFood(t, st, "__test_missing_food__", "protein")
	cleanupStore(t, st, "__TEST_STORE__")

	// Two products this run.
	a := ingestFixture(t, st, foodID, 399, 0)
	a.ExternalID = "ext-still-here"
	b := ingestFixture(t, st, foodID, 499, 0)
	b.ExternalID = "ext-vanished"
	st.UpsertProduct(ctx, a)
	st.UpsertProduct(ctx, b)

	// Next run only sees the first one.
	n, err := st.MarkMissingUnavailable(ctx, "__TEST_STORE__", []string{"ext-still-here"})
	if err != nil {
		t.Fatalf("MarkMissingUnavailable: %v", err)
	}
	if n != 1 {
		t.Errorf("marked %d unavailable; want 1", n)
	}

	// The row must still EXIST — history and baskets depend on it.
	var available bool
	err = st.Pool.QueryRow(ctx,
		`SELECT available FROM products WHERE store_id='__TEST_STORE__' AND external_id='ext-vanished'`).
		Scan(&available)
	if err != nil {
		t.Fatalf("the vanished product row was deleted, not flagged: %v", err)
	}
	if available {
		t.Error("vanished product is still marked available")
	}
}

// The safety valve. A run that returns nothing is almost certainly a broken run
// (bad token, wrong store id, network) — NOT a store that stopped selling food.
// Wiping the whole catalog on that basis would be a self-inflicted outage.
func TestMarkMissingUnavailable_RefusesToWipeEverything(t *testing.T) {
	st := newTestStore(t)

	_, err := st.MarkMissingUnavailable(context.Background(), "__TEST_STORE__", nil)
	if err == nil {
		t.Fatal("expected a refusal when the seen-list is empty")
	}
	if !strings.Contains(err.Error(), "refusing") {
		t.Errorf("error %q should explain the refusal", err)
	}
}
