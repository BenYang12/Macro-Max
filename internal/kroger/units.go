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
	// Normalize first so the matching below only deals with one shape:
	// lowercase, "fl oz" collapsed to "floz", punctuation that separates
	// compound sizes turned into spaces.
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return Size{}, fmt.Errorf("empty size string")
	}
	s = strings.ReplaceAll(s, "fluid ounce", "floz")
	s = strings.ReplaceAll(s, "fl oz", "floz")
	s = strings.ReplaceAll(s, "fl. oz", "floz")
	s = strings.ReplaceAll(s, "fl_oz", "floz")
	// "/" and "," separate the halves of a compound size; "approx" and "about"
	// are noise I can drop entirely.
	s = strings.NewReplacer("/", " ", ",", " ", "approx", " ", "about", " ", "~", " ").Replace(s)

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
