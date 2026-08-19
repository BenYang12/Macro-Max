package store

// products.go - every query that touches the products table.
// same shape as foods.go: Query for lists, QueryRow for one, ErrNotFound for misses.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ProductFilter holds the optional product-list filters.

// FoodID is a pointer, for the same three-state reason as
// a nullable column.

// The "off switch" trick I used for strings ($1 = '')
// relies on "" being a value nobody would ever filter BY.

// There is no such safe sentinel for an integer: 0 is a plausible-looking id, and using it as
// "unset" would silently mean "no filter" if a real 0 ever appeared.
// nil isn unambiguous, so I must use a pointer!
type ProductFilter struct {
	// StoreID is REQUIRED. It once accepted "" as "any store", but the catalog
	// is fixed to one location: the handler always supplies
	// UniversityPlaceStoreID and rejects a client-supplied store_id outright,
	// so no caller could ever reach the unfiltered branch. Keeping it meant a
	// cross-store query was one empty string away, and the planner paid for an
	// OR it could never satisfy.
	StoreID string
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

	// The food filter is still optional:
	//   ($2::bigint IS NULL OR ...)       -- int: NULL means "off"
	// The ::bigint cast is required — Postgres can't infer a type for a bare
	// placeholder compared against nothing but NULL. The store filter is not
	// optional; see ProductFilter.StoreID.

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
		WHERE p.store_id = $1
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
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating product rows: %w", err)
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

// ListSolveCandidates loads every product a solve should consider at one store,
// respecting the target's diet filters and exclusions.
//
// Why this is its OWN query rather than reusing ListProducts: the solver needs
// filtering that the public products endpoint has no business doing (diet tags,
// excluded food ids), and it needs only AVAILABLE products. Bolting four more
// optional parameters onto ListProducts would make a general-purpose function
// serve one specialized caller badly. A second query is cheaper than a
// confusing first one.
func (s *Store) ListSolveCandidates(ctx context.Context, storeID string, dietTags []string, excludeFoodIDs []int64) ([]Product, error) {
	// The two new filter idioms here:
	//
	//   f.tags @> $2   — the food must carry EVERY tag the user requires. This
	//                    is the same containment operator as the tag filter on
	//                    /v1/foods, but with a multi-element right side, so it
	//                    means "superset of", which is exactly what a diet
	//                    filter is: vegan AND gluten_free, not either.
	//
	//   NOT (f.id = ANY($3)) — exclusion. ANY() compares a scalar against every
	//                    element of an array. An empty array makes this
	//                    trivially true, so "no exclusions" needs no special
	//                    case in the Go code.
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
		WHERE p.store_id = $1
		  AND p.available = TRUE
		  AND p.net_weight_g > 0
		  AND COALESCE(p.promo_price_cents, p.price_cents) > 0
		  AND f.tags @> $2
		  AND NOT (f.id = ANY($3))
		ORDER BY f.name, p.net_weight_g`

	// pgx maps a nil slice to SQL NULL, and `tags @> NULL` is NULL (not true),
	// which would silently return zero rows. Normalizing nil to an empty array
	// keeps "no filter" meaning "no filter".
	if dietTags == nil {
		dietTags = []string{}
	}
	if excludeFoodIDs == nil {
		excludeFoodIDs = []int64{}
	}

	rows, err := s.Pool.Query(ctx, query, storeID, dietTags, excludeFoodIDs)
	if err != nil {
		return nil, fmt.Errorf("querying solve candidates: %w", err)
	}
	defer rows.Close()

	products := []Product{}
	for rows.Next() {
		var p Product
		err := rows.Scan(
			&p.ID, &p.FoodID, &p.StoreID, &p.ExternalID,
			&p.Name, &p.Brand,
			&p.PackSizeQty, &p.PackSizeUnit, &p.NetWeightG,
			&p.PriceCents, &p.PromoPriceCents,
			&p.EffectivePriceCents,
			&p.Available, &p.FetchedAt,
			&p.FoodName,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning solve candidate: %w", err)
		}
		products = append(products, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating solve candidates: %w", err)
	}
	return products, nil
}

// ListFoodsByIDs loads the foods backing a set of products, keyed by id.
//
// I return a MAP rather than a slice because the caller's next move is always
// "given this product's food_id, what's its nutrition?" — a lookup, not a scan.
// Returning a slice would force every caller to build this map themselves.
func (s *Store) ListFoodsByIDs(ctx context.Context, ids []int64) (map[int64]Food, error) {
	if len(ids) == 0 {
		return map[int64]Food{}, nil
	}

	// = ANY($1) with an array parameter, rather than building an IN (...) list
	// with N placeholders. One static query string for any number of ids, and
	// no string concatenation anywhere near user input.
	query := `
		SELECT id, name, fdc_id, category, tags,
		       kcal_per_100g, protein_g_per_100g, carbs_g_per_100g, fat_g_per_100g,
		       max_grams_per_week, created_at, updated_at
		FROM foods
		WHERE id = ANY($1)`

	rows, err := s.Pool.Query(ctx, query, ids)
	if err != nil {
		return nil, fmt.Errorf("querying foods by id: %w", err)
	}
	defer rows.Close()

	out := make(map[int64]Food, len(ids))
	for rows.Next() {
		var f Food
		err := rows.Scan(
			&f.ID, &f.Name, &f.FdcID, &f.Category, &f.Tags,
			&f.KcalPer100g, &f.ProteinGPer100g, &f.CarbsGPer100g, &f.FatGPer100g,
			&f.MaxGramsPerWeek, &f.CreatedAt, &f.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning food: %w", err)
		}
		out[f.ID] = f
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating foods: %w", err)
	}
	return out, nil
}
