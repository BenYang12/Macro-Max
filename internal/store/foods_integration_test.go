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

// TestListFoods_ReturnsSeededProteins assumes the DB has been migrated and
// seeded (Step 8 adds the seeder; until then run `make seed` or insert a row
// by hand). It asserts on SHAPE, not exact rows, so it won't break every time
// the seed data changes.
func TestListFoods_ReturnsSeededProteins(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	foods, err := st.ListFoods(ctx, FoodFilter{Category: "protein"})
	if err != nil {
		t.Fatalf("ListFoods returned an error: %v", err)
	}

	if len(foods) == 0 {
		t.Fatal("expected at least one protein food; got none (is the DB seeded?)")
	}

	// Every row the category filter returned must actually BE a protein —
	// this is what proves the WHERE clause works, not just that rows came back.
	for _, f := range foods {
		if f.Category != "protein" {
			t.Errorf("category filter leaked a non-protein food: %q is category %q", f.Name, f.Category)
		}
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
