package store

// ingest.go — the write path for Kroger price ingestion.
//
// Three operations, and the interesting one is the middle:
//   UpsertProduct       insert or update, keyed on (store_id, external_id)
//   recordPriceChange   append to price_history ONLY when the price moved
//   MarkMissingUnavailable  vanished SKUs become available=false, never deleted
//
// The whole file runs inside one transaction per product, because "update the
// price" and "record that the price changed" have to either both happen or
// neither. A price row without its history entry silently loses a data point;
// a history entry without the price update is an outright lie.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// IngestProduct is one product as the ingester found it. Deliberately a
// separate type from store.Product: this is INPUT (no id yet, no timestamps),
// and reusing the output type would mean half its fields are meaningless here.
type IngestProduct struct {
	FoodID          int64
	StoreID         string // the Kroger locationId
	ExternalID      string // the Kroger productId
	Name            string
	Brand           string
	PackSizeQty     float64
	PackSizeUnit    string
	NetWeightG      float64
	PriceCents      int64
	PromoPriceCents int64 // 0 = not on sale
	Available       bool
}

// IngestResult tells the caller what actually happened, so the CLI can print a
// useful summary instead of just "done".
type IngestResult struct {
	Inserted      bool
	Updated       bool
	PriceChanged  bool
	OldPriceCents int64
	NewPriceCents int64
}

// UpsertProduct writes one product and records a price change if there was one.
func (s *Store) UpsertProduct(ctx context.Context, p IngestProduct) (IngestResult, error) {
	var res IngestResult

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("beginning ingest transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Read the CURRENT state first, because "did the price change?" is only
	// answerable by comparing against what's already there. This is a read
	// inside the same transaction as the write, which is exactly what
	// transactions are for: nobody can slip an update in between.
	var (
		existingID int64
		oldPrice   int64
		oldPromo   *int64
		found      bool
	)
	err = tx.QueryRow(ctx, `
		SELECT id, price_cents, promo_price_cents
		FROM products
		WHERE store_id = $1 AND external_id = $2`,
		p.StoreID, p.ExternalID,
	).Scan(&existingID, &oldPrice, &oldPromo)

	switch {
	case err == nil:
		found = true
	case err == pgx.ErrNoRows:
		found = false
	default:
		return res, fmt.Errorf("reading existing product: %w", err)
	}

	// nil-safe promo comparison. A promo going from "none" to a value and back
	// are both real price events, and a plain int comparison would miss the
	// first one entirely.
	var newPromo *int64
	if p.PromoPriceCents > 0 {
		v := p.PromoPriceCents
		newPromo = &v
	}

	priceMoved := !found ||
		oldPrice != p.PriceCents ||
		!sameOptionalInt64(oldPromo, newPromo)

	// The upsert itself, on the composite key the schema was designed around.
	// Note what is NOT overwritten: food_id stays put on conflict, because the
	// food a product maps to is MY curation decision, not Kroger's. Same rule
	// as the FDC importer never touching category.
	var productID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO products (food_id, store_id, external_id, name, brand,
		                      pack_size_qty, pack_size_unit, net_weight_g,
		                      price_cents, promo_price_cents, available, fetched_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11, now())
		ON CONFLICT (store_id, external_id) DO UPDATE SET
			name              = EXCLUDED.name,
			brand             = EXCLUDED.brand,
			pack_size_qty     = EXCLUDED.pack_size_qty,
			pack_size_unit    = EXCLUDED.pack_size_unit,
			net_weight_g      = EXCLUDED.net_weight_g,
			price_cents       = EXCLUDED.price_cents,
			promo_price_cents = EXCLUDED.promo_price_cents,
			available         = EXCLUDED.available,
			fetched_at        = now()
		RETURNING id`,
		p.FoodID, p.StoreID, p.ExternalID, p.Name, nullIfEmpty(p.Brand),
		p.PackSizeQty, p.PackSizeUnit, p.NetWeightG,
		p.PriceCents, newPromo, p.Available,
	).Scan(&productID)
	if err != nil {
		return res, fmt.Errorf("upserting product %s: %w", p.ExternalID, err)
	}

	// APPEND-ON-CHANGE. Writing a history row on every run would grow the table
	// by 43 rows a day forever while saying nothing new; writing only on change
	// means every row in price_history is an actual price EVENT. That makes the
	// Phase 7 price-drop alert a simple LAG() over meaningful rows instead of a
	// filter over mostly-noise.
	if priceMoved {
		_, err = tx.Exec(ctx, `
			INSERT INTO price_history (product_id, price_cents, promo_price_cents, recorded_at)
			VALUES ($1, $2, $3, now())`,
			productID, p.PriceCents, newPromo)
		if err != nil {
			return res, fmt.Errorf("recording price history: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return res, fmt.Errorf("committing ingest: %w", err)
	}

	res.Inserted = !found
	res.Updated = found
	res.PriceChanged = priceMoved && found
	res.OldPriceCents = oldPrice
	res.NewPriceCents = p.PriceCents
	return res, nil
}

// MarkMissingUnavailable flips available=false for products at this store that
// the current run did NOT see.
//
// Never DELETE. Three reasons, and any one of them would be enough:
//  1. price_history has a foreign key to products; deleting throws the history
//     away with it.
//  2. basket_items references products; deleting would break saved baskets.
//  3. A product that vanishes this week often returns next week, and I'd
//     rather flip a boolean than lose and re-create the row (with a new id).
//
// The solver already filters on available = TRUE, so flipping the flag is
// enough to take it out of circulation immediately.
func (s *Store) MarkMissingUnavailable(ctx context.Context, storeID string, seenExternalIDs []string) (int64, error) {
	// != ALL($2) is the array version of NOT IN. An empty array makes this
	// match everything, which is why the guard below exists — a run that found
	// nothing is a failed run, not a signal that the store closed.
	if len(seenExternalIDs) == 0 {
		return 0, fmt.Errorf("refusing to mark everything unavailable: the run found no products at all")
	}

	tag, err := s.Pool.Exec(ctx, `
		UPDATE products
		SET available = FALSE, fetched_at = now()
		WHERE store_id = $1
		  AND available = TRUE
		  AND external_id != ALL($2)`,
		storeID, seenExternalIDs)
	if err != nil {
		return 0, fmt.Errorf("marking missing products unavailable: %w", err)
	}
	return tag.RowsAffected(), nil
}

// PriceHistoryCount is a small helper the ingestion tests use to prove the
// append-on-change rule actually holds.
func (s *Store) PriceHistoryCount(ctx context.Context, productID int64) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx,
		`SELECT count(*) FROM price_history WHERE product_id = $1`, productID).Scan(&n)
	return n, err
}

func sameOptionalInt64(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
