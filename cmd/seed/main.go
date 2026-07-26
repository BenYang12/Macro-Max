// Package main — the dev seeder. Loads the catalog in data.go into Postgres.

// Run with `make seed`, AFTER `make migrate-up`. Safe to re-run any number of
// times: it UPSERTS rather than inserts, so a second run updates the same rows
// instead of duplicating them or dying on a unique-key violation. That
// property is called IDEMPOTENCE, and it's what makes this a tool you can
// reach for without thinking.

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

	// A plain timeout context, not signal.NotifyContext. This is a SHORT-LIVED
	// CLI, not a server: nobody sends it SIGTERM, but a hung database
	// shouldn't leave it blocked forever. 60s is generous for ~85 statements.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	st, err := store.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	// All the work happens in seed(); main just wires and reports. Keeping
	// log.Fatal confined to main is the same "libraries return, callers
	// decide" rule from config.go.
	if err := seed(ctx, st); err != nil {
		log.Fatal(err)
	}
}

// seed writes the whole catalog inside ONE database transaction.
func seed(ctx context.Context, st *store.Store) error {
	// TRANSACTIONS — the first new concept.
	//
	// A transaction wraps many statements into one ATOMIC unit: either every
	// statement takes effect, or none does. Without one, a failure partway
	// through this seeder would leave the database half-populated — 20 foods
	// in, 22 missing, products dangling — and re-running would be guesswork.
	//
	// Begin opens it. Nothing I write below is visible to any other connection
	// until Commit.
	tx, err := st.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}

	// The rollback-defer idiom, and it deserves a careful read.
	//
	// This runs on EVERY exit path: early error returns, and also the happy
	// path after Commit. Rolling back an already-committed transaction is NOT
	// an error — pgx returns ErrTxClosed and changes nothing. So the happy
	// path is unaffected, and every failure path is guaranteed clean, even one
	// I forget to write. Ignoring the error with _ is deliberate and correct
	// here: there is nothing useful to do with it.
	defer func() { _ = tx.Rollback(ctx) }()

	// Foods first, because products need the food ids that don't exist yet.
	foodIDs, err := upsertFoods(ctx, tx)
	if err != nil {
		return err
	}

	productCount, err := upsertProducts(ctx, tx, foodIDs)
	if err != nil {
		return err
	}

	// Commit makes everything permanent and visible. Until this line, another
	// connection querying foods would see the OLD contents.
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	log.Printf("seeded %d foods and %d products", len(foodIDs), productCount)
	return nil
}

// upsertFoods writes every seed food and returns a name -> id lookup map,
// which upsertProducts needs to fill in each product's food_id foreign key.
func upsertFoods(ctx context.Context, tx pgx.Tx) (map[string]int64, error) {
	// THE UPSERT — second new concept.
	//
	// "INSERT ... ON CONFLICT ... DO UPDATE" means: try to insert; if the row
	// collides with a unique constraint, update the existing row instead. One
	// statement, no read-then-write race, no "does it already exist?" query.
	//
	// ON CONFLICT (name) names WHICH constraint to watch — foods.name UNIQUE,
	// from migration 000001. It must be a real unique constraint; Postgres
	// needs an index to detect the conflict.
	//
	// EXCLUDED is a special pseudo-table meaning "the row I TRIED to insert".
	// So `category = EXCLUDED.category` reads "overwrite the stored category
	// with the one from my seed data". Editing a number in data.go and
	// re-running therefore actually updates the database.
	//
	// Two columns are deliberately NOT in the SET list:
	//   - id: never reassign a primary key; products reference it.
	//   - fdc_id: Phase 2 owns that column. If the FDC importer has already
	//     linked a food, re-seeding must not wipe its work. An upsert should
	//     only overwrite the fields it is actually the source of truth for.
	//
	// RETURNING id — third new concept. An INSERT that hands back a column
	// from the row it just wrote. Without it I'd need a second SELECT to learn
	// each generated id. Note it returns the id on the UPDATE path too, which
	// is what makes re-runs work.
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
			updated_at         = now()
		RETURNING id`

	// BATCHING — fourth new concept.
	//
	// Sending 42 statements one at a time costs 42 network round trips. A
	// batch queues them locally and ships all of them in ONE trip, then reads
	// the 42 results back in order. Same SQL, a fraction of the latency —
	// which matters enormously over a real network, less so over localhost.
	batch := &pgx.Batch{}

	for _, f := range seedFoods {
		// Queue does NOT execute — it appends to the local queue. Nothing has
		// touched the database yet at this point in the loop.
		batch.Queue(q,
			f.Name, f.Category, f.Tags,
			f.Kcal, f.Protein, f.Carbs, f.Fat,
			nullableFloat(f.MaxGrams), // 0 in data.go becomes SQL NULL
		)
	}

	// SendBatch fires everything and returns a reader over the results.
	results := tx.SendBatch(ctx, batch)

	// Results come back in EXACTLY the order queued, and there is no key or
	// label on them — position is the only identifier. So I iterate the same
	// slice in the same order and pair each result with its food by index.
	// This is why the two loops must not diverge.
	foodIDs := make(map[string]int64, len(seedFoods))
	for _, f := range seedFoods {
		var id int64
		// QueryRow reads the NEXT queued result. RETURNING id made each one a
		// single-row result, so QueryRow (not Exec) is the right reader.
		if err := results.QueryRow().Scan(&id); err != nil {
			results.Close() // close before returning, or the tx is left wedged
			return nil, fmt.Errorf("upserting food %q: %w", f.Name, err)
		}
		foodIDs[f.Name] = id
	}

	// CRITICAL and easy to miss: the batch holds the transaction's connection
	// until closed. Trying to run another query on tx before this Close
	// deadlocks. Since upsertProducts is next, closing here is mandatory, not
	// hygiene. Close also surfaces any error the loop above didn't reach.
	if err := results.Close(); err != nil {
		return nil, fmt.Errorf("closing food batch: %w", err)
	}

	return foodIDs, nil
}

// upsertProducts writes the fake 'SEED' products, resolving each food_id from
// the map upsertFoods returned. Returns how many it wrote.
func upsertProducts(ctx context.Context, tx pgx.Tx, foodIDs map[string]int64) (int, error) {
	// The conflict target here is the COMPOSITE unique key (store_id,
	// external_id) from migration 000002 — "this store's catalog entry". It's
	// the same key Phase 5's Kroger ingestion will upsert on, so this seeder
	// is a rehearsal for the real ingestion path.
	//
	// 'SEED' is hardcoded as the store_id: these are fake products, and
	// hardcoding guarantees they can never collide with real Kroger rows.
	//
	// available and fetched_at are reset on every run — re-seeding means "this
	// is fresh data as of now", which mirrors what an ingestion run means.
	const q = `
		INSERT INTO products (food_id, store_id, external_id, name, brand,
		                      pack_size_qty, pack_size_unit, net_weight_g,
		                      price_cents, promo_price_cents, available, fetched_at)
		VALUES ($1, 'SEED', $2, $3, $4, $5, $6, $7, $8, $9, TRUE, now())
		ON CONFLICT (store_id, external_id) DO UPDATE SET
			food_id           = EXCLUDED.food_id,
			name              = EXCLUDED.name,
			brand             = EXCLUDED.brand,
			pack_size_qty     = EXCLUDED.pack_size_qty,
			pack_size_unit    = EXCLUDED.pack_size_unit,
			net_weight_g      = EXCLUDED.net_weight_g,
			price_cents       = EXCLUDED.price_cents,
			promo_price_cents = EXCLUDED.promo_price_cents,
			available         = TRUE,
			fetched_at        = now()`

	batch := &pgx.Batch{}
	queued := 0

	// A NESTED loop: each food owns zero or more products. White rice has two,
	// which is the whole point of that plant in data.go.
	for _, f := range seedFoods {
		foodID, ok := foodIDs[f.Name]
		// The two-value map read: ok reports whether the key was PRESENT.
		// Without it, a missing key silently yields 0, and every product would
		// get food_id = 0 — which the foreign key would reject with a
		// confusing error far from the real cause. Check explicitly and fail
		// with a message that names the actual problem.
		if !ok {
			return 0, fmt.Errorf("no id recorded for food %q (bug in upsertFoods)", f.Name)
		}

		for _, p := range f.Products {
			batch.Queue(q,
				foodID, p.ExternalID, p.Name,
				nullableString(p.Brand), // "" becomes SQL NULL
				p.PackQty, p.PackUnit, p.NetWeightG,
				p.PriceCents,
				nullableInt(p.PromoCents), // 0 becomes SQL NULL: not on sale
			)
			queued++
		}
	}

	results := tx.SendBatch(ctx, batch)

	// Exec, not QueryRow: these statements have no RETURNING clause, so each
	// result is just "n rows affected" with no row to scan. Asking QueryRow
	// for a row that doesn't exist would error.
	for i := 0; i < queued; i++ {
		if _, err := results.Exec(); err != nil {
			results.Close()
			return 0, fmt.Errorf("upserting product #%d: %w", i, err)
		}
	}

	if err := results.Close(); err != nil {
		return 0, fmt.Errorf("closing product batch: %w", err)
	}

	return queued, nil
}

// Zero-to-NULL converters.
//
// data.go uses plain values with 0/"" meaning "absent", because writing
// pointers in 42 struct literals would be miserable. The database wants real
// NULLs. These three functions are the boundary where one representation
// becomes the other — the same nil-vs-zero distinction as the pointer fields
// in store/models.go, just crossed in the opposite direction.
//
// The pattern is identical in all three, so read one and you've read all:
// return nil for the zero value, otherwise return the ADDRESS of the
// parameter. Taking &v is safe because v is a copy local to this call, and Go
// keeps it alive as long as the returned pointer exists (it moves to the heap
// automatically). In C this would be a dangling-pointer bug; in Go the escape
// analyzer handles it.
//
// pgx maps a nil pointer to SQL NULL and a non-nil pointer to its value.

func nullableFloat(v float64) *float64 {
	if v == 0 {
		return nil
	}
	return &v
}

func nullableInt(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

func nullableString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
