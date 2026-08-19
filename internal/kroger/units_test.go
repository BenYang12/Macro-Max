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

// Volume conversion, added once I had real densities. The whole reason this is
// legitimate is that a LOOKED-UP density is data, not a guess — so these tests
// pin the arithmetic against values I can check by hand.
func TestVolumeGrams(t *testing.T) {
	const (
		canola   = 0.920
		oliveOil = 0.915
		milk     = 1.032
	)

	tests := []struct {
		size    string
		density float64
		want    float64
	}{
		// 1 fl oz = 29.5735 ml. 32 fl oz canola = 946.35ml * 0.92 = 870.6g.
		{"32 fl oz", canola, 870.65},
		{"32 oz", canola, 870.65},  // bare oz on a liquid = fluid ounces
		{"40 oz", canola, 1088.31}, // the Crisco bottle I skipped before
		{"25.4 fl oz", oliveOil, 687.42},
		// 1 gallon = 3785.41ml. Milk at 1.032 = 3906.5g.
		{"1 gal", milk, 3906.55},
		{"0.5 gal", milk, 1953.27},
		{"1/2 gal", milk, 1953.27}, // the fraction spelling Kroger also uses
		{"59 fl oz", milk, 1800.60},
		{"500 ml", milk, 516.0},
		{"2 l", milk, 2064.0},
	}

	for _, tc := range tests {
		t.Run(tc.size, func(t *testing.T) {
			got, err := VolumeGrams(tc.size, tc.density)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if math.Abs(got-tc.want) > 0.5 {
				t.Errorf("got %.2f grams; want %.2f", got, tc.want)
			}
		})
	}
}

// No density means no conversion — there is deliberately no default, because a
// wrong default density is exactly the silent error this whole design avoids.
func TestVolumeGrams_RequiresADensity(t *testing.T) {
	if _, err := VolumeGrams("1 gal", 0); err == nil {
		t.Fatal("expected an error with no density supplied")
	}
	if _, err := VolumeGrams("16 oz", -1); err == nil {
		t.Fatal("expected an error with a negative density")
	}
}

// A size with no volume unit at all must fail, so the caller can fall back to
// the mass reading (some "liquid" foods are genuinely sold by weight).
func TestVolumeGrams_NoVolumeUnitIsAnError(t *testing.T) {
	if _, err := VolumeGrams("2 lb", 0.92); err == nil {
		t.Fatal("expected an error: 'lb' is a mass unit, not a volume")
	}
}

// The fraction spellings must survive normalization. "1/2 gal" used to parse
// as "2 gal" once the slash became a separator — four times too much milk.
func TestParseSize_FractionsAreHandled(t *testing.T) {
	g, err := VolumeGrams("1/2 gal", 1.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if math.Abs(g-1892.7) > 1 {
		t.Errorf("got %.1f g; want ~1892.7 (half a gallon of water)", g)
	}
}

// AsFluidOunces had no coverage at all, despite deciding the pack_size_qty and
// pack_size_unit written for every liquid product in the catalog.
func TestAsFluidOunces(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      float64
		ok        bool
	}{
		{"fluid ounces pass through", "16 fl oz", 16, true},
		{"spelled-out fluid ounce", "12 fluid ounce", 12, true},
		{"a litre is 33.8 fl oz", "1 l", 33.814, true},
		{"a quart is exactly 32 fl oz", "1 qt", 32, true},
		{"millilitres convert", "500 ml", 16.907, true},
		// The context rule: for a food already known to be a liquid, a bare
		// "oz" on the label means FLUID ounces.
		{"bare oz is fluid in a liquid context", "32 oz", 32, true},
		// A pure mass string has no volume unit to find, so the caller keeps
		// its mass reading rather than getting a silently wrong number.
		{"grams are not fluid ounces", "500 g", 0, false},
		{"a count is not fluid ounces", "12 ct", 0, false},
		{"garbage", "family size", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := AsFluidOunces(tc.raw)
			if ok != tc.ok {
				t.Fatalf("AsFluidOunces(%q) ok = %v; want %v", tc.raw, ok, tc.ok)
			}
			if ok && math.Abs(got-tc.want) > 0.01 {
				t.Errorf("AsFluidOunces(%q) = %v; want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// The "oz" ambiguity, pinned from both sides. The SAME string must yield a
// mass reading through the general path and a fluid reading through the
// liquid-only path — because the FOOD decides which, never the string. If
// these two ever agree, one of the two tables has been "helpfully" corrected
// and a whole category of products silently changed weight.
func TestOunceIsMassByDefaultAndFluidOnlyForKnownLiquids(t *testing.T) {
	const label = "32 oz"

	mass, err := NetWeightGrams(label, 0)
	if err != nil {
		t.Fatalf("NetWeightGrams(%q): %v", label, err)
	}
	if math.Abs(mass-907.18) > 0.01 {
		t.Errorf("mass reading = %v g; want 907.18 g (32 avoirdupois ounces)", mass)
	}

	// Water, so grams and millilitres coincide and the number is checkable.
	fluid, err := VolumeGrams(label, 1.0)
	if err != nil {
		t.Fatalf("VolumeGrams(%q): %v", label, err)
	}
	if math.Abs(fluid-946.35) > 0.01 {
		t.Errorf("fluid reading = %v g; want 946.35 g (32 US fluid ounces of water)", fluid)
	}

	if math.Abs(mass-fluid) < 1 {
		t.Fatal("mass and fluid readings collapsed; the context distinction is gone")
	}
}
