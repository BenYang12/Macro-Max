package store

// targets.go -> first WRITE path in the store
// A target is one user's nutrition + budget goal -> the constraints the solver must satisfy.
// Looking at the cols, one target says something like "Hit 150g protein / 200g carbs / 70g fat daily, stay under 2500 calories, spend ≤ $75/week, shop at store X, keep it vegan, and never suggest peanuts."
// target is input to my phase 4 solver
// this file inserts

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Create target inserts a target and fills in the fields the DATABASE owns.
// writes a new row
// takes a *UserTarget and returns only an error
// user_targets is the table that stores user goals so they persist
// A user creates a target once (POST/v1/targets), it gets a row + an id, and later the solver looks it up by id to generate a meal plan.
// without *UserTarget, goals would vanish between requests
// Because t is a *UserTarget, the Scan(&t.ID, *t.CreatedAt) writes into the caller's own struct
// After CreateTarget returns, the handler's UserTarget now has its ID and CreatedAt filled in — even though the function only returns error.
func (s *Store) CreateTarget(ctx context.Context, t *UserTarget) error {
	// INSERT INTO user_targets (col1, col2, ...) -> which table + which columns
	// VALUES ($1, $2, ...) -> values for those columns
	// RETURNING -> columns to hand back
	query := `
		INSERT INTO user_targets (label, protein_g_daily, carbs_g_daily, fat_g_daily,
		                          calories_max_daily, budget_cents_weekly, store_id,
		                          diet_tags, exclude_food_ids, capability_digest)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id, created_at`

	err := s.Pool.QueryRow(ctx, query,
		t.Label, t.ProteinGDaily, t.CarbsGDaily, t.FatGDaily,
		t.CaloriesMaxDaily, t.BudgetCentsWeekly, t.StoreID,
		t.DietTags, t.ExcludeFoodIDs, t.CapabilityDigest,
	).Scan(&t.ID, &t.CreatedAt) // write the generated values back through the pointer

	if err != nil {
		return fmt.Errorf("inserting target: %w", err)
	}
	return nil
}

// GetTarget returns one target by id, or ErrNotFound.
func (s *Store) GetTarget(ctx context.Context, id int64, capabilityDigest []byte) (UserTarget, error) {
	query := `
		SELECT id, label, protein_g_daily, carbs_g_daily, fat_g_daily,
		       calories_max_daily, budget_cents_weekly, store_id,
		       diet_tags, exclude_food_ids, created_at
		FROM user_targets
		WHERE id = $1 AND capability_digest = $2`

	var t UserTarget
	//.Scan() copies the values from the returned row into my Go variables, writing through the pointers I give it
	// It represents the step that gets data out of the database result and into my program.
	err := s.Pool.QueryRow(ctx, query, id, capabilityDigest).Scan(
		&t.ID, &t.Label, &t.ProteinGDaily, &t.CarbsGDaily, &t.FatGDaily,
		&t.CaloriesMaxDaily, &t.BudgetCentsWeekly, &t.StoreID,
		&t.DietTags, &t.ExcludeFoodIDs, &t.CreatedAt,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return UserTarget{}, ErrNotFound
		}
		return UserTarget{}, fmt.Errorf("querying target %d: %w", id, err)
	}
	return t, nil

}
