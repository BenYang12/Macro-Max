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
	"errors"
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

// BasketLine is a basket item with the names JOINED in.
//
// WHY A SECOND TYPE instead of adding fields to BasketItem: BasketItem is the
// WRITE shape — exactly the five columns SaveBasket inserts, nothing more.
// BasketLine is a READ shape, and it carries three things that are not columns
// in basket_items at all (the two names, and the store's own product id).
// Letting one struct be both would mean SaveBasket silently ignoring half its
// input, which is the kind of quiet asymmetry that eventually writes a bug.
//
// Both consumers of this need the names for different reasons, which is why
// they're worth the join: Claude needs FoodName to write a recipe about
// "lentils" rather than "product 41", and Kroger's cart API needs ExternalID
// because it knows nothing about my database's ids.
type BasketLine struct {
	ProductID   int64   `json:"product_id"`
	ExternalID  string  `json:"external_id"`
	ProductName string  `json:"product_name"`
	FoodName    string  `json:"food_name"`
	Packs       int     `json:"packs"`
	Grams       float64 `json:"grams"`
	CostCents   int     `json:"cost_cents"`
}

// LatestBasketForTarget returns the most recent SUCCESSFUL basket for a target,
// with product and food names joined in.
//
// "Successful" is doing real work in that sentence. SaveBasket deliberately
// records infeasible solves too, so the newest row for a target is often one
// with zero items — and generating a recipe from an empty basket, or pushing
// nothing to a cart, is nonsense. The status filter is what makes "the latest
// basket" mean "the latest basket you could actually shop from".
//
// Returns ErrNotFound when the target has never solved successfully, which the
// handlers turn into a 404 with a message that says so.
func (s *Store) LatestBasketForTarget(ctx context.Context, targetID int64) (Basket, []BasketLine, error) {
	var b Basket
	err := s.Pool.QueryRow(ctx, `
		SELECT id, target_id, store_id, solve_key, status, total_cost_cents, solver_stats
		FROM baskets
		WHERE target_id = $1
		  AND status IN ('optimal', 'feasible')
		ORDER BY created_at DESC
		LIMIT 1`, targetID).Scan(
		&b.ID, &b.TargetID, &b.StoreID, &b.SolveKey, &b.Status, &b.TotalCostCents, &b.SolverStats)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Basket{}, nil, ErrNotFound
		}
		return Basket{}, nil, fmt.Errorf("querying latest basket: %w", err)
	}

	lines, err := s.loadBasketLines(ctx, b.ID)
	if err != nil {
		return Basket{}, nil, err
	}
	return b, lines, nil
}

// BasketByIDForTarget loads the exact solved basket bound into a cart OAuth
// state. The target predicate prevents a valid basket ID from being paired
// with a different target if state construction is ever changed incorrectly.
func (s *Store) BasketByIDForTarget(ctx context.Context, basketID, targetID int64) (Basket, []BasketLine, error) {
	var b Basket
	err := s.Pool.QueryRow(ctx, `
		SELECT id, target_id, store_id, solve_key, status, total_cost_cents, solver_stats
		FROM baskets
		WHERE id = $1 AND target_id = $2 AND status IN ('optimal', 'feasible')`, basketID, targetID).Scan(
		&b.ID, &b.TargetID, &b.StoreID, &b.SolveKey, &b.Status, &b.TotalCostCents, &b.SolverStats)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Basket{}, nil, ErrNotFound
		}
		return Basket{}, nil, fmt.Errorf("querying basket by id: %w", err)
	}
	lines, err := s.loadBasketLines(ctx, b.ID)
	if err != nil {
		return Basket{}, nil, err
	}
	return b, lines, nil
}

func (s *Store) loadBasketLines(ctx context.Context, basketID int64) ([]BasketLine, error) {
	// Two JOINs avoid an N+1 query while retaining the product and food names
	// needed by recipe generation and cart integration. Expensive lines come
	// first because that is the most useful order when reviewing a basket.
	rows, err := s.Pool.Query(ctx, `
		SELECT bi.product_id, p.external_id, p.name, f.name,
		       bi.packs, bi.grams, bi.cost_cents
		FROM basket_items bi
		JOIN products p ON p.id = bi.product_id
		JOIN foods f ON f.id = p.food_id
		WHERE bi.basket_id = $1
		ORDER BY bi.cost_cents DESC`, basketID)
	if err != nil {
		return nil, fmt.Errorf("querying basket lines: %w", err)
	}
	defer rows.Close()

	lines := []BasketLine{}
	for rows.Next() {
		var line BasketLine
		if err := rows.Scan(&line.ProductID, &line.ExternalID, &line.ProductName, &line.FoodName,
			&line.Packs, &line.Grams, &line.CostCents); err != nil {
			return nil, fmt.Errorf("scanning basket line: %w", err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating basket lines: %w", err)
	}
	return lines, nil
}
