package kroger

// units.go — turning Kroger's size strings into grams.
//
// THIS FILE IS THE QUARANTINE. My plan lists unit-string chaos as risk #2, and
// the mitigation is that ALL of it lives in one pure function with an
// exhaustive test table. Every other part of the system reads
// products.net_weight_g and never thinks about units again.
//
// The rule I am not going to break: UNPARSEABLE PRODUCTS ARE SKIPPED AND
// LOGGED, NEVER GUESSED. A wrong net weight is invisible — it produces a
// basket that looks completely normal and is quietly wrong about how much food
// I'm buying. A skipped product produces a log line I can read. One of those
// failure modes I can debug and the other I cannot.
//
// Real examples of what Kroger actually sends, which is why this is harder
// than it sounds:
//
//	"16 oz"            straightforward mass
//	"2.5 lb"           decimal mass
//	"1 gal"            volume: needs density, which I don't have
//	"12 ct"            count: needs grams-per-item, which is food-specific
//	"6 ct / 12 oz"     BOTH: the mass part is the truth
//	"approx 1 lb"      hedged mass
//	"1 dozen"          count, spelled out

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Kind separates the three cases that need genuinely different handling.
type Kind int

const (
	// KindMass converts to grams with a fixed factor. The easy case.
	KindMass Kind = iota

	// KindVolume cannot convert without density, and density varies per food
	// (oil 0.92 g/ml, honey 1.42). I reject these — the same decision I made
	// for FDC serving sizes in Phase 2, for the same reason.
	KindVolume

	// KindCount needs a grams-per-item figure that depends on the food ("1 egg
	// = 50g"). The parser extracts the count; the CALLER supplies the weight,
	// because only the caller knows which food this is.
	KindCount
)

// Size is a parsed size string.
type Size struct {
	Qty  float64
	Unit string // normalized lowercase: "g" "kg" "oz" "lb" "fl_oz" "ml" "l" "ct"
	Kind Kind
}

// gramsPerMassUnit holds the exact conversion factors. These are definitions,
// not approximations: the international pound is defined as exactly
// 0.45359237 kg, and an ounce is exactly 1/16 of that.
var gramsPerMassUnit = map[string]float64{
	"g":     1,
	"gram":  1,
	"grams": 1,
	"kg":    1000,
	"mg":    0.001,
	"oz":    28.349523125,
	"ounce": 28.349523125,
	"lb":    453.59237,
	"lbs":   453.59237,
	"pound": 453.59237,
}

// volumeUnits are recognized so I can REJECT them with a specific message
// rather than a generic "unparseable". Knowing that a product failed because
// it's sold by volume is much more actionable than knowing it just failed.
var volumeUnits = map[string]bool{
	"fl": true, "floz": true, "fl_oz": true, "ml": true, "l": true,
	"liter": true, "litre": true, "pt": true, "pint": true,
	"qt": true, "quart": true, "gal": true, "gallon": true,
}

// countUnits are the "sold by the item" markers.
var countUnits = map[string]bool{
	"ct": true, "count": true, "each": true, "ea": true,
	"pk": true, "pack": true, "dozen": true, "doz": true,
}

// sizePattern pulls a number followed by a unit word out of a string.
//
// Reading the regex, since I'll forget:
//
//	([0-9]*\.?[0-9]+)   a number, optionally decimal ("2", "2.5", ".5")
//	\s*                 optional whitespace ("16oz" and "16 oz" both appear)
//	([a-z_]+)           the unit word
//
// I deliberately do NOT try to write one regex that handles every case. The
// regex finds CANDIDATES; the Go code below decides what they mean. Regexes are
// terrible at "prefer the mass part of a compound size", and Go is fine at it.
var sizePattern = regexp.MustCompile(`([0-9]*\.?[0-9]+)\s*([a-z_]+)`)

// ParseSize extracts the most useful size from a raw Kroger size string.
//
// When a string contains BOTH a count and a mass ("6 ct / 12 oz"), mass wins.
// Mass is the thing I actually need and the thing I can convert exactly, so
// preferring it turns a would-be skip into a usable product.
func ParseSize(raw string) (Size, error) {
	s := normalizeSizeString(raw)
	if s == "" {
		return Size{}, fmt.Errorf("empty size string")
	}

	matches := sizePattern.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		return Size{}, fmt.Errorf("no number+unit found in %q", raw)
	}

	// Collect every candidate, then choose. Two passes rather than one, so the
	// preference rule ("mass beats count") is stated in one obvious place
	// instead of being smeared through the loop.
	var mass, count, volume *Size

	for _, m := range matches {
		qty, err := strconv.ParseFloat(m[1], 64)
		if err != nil || qty <= 0 {
			continue
		}
		unit := m[2]

		switch {
		case gramsPerMassUnit[unit] > 0:
			if mass == nil {
				mass = &Size{Qty: qty, Unit: canonicalMassUnit(unit), Kind: KindMass}
			}
		case countUnits[unit]:
			if count == nil {
				// A dozen is twelve, and saying so here means the caller only
				// ever deals with a plain item count.
				if unit == "dozen" || unit == "doz" {
					qty *= 12
				}
				count = &Size{Qty: qty, Unit: "ct", Kind: KindCount}
			}
		case volumeUnits[unit]:
			if volume == nil {
				volume = &Size{Qty: qty, Unit: unit, Kind: KindVolume}
			}
		}
	}

	// The preference order, stated once.
	if mass != nil {
		return *mass, nil
	}
	if count != nil {
		return *count, nil
	}
	if volume != nil {
		// Recognized but unusable. The specific message is the point.
		return *volume, fmt.Errorf(
			"size %q is a volume (%g %s); converting to grams needs a density I don't have",
			raw, volume.Qty, volume.Unit)
	}

	return Size{}, fmt.Errorf("no recognizable unit in %q", raw)
}

// normalizeSizeString lowercases, collapses the many spellings of fluid
// ounces, and turns compound-size punctuation into spaces. Shared by ParseSize
// and VolumeGrams so the two can never disagree about what a string says.
func normalizeSizeString(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "fluid ounce", "floz")
	s = strings.ReplaceAll(s, "fl oz", "floz")
	s = strings.ReplaceAll(s, "fl. oz", "floz")
	s = strings.ReplaceAll(s, "fl_oz", "floz")
	// "1/2 gal" must not become "1 2 gal" — handle the fraction before "/"
	// becomes a separator.
	s = strings.ReplaceAll(s, "1/2", "0.5")
	s = strings.ReplaceAll(s, "1/4", "0.25")
	s = strings.ReplaceAll(s, "3/4", "0.75")
	return strings.NewReplacer("/", " ", ",", " ", "approx", " ", "about", " ", "~", " ").Replace(s)
}

func canonicalMassUnit(u string) string {
	switch u {
	case "gram", "grams":
		return "g"
	case "ounce":
		return "oz"
	case "lbs", "pound":
		return "lb"
	default:
		return u
	}
}

// mlPerVolumeUnit converts the volume units Kroger uses into millilitres.
// These are exact US customary definitions, not estimates.
var mlPerVolumeUnit = map[string]float64{
	"floz": 29.5735295625,

	// Bare "oz" counts as FLUID ounces here, and this line is the entire point
	// of the density table.
	//
	// It is safe ONLY because VolumeGrams is unreachable without a density,
	// and a density exists only for foods I explicitly listed as liquids. So
	// "32 oz" means 907g of cheese and 946ml of milk, and the FOOD decides
	// which — never the string alone. Having "oz" in both the mass table and
	// this one isn't a contradiction; it's the same ambiguity resolved by
	// context, which is the only thing that can resolve it.
	"oz":    29.5735295625,
	"ounce": 29.5735295625,

	"ml":     1,
	"l":      1000,
	"liter":  1000,
	"litre":  1000,
	"pt":     473.176473,
	"pint":   473.176473,
	"qt":     946.352946,
	"quart":  946.352946,
	"gal":    3785.411784,
	"gallon": 3785.411784,
}

// VolumeGrams converts a volume size to grams GIVEN a density in g/ml.
//
// I resisted this for a while, because "converting volume to mass needs a
// density I don't have" was the honest position and it kept me from guessing.
// What changed is that I now HAVE densities — for the four specific foods that
// matter — and a measured density is not a guess.
//
// The distinction I'm drawing: refusing to convert ANY volume is safe but
// costs me every oil, every milk, and every liquid egg white. Converting with
// a per-food density I've looked up is a bounded, documented assumption. What
// I still won't do is apply a DEFAULT density to an unknown food, which is
// why this takes the density as a required argument and has no fallback.
func VolumeGrams(rawSize string, gramsPerML float64) (float64, error) {
	if gramsPerML <= 0 {
		return 0, fmt.Errorf("no density supplied for %q", rawSize)
	}

	s := normalizeSizeString(rawSize)
	matches := sizePattern.FindAllStringSubmatch(s, -1)
	for _, m := range matches {
		qty, err := strconv.ParseFloat(m[1], 64)
		if err != nil || qty <= 0 {
			continue
		}
		if ml, ok := mlPerVolumeUnit[m[2]]; ok {
			return qty * ml * gramsPerML, nil
		}
	}
	return 0, fmt.Errorf("no volume unit found in %q", rawSize)
}

// AsFluidOunces re-expresses a volume size in fluid ounces.
//
// Purely for the display columns (products.pack_size_qty / pack_size_unit).
// My schema's CHECK constraint allows fl_oz but not gal/qt/pt, so a gallon of
// milk has to be recorded as 128 fl_oz to be storable at all. Before this it
// fell through to "each", which made the label read "1.00 each" for a gallon —
// technically harmless (the solver only reads net_weight_g) but wrong on a
// column whose entire job is to be human-readable.
func AsFluidOunces(rawSize string) (float64, bool) {
	s := normalizeSizeString(rawSize)
	for _, m := range sizePattern.FindAllStringSubmatch(s, -1) {
		qty, err := strconv.ParseFloat(m[1], 64)
		if err != nil || qty <= 0 {
			continue
		}
		if ml, ok := mlPerVolumeUnit[m[2]]; ok {
			return qty * ml / mlPerVolumeUnit["floz"], true
		}
	}
	return 0, false
}

// NetWeightGrams is the function the ingestion worker actually calls.
//
// gramsPerItem is the caller's per-FOOD knowledge, from the curated table in
// cmd/krogeringest: "one large egg is 50 grams". It's 0 when unknown.
//
// Splitting it this way keeps ParseSize pure and food-agnostic — it reads a
// string, nothing more — while letting count-based products work when, and only
// when, I've actually recorded what one item weighs. A count product with no
// table entry is an error, not a guess.
func NetWeightGrams(rawSize string, gramsPerItem float64) (float64, error) {
	size, err := ParseSize(rawSize)
	if err != nil {
		return 0, err
	}

	switch size.Kind {
	case KindMass:
		return size.Qty * gramsPerMassUnit[size.Unit], nil

	case KindCount:
		if gramsPerItem <= 0 {
			return 0, fmt.Errorf(
				"size %q is a count (%g items) and I have no grams-per-item entry for this food",
				rawSize, size.Qty)
		}
		return size.Qty * gramsPerItem, nil

	default:
		return 0, fmt.Errorf("size %q is not convertible to grams", rawSize)
	}
}
