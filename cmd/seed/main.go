// Package main loads the development food catalog into Postgres.

package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/BenYang12/Macro-Max/internal/config"
	"github.com/BenYang12/Macro-Max/internal/store"
	"github.com/jackc/pgx/v5"
)

func main() {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	st, err := store.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	if err := seed(ctx, st); err != nil {
		log.Fatal(err)
	}
}

func seed(ctx context.Context, st *store.Store) error {
	tx, err := st.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	count, err := upsertFoods(ctx, tx)
	if err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	log.Printf("seeded %d foods", count)
	return nil
}

func upsertFoods(ctx context.Context, tx pgx.Tx) (int, error) {
	// fdc_id is intentionally omitted so reseeding preserves USDA links.
	const q = `
		INSERT INTO foods (name, category, tags, kcal_per_100g,
		                   protein_g_per_100g, carbs_g_per_100g, fat_g_per_100g,
		                   max_grams_per_week)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (name) DO UPDATE SET
			category           = EXCLUDED.category,
			tags               = EXCLUDED.tags,
			kcal_per_100g      = EXCLUDED.kcal_per_100g,
			protein_g_per_100g = EXCLUDED.protein_g_per_100g,
			carbs_g_per_100g   = EXCLUDED.carbs_g_per_100g,
			fat_g_per_100g     = EXCLUDED.fat_g_per_100g,
			max_grams_per_week = EXCLUDED.max_grams_per_week,
			updated_at         = now()`

	batch := &pgx.Batch{}

	for _, f := range seedFoods {
		batch.Queue(q,
			f.Name, f.Category, f.Tags,
			f.Kcal, f.Protein, f.Carbs, f.Fat,
			nullableFloat(f.MaxGrams), // 0 in data.go becomes SQL NULL
		)
	}

	results := tx.SendBatch(ctx, batch)
	for _, f := range seedFoods {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return 0, fmt.Errorf("upserting food %q: %w", f.Name, err)
		}
	}

	if err := results.Close(); err != nil {
		return 0, fmt.Errorf("closing food batch: %w", err)
	}

	return len(seedFoods), nil
}

func nullableFloat(v float64) *float64 {
	if v == 0 {
		return nil
	}
	return &v
}
