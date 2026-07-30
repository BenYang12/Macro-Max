// Package main is the FDC importer: it replaces the seeder's hand-entered
// nutrition approximations with authoritative USDA FoodData Central values and
// records each food's fdc_id.
//
// Two modes, because the plan's open question ("hand-map each food to a
// Foundation fdc_id vs trusting search") deserves both answers available:
//
//	# 1. SEARCH mode — discover candidate fdc_ids for a food, print, write nothing.
//	go run ./cmd/fdcimport -search "chicken breast raw"
//
//	# 2. IMPORT mode — link one known fdc_id to one existing food, and write.
//	go run ./cmd/fdcimport -food "Chicken Breast, raw" -fdc-id 171077
//
//	# ...or import every food in the curated mapping table (mapping.go):
//	go run ./cmd/fdcimport -all
//
// Search mode exists so a human stays in the loop. Automatic name matching
// against a 600,000-row database silently picks wrong foods ("chicken breast"
// matches breaded frozen nuggets), and wrong nutrition data poisons every
// solve. The plan's recommendation — hand-map each food — is the default here.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/BenYang12/Macro-Max/internal/config"
	"github.com/BenYang12/Macro-Max/internal/fdc"
	"github.com/BenYang12/Macro-Max/internal/store"
)

func main() {
	// The flag package is the stdlib CLI parser. Each call registers one flag
	// and returns a POINTER to where the parsed value will land — the value
	// isn't there until flag.Parse() runs.
	searchQuery := flag.String("search", "", "search FDC for this term and print candidates (writes nothing)")
	foodName := flag.String("food", "", "name of the food row in our database to update")
	fdcID := flag.Int64("fdc-id", 0, "the FDC id to link to -food")
	all := flag.Bool("all", false, "import every food in the curated mapping table")
	suggest := flag.Bool("suggest", false, "search FDC for every food in the database and print ready-to-paste mapping entries")
	dryRun := flag.Bool("dry-run", false, "fetch and validate but do not write to the database")
	flag.Parse()

	cfg, err := config.LoadFromEnv()
	if err != nil {
		log.Fatal(err)
	}

	// THE API KEY CHECK, placed here rather than in config.LoadFromEnv.
	// This is the only command that needs the key, so this is the only place
	// that should refuse to start without it. Validating it in config would
	// mean `make run` and `make seed` also died over a key they never use.
	if cfg.FDCAPIKey == "" {
		log.Fatal("FDC_API_KEY is not set.\n" +
			"Get a free key at https://fdc.nal.usda.gov/api-key-signup.html\n" +
			"then add it to .env (copy .env.example if you haven't yet).")
	}

	client := fdc.New(cfg.FDCAPIKey)

	// A generous overall deadline: -all makes one request per food, and FDC is
	// not fast. Still bounded, so a hung API can't wedge the command forever.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Search mode is read-only, so it never opens a database connection.
	if *searchQuery != "" {
		if err := runSearch(ctx, client, *searchQuery); err != nil {
			log.Fatal(err)
		}
		return
	}

	// Every remaining mode writes, so from here on a database is required.
	st, err := store.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	switch {
	case *suggest:
		if err := runSuggest(ctx, client, st); err != nil {
			log.Fatal(err)
		}
	case *all:
		if err := runImportAll(ctx, client, st, *dryRun); err != nil {
			log.Fatal(err)
		}
	case *foodName != "" && *fdcID != 0:
		if err := importOne(ctx, client, st, *foodName, *fdcID, *dryRun); err != nil {
			log.Fatal(err)
		}
	default:
		// No valid mode selected: print usage and exit non-zero. flag.Usage
		// writes the auto-generated help text for every registered flag.
		fmt.Fprint(os.Stderr, "error: choose a mode: -search, -suggest, -all, or both -food and -fdc-id\n\n")
		flag.Usage()
		os.Exit(2)
	}
}

// runSearch prints candidate FDC records for a query so a human can pick one.
func runSearch(ctx context.Context, client *fdc.Client, query string) error {
	results, err := client.Search(ctx, query, fdc.PreferredDataTypes)
	if err != nil {
		return err
	}
	if len(results) == 0 {
		fmt.Printf("no results for %q\n", query)
		return nil
	}

	fmt.Printf("%d results for %q (most relevant first):\n\n", len(results), query)

	// Cap the output: FDC returns up to 50 and nobody reads past the first few.
	limit := len(results)
	if limit > 15 {
		limit = 15
	}

	for _, r := range results[:limit] {
		fmt.Printf("  fdc_id %-9d [%-11s] %s", r.FdcID, r.DataType, r.Description)
		if r.BrandOwner != "" {
			fmt.Printf("  (%s)", r.BrandOwner)
		}
		fmt.Println()
	}

	fmt.Printf("\nTo link one:\n  go run ./cmd/fdcimport -food \"<our food name>\" -fdc-id <id>\n")
	fmt.Printf("Prefer Foundation over SR Legacy over Branded.\n")
	return nil
}

// importOne fetches, normalizes, validates, and writes a single food.
//
// The order is deliberate: VALIDATE BEFORE WRITING. Nutrition that fails a
// tripwire must never reach the database, because the seeder's hand-entered
// approximation — while imprecise — is at least sane, and overwriting it with
// something impossible is a strict downgrade.
func importOne(ctx context.Context, client *fdc.Client, st *store.Store, foodName string, fdcID int64, dryRun bool) error {
	// Read our row first, for two reasons: fail fast on a typo'd food name
	// before spending an API call, and learn the category so the coherence
	// tripwire can run.
	existing, err := st.GetFoodByName(ctx, foodName)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no food named %q in the database (run `make seed` first, and check the exact spelling)", foodName)
		}
		return err
	}

	detail, err := client.Detail(ctx, fdcID)
	if err != nil {
		return fmt.Errorf("fetching fdc %d: %w", fdcID, err)
	}

	p, err := fdc.Normalize(detail)
	if err != nil {
		return fmt.Errorf("normalizing fdc %d: %w", fdcID, err)
	}

	if err := fdc.Validate(p, existing.Category); err != nil {
		// The rejection message includes the parsed values, which is what makes
		// a data problem diagnosable rather than just annoying.
		return fmt.Errorf("fdc %d (%s) failed validation for %q: %w\n"+
			"  parsed: %.1f kcal, %.1fg protein, %.1fg carbs, %.1fg fat",
			fdcID, detail.DataType, foodName, err,
			p.Kcal, p.ProteinG, p.CarbsG, p.FatG)
	}

	// Show the change either way, so a dry run is genuinely informative.
	fmt.Printf("%-34s  fdc %-9d [%s]\n", foodName, fdcID, detail.DataType)
	fmt.Printf("  kcal    %7.1f -> %7.1f\n", existing.KcalPer100g, p.Kcal)
	fmt.Printf("  protein %7.1f -> %7.1f\n", existing.ProteinGPer100g, p.ProteinG)
	fmt.Printf("  carbs   %7.1f -> %7.1f\n", existing.CarbsGPer100g, p.CarbsG)
	fmt.Printf("  fat     %7.1f -> %7.1f\n", existing.FatGPer100g, p.FatG)

	if dryRun {
		fmt.Println("  (dry run: nothing written)")
		return nil
	}

	if err := st.UpdateFoodNutrition(ctx, foodName, p.FdcID, p.Kcal, p.ProteinG, p.CarbsG, p.FatG); err != nil {
		return err
	}
	fmt.Println("  written")
	return nil
}

// runImportAll walks the curated mapping table in mapping.go.
//
// It does NOT stop on the first failure. A single food with bad FDC data
// shouldn't block the other 41 — so failures are collected, reported at the
// end, and reflected in the exit status. Partial success is the honest outcome
// for a batch job over third-party data.
func runImportAll(ctx context.Context, client *fdc.Client, st *store.Store, dryRun bool) error {
	var failures []string

	for _, m := range curatedMapping {
		if err := importOne(ctx, client, st, m.FoodName, m.FdcID, dryRun); err != nil {
			// Log and continue, rather than return.
			log.Printf("SKIPPED %s: %v", m.FoodName, err)
			failures = append(failures, m.FoodName)
		}

		// Be a good API citizen. FDC's default quota is 1,000 requests/hour;
		// this pause keeps a 42-food run far under it and avoids the 429 path
		// entirely. Deliberately simple — Phase 5 introduces real token-bucket
		// rate limiting for Kroger, where the volume actually demands it.
		time.Sleep(200 * time.Millisecond)
	}

	imported := len(curatedMapping) - len(failures)
	fmt.Printf("\n%d/%d foods imported\n", imported, len(curatedMapping))

	if len(failures) > 0 {
		return fmt.Errorf("%d food(s) failed: %v", len(failures), failures)
	}
	return nil
}

// runSuggest searches FDC for every food in our database and prints candidate
// mapping entries as Go source, ready to paste into mapping.go.
//
// It writes NOTHING to the database, and it deliberately prints the FDC
// description alongside each id: the whole point is that a human reads those
// descriptions and rejects the wrong ones. A tool that hid the descriptions and
// just emitted ids would be automating precisely the judgment that must not be
// automated.
func runSuggest(ctx context.Context, client *fdc.Client, st *store.Store) error {
	foods, err := st.ListFoods(ctx, store.FoodFilter{})
	if err != nil {
		return err
	}
	if len(foods) == 0 {
		return fmt.Errorf("no foods in the database; run `make seed` first")
	}

	fmt.Printf("// Suggestions for %d foods. VERIFY EACH DESCRIPTION before pasting.\n", len(foods))
	fmt.Printf("// Prefer Foundation over SR Legacy over Branded.\n\n")

	var unmatched []string

	for _, f := range foods {
		// Search using our food name as the query. Restricting to the two
		// authoritative data types keeps manufacturer label data out of the
		// suggestions entirely — if nothing lab-measured matches, that is worth
		// knowing explicitly rather than papering over with a Branded hit.
		results, err := client.Search(ctx, f.Name,
			[]string{fdc.DataTypeFoundation, fdc.DataTypeSRLegacy})
		if err != nil {
			return fmt.Errorf("searching for %q: %w", f.Name, err)
		}

		if len(results) == 0 {
			unmatched = append(unmatched, f.Name)
			fmt.Printf("// NO MATCH for %q — search manually:\n", f.Name)
			fmt.Printf("//   go run ./cmd/fdcimport -search %q\n\n", f.Name)
			continue
		}

		top := results[0]
		fmt.Printf("{\n")
		fmt.Printf("\tFoodName: %q,\n", f.Name)
		fmt.Printf("\tFdcID:    %d,\n", top.FdcID)
		fmt.Printf("\tNote:     %q,\n", top.DataType+": "+top.Description)
		fmt.Printf("},\n")

		// Show the runners-up as comments. The top hit is frequently not the
		// right one, and having alternatives inline saves a second search.
		for _, alt := range results[1:min(4, len(results))] {
			fmt.Printf("// alt: %d [%s] %s\n", alt.FdcID, alt.DataType, alt.Description)
		}
		fmt.Println()

		time.Sleep(200 * time.Millisecond) // stay well under the hourly quota
	}

	if len(unmatched) > 0 {
		fmt.Printf("\n// %d food(s) had no Foundation/SR Legacy match: %v\n", len(unmatched), unmatched)
	}
	return nil
}
