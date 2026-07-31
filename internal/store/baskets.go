package store

// baskets.go — persisting solver results.
//
// Every solve gets written down, even infeasible ones. That's deliberate: the
// history of what a user asked for and what came back is the raw material for
// everything interesting later (did their budget trend up? does this store
// consistently fail vegan targets?), and it costs one insert to keep.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Basket mirrors one row of the baskets table.
type Basket struct {
	ID             int64           `json:"id"`
	TargetID       int64           `json:"target_id"`
	StoreID        string          `json:"store_id"`
	SolveKey       string          `json:"solve_key"`
	Status         string          `json:"status"`
	TotalCostCents int             `json:"total_cost_cents"`
	SolverStats    json.RawMessage `json:"solver_stats,omitempty"`
}

// BasketItem is one line.
type BasketItem struct {
	ProductID int64   `json:"product_id"`
	Packs     int     `json:"packs"`
	Grams     float64 `json:"grams"`
	CostCents int     `json:"cost_cents"`
}

// SaveBasket writes a basket and its items in ONE transaction.
//
// The transaction matters more here than it looks. A basket row without its
// items is a silent lie — it claims a $38 result with nothing in it — and
// there's no constraint that could catch that, because "zero items" is valid
// for an infeasible solve. Atomicity is the only thing standing between me and
// half-written results.
func (s *Store) SaveBasket(ctx context.Context, b *Basket, items []BasketItem) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning basket transaction: %w", err)
	}
	// Same rollback-defer idiom as the seeder: harmless after a successful
	// commit, and it guarantees no path leaks an open transaction.
	defer func() { _ = tx.Rollback(ctx) }()

	err = tx.QueryRow(ctx, `
		INSERT INTO baskets (target_id, store_id, solve_key, status,
		                     total_cost_cents, solver_stats)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id`,
		b.TargetID, b.StoreID, b.SolveKey, b.Status, b.TotalCostCents, b.SolverStats,
	).Scan(&b.ID)
	if err != nil {
		return fmt.Errorf("inserting basket: %w", err)
	}

	if len(items) == 0 {
		// An infeasible or error solve has no lines, and that's a legitimate
		// record — I still want the row saying "this was asked, and failed".
		return tx.Commit(ctx)
	}

	// CopyFrom is pgx's binary bulk-load path (Postgres COPY). For a handful of
	// rows it's overkill versus a batch, but it's the right reflex for
	// "insert N rows I already have in memory", and it degrades gracefully as
	// baskets grow.
	rows := make([][]any, 0, len(items))
	for _, it := range items {
		rows = append(rows, []any{b.ID, it.ProductID, it.Packs, it.Grams, it.CostCents})
	}

	_, err = tx.CopyFrom(ctx,
		pgx.Identifier{"basket_items"},
		[]string{"basket_id", "product_id", "packs", "grams", "cost_cents"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return fmt.Errorf("inserting basket items: %w", err)
	}

	return tx.Commit(ctx)
}

// GetBasketBySolveKey finds a previously-computed basket. Phase 4 serves
// repeats from Redis, so this is mainly for history and debugging — "show me
// what this exact request returned last time" without depending on the cache
// still holding it.
func (s *Store) GetBasketBySolveKey(ctx context.Context, key string) (Basket, []BasketItem, error) {
	var b Basket
	err := s.Pool.QueryRow(ctx, `
		SELECT id, target_id, store_id, solve_key, status, total_cost_cents, solver_stats
		FROM baskets
		WHERE solve_key = $1
		ORDER BY created_at DESC
		LIMIT 1`, key).Scan(
		&b.ID, &b.TargetID, &b.StoreID, &b.SolveKey, &b.Status, &b.TotalCostCents, &b.SolverStats)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Basket{}, nil, ErrNotFound
		}
		return Basket{}, nil, fmt.Errorf("querying basket: %w", err)
	}

	rows, err := s.Pool.Query(ctx, `
		SELECT product_id, packs, grams, cost_cents
		FROM basket_items WHERE basket_id = $1`, b.ID)
	if err != nil {
		return Basket{}, nil, fmt.Errorf("querying basket items: %w", err)
	}
	defer rows.Close()

	items := []BasketItem{}
	for rows.Next() {
		var it BasketItem
		if err := rows.Scan(&it.ProductID, &it.Packs, &it.Grams, &it.CostCents); err != nil {
			return Basket{}, nil, fmt.Errorf("scanning basket item: %w", err)
		}
		items = append(items, it)
	}
	return b, items, rows.Err()
}
