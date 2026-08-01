// Package main is the Kroger price ingester.
//
//	# find a store near a zip (read-only, writes nothing)
//	go run ./cmd/krogeringest -zip 45202
//
//	# see what WOULD be written, without writing it
//	go run ./cmd/krogeringest -location 01400376 -dry-run
//
//	# actually ingest
//	go run ./cmd/krogeringest -location 01400376
//
// THE SHAPE OF THIS PROGRAM: for each of my ~42 foods, search Kroger, filter
// the results down to ones I trust, convert their sizes to grams, and upsert.
// The work is embarrassingly parallel (each food is independent), which makes
// it the right place to finally use a worker pool.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/BenYang12/Macro-Max/internal/config"
	"github.com/BenYang12/Macro-Max/internal/kroger"
	"github.com/BenYang12/Macro-Max/internal/store"
)

// How many foods to process at once. The rate limiter inside the client is the
// real governor — this just decides how many goroutines are queued behind it.
const workers = 4

// At most this many products per food. My earlier design decision: enough pack
// sizes to give the MILP a real integer choice, not so many that the catalog
// fills with near-duplicates.
const maxProductsPerFood = 4

func main() {
	zip := flag.String("zip", "", "find stores near this zip code and print them (writes nothing)")
	locationID := flag.String("location", "", "Kroger locationId to ingest prices from")
	dryRun := flag.Bool("dry-run", false, "fetch and parse but do not write to the database")
	only := flag.String("only", "", "ingest just this one food name (for debugging a single mapping)")
	flag.Parse()

	cfg, err := config.LoadFromEnv()
	if err != nil {
		log.Fatal(err)
	}

	// Credentials are checked HERE, in the one command that needs them — the
	// same rule I used for FDC_API_KEY. `make run` must never refuse to start
	// over a key it never touches.
	if cfg.KrogerClientID == "" || cfg.KrogerClientSecret == "" {
		log.Fatal("KROGER_CLIENT_ID and KROGER_CLIENT_SECRET must be set.\n" +
			"Register an app at https://developer.kroger.com/ and add both to .env\n" +
			"(copy .env.example if you haven't yet).")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	client := kroger.New(cfg.KrogerClientID, cfg.KrogerClientSecret, nil)

	// Store lookup is read-only, so it never opens a database connection.
	if *zip != "" {
		if err := runLocations(ctx, client, *zip); err != nil {
			log.Fatal(err)
		}
		return
	}

	if *locationID == "" {
		fmt.Fprint(os.Stderr, "error: pass -zip to find a store, or -location to ingest\n\n")
		flag.Usage()
		os.Exit(2)
	}

	st, err := store.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	if err := runIngest(ctx, client, st, *locationID, *dryRun, *only); err != nil {
		log.Fatal(err)
	}
}

func runLocations(ctx context.Context, client *kroger.Client, zip string) error {
	locations, err := client.Locations(ctx, zip, 10)
	if err != nil {
		return err
	}
	if len(locations) == 0 {
		return fmt.Errorf("no Kroger-family stores found near %s", zip)
	}

	fmt.Printf("%d stores near %s:\n\n", len(locations), zip)
	for _, l := range locations {
		fmt.Printf("  locationId %-12s %s (%s)\n", l.LocationID, l.Name, l.Chain)
		fmt.Printf("  %s%s, %s %s\n\n", strings.Repeat(" ", 25),
			l.Address.AddressLine1, l.Address.City, l.Address.State)
	}
	fmt.Printf("Then ingest with:\n  go run ./cmd/krogeringest -location <locationId> -dry-run\n")
	return nil
}

// foodResult is what one worker produces. Collecting structs rather than
// writing to shared state from inside the goroutines is the simplest way to
// stay race-free: each worker owns its result until the pool is done.
type foodResult struct {
	FoodName string
	Products []store.IngestProduct
	Skipped  []string // human-readable reasons, for the log
	Err      error
}

func runIngest(ctx context.Context, client *kroger.Client, st *store.Store,
	locationID string, dryRun bool, only string) error {

	// Resolve my curated terms against the foods actually in the database. A
	// mapping entry whose food doesn't exist is a typo, and I want to know
	// immediately rather than silently ingesting 41 of 42 foods.
	foods, err := st.ListFoods(ctx, store.FoodFilter{})
	if err != nil {
		return fmt.Errorf("loading foods: %w", err)
	}
	byName := make(map[string]store.Food, len(foods))
	for _, f := range foods {
		byName[f.Name] = f
	}

	var work []foodSearch
	for _, m := range searchTerms {
		if only != "" && m.FoodName != only {
			continue
		}
		if _, ok := byName[m.FoodName]; !ok {
			log.Printf("WARNING: mapping references %q, which is not in the database — skipping", m.FoodName)
			continue
		}
		work = append(work, m)
	}
	if len(work) == 0 {
		return fmt.Errorf("nothing to ingest (check -only spelling, or run `make seed` first)")
	}

	log.Printf("ingesting %d foods from location %s (dry-run=%v, %d workers)",
		len(work), locationID, dryRun, workers)

	// ---- THE WORKER POOL ----
	//
	// errgroup is sync.WaitGroup plus two things I'd otherwise write by hand:
	// it collects the FIRST error any goroutine returns, and its context is
	// cancelled the moment one fails — so a bad token doesn't leave 41 more
	// requests in flight.
	//
	// The SEMAPHORE is the bounded part. errgroup on its own would launch all
	// 42 goroutines at once; a buffered channel used as a counting semaphore
	// caps how many run concurrently. Acquiring is a send (blocks when full),
	// releasing is a receive. It's the idiomatic Go way to bound concurrency
	// without a third-party pool library.
	g, gctx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, workers)

	results := make([]foodResult, len(work))

	for i, m := range work {
		// In Go 1.22+ loop variables are per-iteration, so capturing them
		// directly is safe now. Before that this needed `i, m := i, m` — one of
		// the language's most famous footguns, and worth remembering when
		// reading older code.
		g.Go(func() error {
			select {
			case sem <- struct{}{}: // acquire a slot
			case <-gctx.Done():
				return gctx.Err()
			}
			defer func() { <-sem }() // release it

			food := byName[m.FoodName]
			res := ingestOneFood(gctx, client, locationID, m, food)
			// Each goroutine writes to its OWN index. No mutex needed: distinct
			// slice elements are independent memory, and the slice header never
			// changes.
			results[i] = res

			// Returning the error would cancel the whole group. I deliberately
			// do NOT do that for a per-food failure: one bad mapping shouldn't
			// abort the other 41. Only a context cancellation propagates.
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return fmt.Errorf("ingestion cancelled: %w", err)
	}

	return writeResults(ctx, st, locationID, results, dryRun)
}

// ingestOneFood searches, filters, and converts. No database access — that
// happens after the pool finishes, on one goroutine, which keeps the write path
// simple and transactional.
func ingestOneFood(ctx context.Context, client *kroger.Client, locationID string,
	m foodSearch, food store.Food) foodResult {

	out := foodResult{FoodName: m.FoodName}

	products, err := client.SearchProducts(ctx, m.Term, locationID, 25)
	if err != nil {
		out.Err = err
		return out
	}

	for _, p := range products {
		if len(out.Products) >= maxProductsPerFood {
			break
		}

		// THE MATCH RULE: every word of my search term must appear in the
		// product name. Deliberately strict — "chicken breast" will not match
		// "breaded chicken nuggets", at the cost of also missing some products
		// that were genuinely fine. I'd much rather under-ingest than quietly
		// attach the wrong nutrition to a product.
		if !matchesSearchTerm(p.Description, m.Term) {
			continue
		}

		// The NEGATIVE filter, added after my first dry run ingested baby-food
		// pouches as spinach and cooking spray as canola oil.
		if bad := excludedBy(p.Description); bad != "" {
			out.Skipped = append(out.Skipped,
				fmt.Sprintf("%s: excluded by %q", p.Description, bad))
			continue
		}

		// Kroger nests size and price under items[]. Take the first item that
		// has both.
		item, ok := firstUsableItem(p)
		if !ok {
			out.Skipped = append(out.Skipped,
				fmt.Sprintf("%s: no item with a size and price", p.Description))
			continue
		}

		// For foods sold by volume, a bare "oz" is almost certainly FLUID
		// ounces mislabeled, and reading it as mass would be quietly ~4% wrong.
		// Rejecting is the honest choice.
		if liquidFoods[m.FoodName] && isBareOunces(item.Size) {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s (%s): bare \"oz\" on a liquid food is ambiguous (fluid vs weight), so I won't guess",
				p.Description, item.Size))
			continue
		}

		grams, err := kroger.NetWeightGrams(item.Size, m.GramsPerItem)
		if err != nil {
			// SKIP AND LOG, NEVER GUESS. This is where oils and milk drop out,
			// and the log line says exactly why.
			out.Skipped = append(out.Skipped,
				fmt.Sprintf("%s (%s): %v", p.Description, item.Size, err))
			continue
		}

		size, _ := kroger.ParseSize(item.Size)

		out.Products = append(out.Products, store.IngestProduct{
			FoodID:          food.ID,
			StoreID:         locationID,
			ExternalID:      p.ProductID,
			Name:            p.Description,
			Brand:           p.Brand,
			PackSizeQty:     size.Qty,
			PackSizeUnit:    normalizeUnitForSchema(size.Unit),
			NetWeightG:      grams,
			PriceCents:      kroger.DollarsToCents(item.Price.Regular),
			PromoPriceCents: kroger.DollarsToCents(item.Price.Promo),
			Available:       item.Inventory.StockLevel != "TEMPORARILY_OUT_OF_STOCK",
		})
	}

	return out
}

// matchesSearchTerm requires every word of the term to appear in the name.
func matchesSearchTerm(productName, term string) bool {
	name := strings.ToLower(productName)
	for _, word := range strings.Fields(strings.ToLower(term)) {
		// Skip very short words ("a", "2") — they match everything and carry no
		// signal, so requiring them only adds false negatives.
		if len(word) <= 2 {
			continue
		}
		if !strings.Contains(name, word) {
			return false
		}
	}
	return true
}

// excludedBy reports which exclude word disqualified a product, or "" if none
// did. Returning the WORD rather than a bool makes the skip log say why, which
// is what lets me tune the list instead of guessing at it.
//
// WORD BOUNDARIES, NOT SUBSTRINGS — and I learned this the expensive way. My
// first version used strings.Contains, so the exclude word "bar" (meant to
// catch protein bars) also matched "BARilla" and threw out every box of pasta.
// "pet" would have eaten "petite", "dog" would have eaten anything with "dog"
// buried in it.
//
// So: single-word excludes are matched against the product's WORDS, and only
// multi-word phrases fall back to a substring check (a phrase like "cooking
// spray" can't accidentally hide inside another word).
//
// The general lesson is one I keep re-learning in this project: a filter that's
// too loose ingests garbage, and a filter that's too tight silently drops good
// data. Both fail quietly. The only reason I caught either is that dry-run
// prints its reasoning.
func excludedBy(productName string) string {
	lower := strings.ToLower(productName)

	// Split on anything that isn't a letter or digit, so "Butter, Spreadable"
	// and "Non-Stick" tokenize the way a reader would expect.
	words := strings.FieldsFunc(lower, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	})
	wordSet := make(map[string]bool, len(words))
	for _, w := range words {
		wordSet[w] = true
	}

	for _, bad := range excludeWords {
		if strings.Contains(bad, " ") {
			// Multi-word phrase: substring is safe and is what I want.
			if strings.Contains(lower, bad) {
				return bad
			}
			continue
		}
		if wordSet[bad] {
			return bad
		}
	}
	return ""
}

// isBareOunces reports whether a size uses "oz" with no "fl"/"fluid" qualifier.
// Kroger writes fluid ounces both ways, and the unqualified spelling is the
// ambiguous one.
func isBareOunces(size string) bool {
	s := strings.ToLower(size)
	if !strings.Contains(s, "oz") {
		return false
	}
	// Explicitly fluid is NOT ambiguous — my parser already rejects it as a
	// volume, with a clearer message than this check would give.
	if strings.Contains(s, "fl") || strings.Contains(s, "fluid") {
		return false
	}
	return true
}

func firstUsableItem(p kroger.Product) (kroger.ProductItem, bool) {
	for _, it := range p.Items {
		if it.Size != "" && it.Price.Regular > 0 {
			return it, true
		}
	}
	return kroger.ProductItem{}, false
}

// normalizeUnitForSchema maps my parser's units onto the CHECK constraint in
// migration 000002. Anything unexpected becomes "each", which is the schema's
// least-wrong catch-all — and it only affects the display column, never
// net_weight_g, which is the one the solver reads.
func normalizeUnitForSchema(u string) string {
	switch u {
	case "g", "kg", "oz", "lb", "ml", "l", "fl_oz", "each", "dozen":
		return u
	case "ct":
		return "each"
	default:
		return "each"
	}
}

func writeResults(ctx context.Context, st *store.Store, locationID string,
	results []foodResult, dryRun bool) error {

	var (
		totalFound, totalSkipped, inserted, updated, priceChanges int
		seenIDs                                                   []string
		failures                                                  []string
	)
	// No mutex here, deliberately: g.Wait() has already returned, so the worker
	// pool is finished and this loop is the only thing touching these. Doing the
	// concurrent part (slow network fetches) and the serial part (database
	// writes) in separate phases is what keeps the write path lock-free.
	//
	// I originally left a `seenMu sync.Mutex` here from an earlier draft and
	// `go vet` caught it: assigning a struct containing a mutex COPIES the lock,
	// which silently breaks mutual exclusion. Good reminder that vet finds real
	// concurrency bugs, not just style nits.

	for _, r := range results {
		if r.Err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", r.FoodName, r.Err))
			continue
		}

		for _, s := range r.Skipped {
			log.Printf("  skip %s", s)
		}
		totalSkipped += len(r.Skipped)

		if len(r.Products) == 0 {
			log.Printf("  NO MATCH for %q — the search term may need adjusting", r.FoodName)
			continue
		}

		for _, p := range r.Products {
			totalFound++
			seenIDs = append(seenIDs, p.ExternalID)

			if dryRun {
				promo := ""
				if p.PromoPriceCents > 0 {
					promo = fmt.Sprintf(" (promo %d)", p.PromoPriceCents)
				}
				fmt.Printf("  %-45s %8.1fg %6d cents%s\n",
					truncate(p.Name, 45), p.NetWeightG, p.PriceCents, promo)
				continue
			}

			res, err := st.UpsertProduct(ctx, p)
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", p.Name, err))
				continue
			}
			if res.Inserted {
				inserted++
			}
			if res.Updated {
				updated++
			}
			if res.PriceChanged {
				priceChanges++
				log.Printf("  price change: %s %d -> %d cents",
					truncate(p.Name, 40), res.OldPriceCents, res.NewPriceCents)
			}
		}
	}

	fmt.Printf("\n%d products matched, %d skipped\n", totalFound, totalSkipped)

	if dryRun {
		fmt.Println("(dry run: nothing written)")
		return nil
	}

	fmt.Printf("%d inserted, %d updated, %d price changes\n", inserted, updated, priceChanges)

	// Only sweep for vanished SKUs when the run actually succeeded broadly.
	// Marking everything unavailable because the API had a bad minute would be
	// a self-inflicted outage — the store layer refuses an empty list, and this
	// is the second guard.
	if len(seenIDs) > 0 && len(failures) == 0 {
		sort.Strings(seenIDs)
		n, err := st.MarkMissingUnavailable(ctx, locationID, seenIDs)
		if err != nil {
			log.Printf("warning: %v", err)
		} else if n > 0 {
			fmt.Printf("%d previously-seen products are no longer listed (marked unavailable)\n", n)
		}
	} else if len(failures) > 0 {
		fmt.Println("skipping the vanished-SKU sweep because this run had failures")
	}

	if len(failures) > 0 {
		fmt.Printf("\n%d failures:\n", len(failures))
		for _, f := range failures {
			fmt.Printf("  %s\n", f)
		}
		return fmt.Errorf("%d food(s) failed", len(failures))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
