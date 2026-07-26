package store

// products.go - every query that touches the products table.
// same shape as foods.go: Query for lists, QueryRow for one, ErrNotFound for misses.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ProductFilter holds the optional list filters (?store_id=SEED&food_id=3).

// FoodID is a pointer, for the same three-state reason as
// a nullable column.

// The "off switch" trick I used for strings ($1 = '')
// relies on "" being a value nobody would ever filter BY.

// There is no such safe sentinel for an integer: 0 is a plausible-looking id, and using it as
// "unset" would silently mean "no filter" if a real 0 ever appeared.
// nil isn unambiguous, so I must use a pointer!
type ProductFilter struct {
	StoreID string // "" = any store
	FoodID  *int64 // nil = any food
}

// ListProducts returns products matching the filter, newest prices first.
func (s *Store) ListProducts(ctx context.Context, filter ProductFilter) ([]Product, error) {
	// JOIN Notes:
	// Each product row stores food_id (a number)
	// To return "Chicken Breast, raw" alongside the product, I need columns from BOTH tables -> a JOIN stitches rows together on a matching condition:
	// FROM products p JOIN foods f ON f.id = p.food_id
	// for each row in products, find the row in foods whose id equals this product's food_id, and treat the two as one wide row. Now, f.name is selectable right next to p.price_cents!
	// This is an INNER join: a product with no matching food would be dropped (returns only the rows where there is a matching value in both tables)

	// COALESCE(promo_price_cents, price_cents) — the second new idea.
	// COALESCE returns its first NON-NULL argument. On sale -> the promo
	// price; not on sale -> promo is NULL, so it falls through to the regular
	// price. This is the project's "effective price" rule, expressed once, in
	// SQL, so every caller gets the same answer.

	// The filters use both tricks side by side:
	//   ($1 = '' OR p.store_id = $1)      -- string: empty means "off"
	//   ($2::bigint IS NULL OR ...)       -- int: NULL means "off"
	// The ::bigint cast is required — Postgres can't infer a type for a bare
	// placeholder compared against nothing but NULL.

	query := `
		SELECT p.id, p.food_id, p.store_id, p.external_id,
		       p.name, p.brand,
		       p.pack_size_qty, p.pack_size_unit, p.net_weight_g,
		       p.price_cents, p.promo_price_cents,
		       COALESCE(p.promo_price_cents, p.price_cents) AS effective_price_cents,
		       p.available, p.fetched_at,
		       f.name AS food_name
		FROM products p
		JOIN foods f ON f.id = p.food_id
		WHERE ($1 = '' OR p.store_id = $1)
		  AND ($2::bigint IS NULL OR p.food_id = $2)
		ORDER BY f.name, p.net_weight_g`

	rows, err := s.Pool.Query(ctx, query, filter.StoreID, filter.FoodID)
	if err != nil {
		return nil, fmt.Errorf("querying products: %w", err)
	}
	defer rows.Close()

	products := []Product{}

	//rows.Next advances to next row (returns true if there was one, or false when there are no more)
	for rows.Next() {
		var p Product
		// Scan order must match the SELECT list exactly — including the two
		// derived columns (effective_price_cents, food_name) at their exact
		// positions. Get this order wrong and you get a type error at best,
		// silently swapped values at worst.

		err := rows.Scan( //rows.Scan() reads current row into my variables
			&p.ID, &p.FoodID, &p.StoreID, &p.ExternalID,
			&p.Name, &p.Brand,
			&p.PackSizeQty, &p.PackSizeUnit, &p.NetWeightG,
			&p.PriceCents, &p.PromoPriceCents,
			&p.EffectivePriceCents,
			&p.Available, &p.FetchedAt,
			&p.FoodName,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning product row: %w", err)
		}
		products = append(products, p)
	}
	return products, nil
}

// GetProduct returns a single product by primary key, or ErrNotFound.
// Note it reuses the SAME ErrNotFound sentinel that foods.go declared — one
// "not found" concept for the whole store package, so the handler's 404
// mapping is identical for every resource.
func (s *Store) GetProduct(ctx context.Context, id int64) (Product, error) {
	query := `
		SELECT p.id, p.food_id, p.store_id, p.external_id,
		       p.name, p.brand,
		       p.pack_size_qty, p.pack_size_unit, p.net_weight_g,
		       p.price_cents, p.promo_price_cents,
		       COALESCE(p.promo_price_cents, p.price_cents) AS effective_price_cents,
		       p.available, p.fetched_at,
		       f.name AS food_name
		FROM products p
		JOIN foods f ON f.id = p.food_id
		WHERE p.id = $1`

	var p Product
	err := s.Pool.QueryRow(ctx, query, id).Scan(
		&p.ID, &p.FoodID, &p.StoreID, &p.ExternalID,
		&p.Name, &p.Brand,
		&p.PackSizeQty, &p.PackSizeUnit, &p.NetWeightG,
		&p.PriceCents, &p.PromoPriceCents,
		&p.EffectivePriceCents,
		&p.Available, &p.FetchedAt,
		&p.FoodName,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Product{}, ErrNotFound
		}
		return Product{}, fmt.Errorf("querying product %d: %w", id, err)
	}

	return p, nil
}
