package kroger

// The exhaustive table my plan calls for. This is the highest-value test file
// in Phase 5: every entry here is a real shape Kroger sends, and every one of
// them is a chance to silently corrupt a net weight.
//
// I'm testing the failure cases as carefully as the success cases, because the
// whole design rests on "skip and log, never guess" — and a parser that
// accidentally succeeds on garbage breaks that promise without any symptom.

import (
	"math"
	"strings"
	"testing"
)

func TestNetWeightGrams_Mass(t *testing.T) {
	tests := []struct {
		size string
		want float64
	}{
		// The common shapes.
		{"16 oz", 453.59237},
		{"1 lb", 453.59237},
		{"2.5 lb", 1133.980925},
		{"5 lb", 2267.96185},
		{"12 oz", 340.1942775},
		{"500 g", 500},
		{"1 kg", 1000},

		// Whitespace and casing vary run to run.
		{"16OZ", 453.59237},
		{"16oz", 453.59237},
		{"  2 LB  ", 907.18474},
		{"2.5lb", 1133.980925},

		// Spelled-out units.
		{"1 pound", 453.59237},
		{"3 lbs", 1360.77711},
		{"250 grams", 250},

		// Hedged weights, which Kroger uses for anything sold by the piece.
		{"approx 1 lb", 453.59237},
		{"about 2 lb", 907.18474},
		{"~1.5 lb", 680.388555},

		// COMPOUND sizes: the mass half is the truth, and it appears second.
		// If my preference rule were "first match wins" these would all be
		// wrong, which is exactly why the rule is stated explicitly.
		{"6 ct / 12 oz", 340.1942775},
		{"12 ct / 1 lb", 453.59237},
		{"4 pk / 8 oz", 226.796185},

		// Leading decimal with no zero.
		{".5 lb", 226.796185},
	}

	for _, tc := range tests {
		t.Run(tc.size, func(t *testing.T) {
			got, err := NetWeightGrams(tc.size, 0)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Tolerance, not equality: these are floats produced by
			// multiplication, and 2.5 * 453.59237 is not exactly representable.
			if math.Abs(got-tc.want) > 0.001 {
				t.Errorf("got %v grams; want %v", got, tc.want)
			}
		})
	}
}

func TestNetWeightGrams_CountNeedsATableEntry(t *testing.T) {
	// Without a grams-per-item figure, a count size is an ERROR. This is the
	// never-guess rule: I would rather lose the product than invent a weight.
	if _, err := NetWeightGrams("12 ct", 0); err == nil {
		t.Fatal("expected an error for a count size with no table entry")
	} else if !strings.Contains(err.Error(), "grams-per-item") {
		t.Errorf("error %q should explain the missing table entry", err)
	}

	// WITH an entry, it converts: 12 eggs at 50g each.
	got, err := NetWeightGrams("12 ct", 50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 600 {
		t.Errorf("got %v; want 600", got)
	}
}

func TestNetWeightGrams_DozenIsTwelve(t *testing.T) {
	got, err := NetWeightGrams("1 dozen", 50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 600 {
		t.Errorf("got %v; want 600 (a dozen is 12 x 50g)", got)
	}
}

func TestNetWeightGrams_VolumeIsRejectedWithAUsefulMessage(t *testing.T) {
	// Every one of these is a real Kroger size, and every one is unusable
	// without a per-food density. Rejecting them is correct; rejecting them
	// with a message that says WHY is what makes the log readable.
	for _, size := range []string{
		"1 gal", "59 fl oz", "16.9 fl oz", "2 l", "500 ml", "1 qt", "1 pt",
	} {
		t.Run(size, func(t *testing.T) {
			_, err := NetWeightGrams(size, 0)
			if err == nil {
				t.Fatalf("volume size %q should not have parsed to grams", size)
			}
			if !strings.Contains(err.Error(), "density") &&
				!strings.Contains(err.Error(), "not convertible") {
				t.Errorf("error %q should explain the volume problem", err)
			}
		})
	}
}

func TestNetWeightGrams_GarbageIsRejected(t *testing.T) {
	for _, size := range []string{
		"",            // missing entirely
		"   ",         // whitespace only
		"each",        // a unit with no number
		"family size", // marketing copy
		"0 oz",        // a zero weight is not a weight
		"large",       // no number, no unit
		"assorted",    //
	} {
		t.Run("garbage_"+size, func(t *testing.T) {
			if _, err := NetWeightGrams(size, 100); err == nil {
				t.Errorf("size %q should have been rejected", size)
			}
		})
	}
}

// A direct test of the preference rule, separate from the table above, because
// it's the one piece of real logic in the parser rather than a lookup.
func TestParseSize_MassBeatsCount(t *testing.T) {
	s, err := ParseSize("6 ct / 12 oz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Kind != KindMass {
		t.Errorf("Kind = %v; want KindMass — the mass half should win", s.Kind)
	}
	if s.Qty != 12 || s.Unit != "oz" {
		t.Errorf("got %v %s; want 12 oz", s.Qty, s.Unit)
	}
}

// Count beats volume, so a "12 ct" that also mentions fluid ounces still has a
// path to grams via the table.
func TestParseSize_CountBeatsVolume(t *testing.T) {
	s, err := ParseSize("12 ct / 8 fl oz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Kind != KindCount {
		t.Errorf("Kind = %v; want KindCount", s.Kind)
	}
}

// Unit normalization: "lbs", "pound", and "lb" must all end up identical, so
// downstream code never has to know Kroger's spelling that day.
func TestParseSize_NormalizesUnitSpelling(t *testing.T) {
	for _, size := range []string{"2 lb", "2 lbs", "2 pound"} {
		s, err := ParseSize(size)
		if err != nil {
			t.Fatalf("%q: %v", size, err)
		}
		if s.Unit != "lb" {
			t.Errorf("%q normalized to %q; want lb", size, s.Unit)
		}
	}
}
