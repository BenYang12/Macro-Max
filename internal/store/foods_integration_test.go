package store

// foods_integration_test.go — tests that run the REAL SQL against a REAL
// Postgres. Contrast with a unit test (no I/O, microseconds). These need a
// database, so they SELF-SKIP unless TEST_DATABASE_URL is set. `make test`
// on a bare laptop skips them; `make test-int` (and CI) set the var and run
// them.
//
// Naming note: the file ends in _test.go so `go test` compiles it only during
// testing. The "_integration" part is just a human label — Go attaches no
// meaning to it.

import (
	"context"
	"errors"
	"os"
	"testing"
)

// newTestStore is a HELPER shared by the tests below. It reads
// TEST_DATABASE_URL and either skips the test or returns a live *Store.
//
// t.Helper() marks this as a helper: when it calls t.Fatal, the failure is
// reported at the CALLER's line, not inside here — so failures point at the
// test that actually broke.
func newTestStore(t *testing.T) *Store {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		// t.Skip stops THIS test and marks it skipped (not failed). This is
		// what lets the same test suite be safe to run anywhere.
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}

	// A test needs a context too. context.Background() is the empty root
	// context — fine for a test, which has no signals to propagate.
	st, err := NewPool(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connecting to test database: %v", err)
	}

	// t.Cleanup registers a function to run when the test finishes (pass or
	// fail) — the test-scoped version of defer. Closes the pool so tests
	// don't leak connections into each other.
	t.Cleanup(st.Close)

	return st
}

// insertTestFood creates one food for a test and deletes it afterward.
//
// WHY THIS EXISTS — a lesson learned the hard way: this test used to just
// query for whatever proteins happened to be in the database, assuming
// `make seed` had been run. That passed on my laptop and FAILED IN CI, which
// migrates a fresh database but never seeds it.
//
// The rule it violated: a test must create every precondition it depends on.
// Relying on ambient database state makes a test pass or fail based on what
// someone did to the database an hour ago — the definition of a flaky test.
// The products tests got this right from the start; this one now matches.
func insertTestFood(t *testing.T, st *Store, name, category string) int64 {
	t.Helper()
	ctx := context.Background()

	var id int64
	err := st.Pool.QueryRow(ctx, `
		INSERT INTO foods (name, category, tags, kcal_per_100g,
		                   protein_g_per_100g, carbs_g_per_100g, fat_g_per_100g)
		VALUES ($1, $2, '{gluten_free}', 120, 22.5, 0, 2.6)
		RETURNING id`, name, category).Scan(&id)
	if err != nil {
		t.Fatalf("inserting test food %q: %v", name, err)
	}

	t.Cleanup(func() {
		st.Pool.Exec(context.Background(), `DELETE FROM foods WHERE id = $1`, id)
	})

	return id
}

// TestListFoods_FiltersByCategory proves the category WHERE clause works.
// It inserts one protein and one carb, so it can assert that the filter both
// INCLUDES the right row and EXCLUDES the wrong one — something a query
// against ambient data can't test reliably.
func TestListFoods_FiltersByCategory(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	insertTestFood(t, st, "__test_protein__", "protein")
	insertTestFood(t, st, "__test_carb__", "carb")

	foods, err := st.ListFoods(ctx, FoodFilter{Category: "protein"})
	if err != nil {
		t.Fatalf("ListFoods returned an error: %v", err)
	}

	// Every row the filter returned must actually BE a protein — this is what
	// proves the WHERE clause works, not just that rows came back. Note this
	// holds whether or not the database is also seeded, so the test is correct
	// in both environments.
	found := false
	for _, f := range foods {
		if f.Category != "protein" {
			t.Errorf("category filter leaked a non-protein food: %q is category %q", f.Name, f.Category)
		}
		if f.Name == "__test_protein__" {
			found = true
		}
		if f.Name == "__test_carb__" {
			t.Error("category filter returned the carb food")
		}
	}
	if !found {
		t.Error("the inserted protein food was not returned by the filter")
	}
}

// TestListFoods_FiltersByTag exercises the TEXT[] containment operator and the
// GIN index behind it. Same self-sufficiency rule: insert, then query.
func TestListFoods_FiltersByTag(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	insertTestFood(t, st, "__test_tagged__", "protein") // inserted with {gluten_free}

	foods, err := st.ListFoods(ctx, FoodFilter{Tag: "gluten_free"})
	if err != nil {
		t.Fatalf("ListFoods returned an error: %v", err)
	}

	for _, f := range foods {
		if f.Name == "__test_tagged__" {
			return // found it; the @> containment filter works
		}
	}
	t.Error("tag filter did not return the food tagged gluten_free")
}

// An empty result must come back as an empty slice, never nil — otherwise the
// API would serialize {"foods": null} and break clients that call .map().
func TestListFoods_NoMatchesReturnsEmptySlice(t *testing.T) {
	st := newTestStore(t)

	foods, err := st.ListFoods(context.Background(), FoodFilter{Category: "__nonexistent__"})
	if err != nil {
		t.Fatalf("ListFoods returned an error: %v", err)
	}
	if foods == nil {
		t.Error("got a nil slice; want an empty one")
	}
	if len(foods) != 0 {
		t.Errorf("got %d foods; want 0", len(foods))
	}
}

// TestGetFood_UnknownIDIsNotFound checks the sentinel-error translation:
// a missing row must surface as OUR ErrNotFound, not pgx.ErrNoRows and not a
// generic error. The handler's 404 depends on exactly this.
func TestGetFood_UnknownIDIsNotFound(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// IDENTITY PKs start at 1 and climb, so a huge id is guaranteed absent.
	_, err := st.GetFood(ctx, 999_999_999)

	// errors.Is walks the wrap chain, so this holds even if GetFood wrapped
	// the sentinel with %w somewhere.
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for a missing id; got %v", err)
	}
}
