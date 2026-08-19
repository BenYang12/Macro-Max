package solver

import "testing"

// SolveKey had no direct coverage, which is how a redundant ingredient
// survived in it. These tests pin the property the whole content-addressed
// design rests on: the key must move when — and only when — something that
// could change the ANSWER moves.
func TestSolveKey(t *testing.T) {
	keyFor := func(t *testing.T, in SolveInput) string {
		t.Helper()
		req, err := BuildRequest(in)
		if err != nil {
			t.Fatalf("BuildRequest: %v", err)
		}
		key, err := SolveKey(req)
		if err != nil {
			t.Fatalf("SolveKey: %v", err)
		}
		return key
	}

	base := keyFor(t, fixture())

	t.Run("is stable for identical input", func(t *testing.T) {
		if again := keyFor(t, fixture()); again != base {
			t.Errorf("same input produced two keys:\n %s\n %s", base, again)
		}
	})

	// This is the case that made a separate prices fingerprint unnecessary.
	// A price move reaches the request through BuildRequest, so it reaches the
	// key through the marshaled blob alone.
	t.Run("changes when a product price changes", func(t *testing.T) {
		in := fixture()
		in.Products[0].EffectivePriceCents = 1099
		if got := keyFor(t, in); got == base {
			t.Error("a price change must produce a different key")
		}
	})

	t.Run("changes when a macro target changes", func(t *testing.T) {
		in := fixture()
		in.Target.ProteinGDaily = 181
		if got := keyFor(t, in); got == base {
			t.Error("a target change must produce a different key")
		}
	})

	t.Run("changes when the budget changes", func(t *testing.T) {
		in := fixture()
		in.Target.BudgetCentsWeekly = 8000
		if got := keyFor(t, in); got == base {
			t.Error("a budget change must produce a different key")
		}
	})

	t.Run("is namespaced", func(t *testing.T) {
		if len(base) < len("solve:") || base[:len("solve:")] != "solve:" {
			t.Errorf("key %q should carry the solve: prefix", base)
		}
	})
}
