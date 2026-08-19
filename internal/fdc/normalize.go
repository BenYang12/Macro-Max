package fdc

// normalize.go — turning FDC's several inconsistent shapes into ONE trustworthy
// per-100g record.
//
// Everything here is a PURE FUNCTION: input in, value out, no I/O, no clock, no
// database, no network. That is a deliberate design choice, not an accident.
// The tricky logic in this phase is arithmetic (unit conversion, per-serving
// division, sanity ratios), and pure functions let all of it be tested
// exhaustively with a table and zero setup. The messy I/O lives in client.go;
// the messy judgment lives here; neither contaminates the other.

import (
	"fmt"
	"math"
	"strings"
)

// Per100g is the normalized result — the shape our foods table stores.
// This is the ONLY thing the rest of the app should ever see from FDC.
type Per100g struct {
	Kcal     float64
	ProteinG float64
	CarbsG   float64
	FatG     float64

	// Provenance, carried along so the importer can record and report it.
	FdcID       int64
	Description string
	DataType    string
}

// kcalPerKJ converts kilojoules to kilocalories. Some FDC records report
// energy only in kJ (nutrient 1062) rather than kcal (1008), and 1 kcal is
// defined as exactly 4.184 kJ.
const kcalPerKJ = 4.184

// gramsPerUnit maps the mass units FDC uses in servingSizeUnit to grams.
// Keys are lowercase; callers must lowercase before looking up, because FDC's
// casing is inconsistent ("G", "g", "GRM" all appear in the wild).
//
// NOTE WHAT IS ABSENT: ml, l, fl oz, and every other VOLUME unit. Converting
// volume to mass requires density, which FDC does not provide and which varies
// per food (oil is 0.92 g/ml, honey is 1.42). Guessing would silently corrupt
// every macro downstream. Volume servings are REJECTED instead — the same
// skip-and-log-never-guess rule the plan sets for Kroger pack sizes.
var gramsPerUnit = map[string]float64{
	"g":    1,
	"grm":  1,
	"gram": 1,
	"mg":   0.001,
	"kg":   1000,
	"oz":   28.349523125,
	"lb":   453.59237,
}

// Normalize converts one FDC food record into per-100g macros.
//
// It dispatches on DataType because the three types report nutrition in
// genuinely different ways — this function's whole job is hiding that.
func Normalize(d FoodDetail) (Per100g, error) {
	out := Per100g{
		FdcID:       d.FdcID,
		Description: d.Description,
		DataType:    d.DataType,
	}

	switch d.DataType {
	case DataTypeBranded:
		// Branded is the awkward path: per-serving label values.
		return normalizeBranded(d, out)
	default:
		// Foundation, SR Legacy, and anything else with a foodNutrients array
		// report per-100g already. Treating unknown types this way is the
		// safer default: if FDC adds a new lab-measured type, the per-100g
		// reading is far more likely to be right than the label reading.
		return normalizeNutrients(d, out)
	}
}

// normalizeNutrients handles Foundation / SR Legacy: values already per-100g.
func normalizeNutrients(d FoodDetail, out Per100g) (Per100g, error) {
	if len(d.FoodNutrients) == 0 {
		return Per100g{}, fmt.Errorf("fdc %d (%s): no foodNutrients array", d.FdcID, d.DataType)
	}

	// Track whether energy was found at all, and in which unit. A food can
	// legitimately have 0 protein (oil), so "did I see this nutrient?" cannot
	// be inferred from the value being zero — the same nil-vs-zero problem as
	// everywhere else, solved here with explicit found flags.
	var (
		foundProtein, foundCarbs, foundFat bool
		kcal, kj                           float64
		foundKcal, foundKJ                 bool
		atwaterSpecific, atwaterGeneral    float64
		foundSpecific, foundGeneral        bool
	)

	for _, fn := range d.FoodNutrients {
		switch fn.Nutrient.ID {
		case NutrientProtein:
			out.ProteinG, foundProtein = fn.Amount, true
		case NutrientCarbs:
			out.CarbsG, foundCarbs = fn.Amount, true
		case NutrientFat:
			out.FatG, foundFat = fn.Amount, true
		case NutrientEnergyKC:
			kcal, foundKcal = fn.Amount, true
		case NutrientEnergyKJ:
			kj, foundKJ = fn.Amount, true
		case NutrientEnergyAtwaterSpecific:
			atwaterSpecific, foundSpecific = fn.Amount, true
		case NutrientEnergyAtwaterGeneral:
			atwaterGeneral, foundGeneral = fn.Amount, true
		}
	}

	// Missing MACROS is fatal: a food with no protein figure is unusable as a
	// solver input, and defaulting to zero would quietly make it look like a
	// pure carb source.
	var missing []string
	if !foundProtein {
		missing = append(missing, "protein")
	}
	if !foundCarbs {
		missing = append(missing, "carbs")
	}
	if !foundFat {
		missing = append(missing, "fat")
	}
	if len(missing) > 0 {
		return Per100g{}, fmt.Errorf("fdc %d: missing nutrient(s): %s",
			d.FdcID, strings.Join(missing, ", "))
	}

	// Energy, in descending order of directness. Preferring a value already
	// reported in kcal avoids compounding a unit conversion we don't need.
	//
	//  1008  plain "Energy" in kcal — what SR Legacy reports.
	//  2048  Atwater SPECIFIC factors: per-food-group coefficients, the more
	//        accurate of the two Atwater figures, so it outranks 2047.
	//  2047  Atwater GENERAL factors: the flat 4/4/9.
	//  1062  kJ, converted last because it is the only lossy branch.
	//
	// 2047 is worth understanding before trusting it: it is *derived* from the
	// same macros this record already reports, so Validate's Atwater tripwire
	// cannot detect a bad record whose energy came from 2047 — the check would
	// be comparing the macros against themselves. That is a reason to prefer
	// 2048, not a reason to reject 2047, which is still USDA's own figure.
	switch {
	case foundKcal:
		out.Kcal = kcal
	case foundSpecific:
		out.Kcal = atwaterSpecific
	case foundGeneral:
		out.Kcal = atwaterGeneral
	case foundKJ:
		out.Kcal = kj / kcalPerKJ
	default:
		return Per100g{}, fmt.Errorf(
			"fdc %d: no energy nutrient (none of %d kcal, %d/%d Atwater kcal, %d kJ)",
			d.FdcID, NutrientEnergyKC, NutrientEnergyAtwaterSpecific,
			NutrientEnergyAtwaterGeneral, NutrientEnergyKJ)
	}

	return out, nil
}

// normalizeBranded handles Branded foods, whose labelNutrients are PER SERVING.
//
// The arithmetic: label value is "per one serving", the serving is
// ServingSize ServingSizeUnit, so
//
//	per100g = labelValue / servingGrams * 100
//
// Getting this backwards (multiplying instead of dividing) is the classic bug,
// and it produces numbers that look plausible for servings near 100 g — which
// is why the test table below includes a serving deliberately far from 100.
func normalizeBranded(d FoodDetail, out Per100g) (Per100g, error) {
	if d.LabelNutrients == nil {
		// Some Branded records carry a foodNutrients array too. Prefer that
		// over failing outright.
		if len(d.FoodNutrients) > 0 {
			return normalizeNutrients(d, out)
		}
		return Per100g{}, fmt.Errorf("fdc %d: branded food has no labelNutrients", d.FdcID)
	}

	if d.ServingSize <= 0 {
		return Per100g{}, fmt.Errorf("fdc %d: branded food has non-positive servingSize %v",
			d.FdcID, d.ServingSize)
	}

	// Convert the serving size to grams, REJECTING volume units.
	unit := strings.ToLower(strings.TrimSpace(d.ServingSizeUnit))
	factor, ok := gramsPerUnit[unit]
	if !ok {
		return Per100g{}, fmt.Errorf(
			"fdc %d: cannot convert serving unit %q to grams (volume units are not convertible without density)",
			d.FdcID, d.ServingSizeUnit)
	}
	servingGrams := d.ServingSize * factor

	// Guard against a serving so tiny that scaling to 100 g explodes the
	// numbers. A sub-gram serving is either a data error or a spice, and
	// neither belongs in a grocery optimizer.
	if servingGrams < 1 {
		return Per100g{}, fmt.Errorf("fdc %d: serving size %.3f g is implausibly small",
			d.FdcID, servingGrams)
	}

	// scale converts one per-serving value to per-100g.
	scale := 100 / servingGrams

	ln := d.LabelNutrients
	// Each label field is a pointer: nil means the label omitted it. Macros
	// are required; energy is required. Same reasoning as the Foundation path.
	var missing []string
	if ln.Protein == nil {
		missing = append(missing, "protein")
	}
	if ln.Carbohydrates == nil {
		missing = append(missing, "carbohydrates")
	}
	if ln.Fat == nil {
		missing = append(missing, "fat")
	}
	if ln.Calories == nil {
		missing = append(missing, "calories")
	}
	if len(missing) > 0 {
		return Per100g{}, fmt.Errorf("fdc %d: branded label missing %s",
			d.FdcID, strings.Join(missing, ", "))
	}

	out.ProteinG = ln.Protein.Value * scale
	out.CarbsG = ln.Carbohydrates.Value * scale
	out.FatG = ln.Fat.Value * scale
	out.Kcal = ln.Calories.Value * scale

	return out, nil
}

// ----------------------------------------------------------------- validation

// Validate is the tripwire layer: it catches records that PARSED fine but
// cannot be true. FDC is a real dataset with real errors, and a single bad
// macro silently poisons every solve that food appears in.
//
// It is separate from Normalize on purpose. Normalize answers "what does this
// record say?"; Validate answers "should I believe it?". Keeping them apart
// means the importer can log a rejection with the parsed values included,
// which is what makes a data problem diagnosable.
//
// The category argument is a small deviation from the plan's `Validate(p)`
// signature: the "zero protein on a protein food" tripwire needs to know the
// category, and threading it in beats a second function that callers forget to
// call. Pass "" to skip the category-specific check.
func Validate(p Per100g, category string) error {
	// 1. Macros cannot exceed the mass they are measured in. 100 g of food
	// cannot contain 120 g of protein. The threshold is 105, not 100, because
	// legitimate rounding and moisture accounting can push a pure protein
	// isolate a hair over 100.
	const maxGramsPer100g = 105
	if p.ProteinG > maxGramsPer100g || p.CarbsG > maxGramsPer100g || p.FatG > maxGramsPer100g {
		return fmt.Errorf("implausible macro > %dg/100g (protein %.1f, carbs %.1f, fat %.1f)",
			maxGramsPer100g, p.ProteinG, p.CarbsG, p.FatG)
	}

	// Negative anything is a parse or data error.
	if p.ProteinG < 0 || p.CarbsG < 0 || p.FatG < 0 || p.Kcal < 0 {
		return fmt.Errorf("negative value (protein %.1f, carbs %.1f, fat %.1f, kcal %.1f)",
			p.ProteinG, p.CarbsG, p.FatG, p.Kcal)
	}

	// Total macro mass over 105 g is impossible even when each one alone is
	// fine — this catches the per-serving-division bug, which inflates all
	// three proportionally and so can slip past the individual checks.
	if total := p.ProteinG + p.CarbsG + p.FatG; total > maxGramsPer100g {
		return fmt.Errorf("macros sum to %.1fg per 100g, which is impossible", total)
	}

	// 2. Energy must roughly match the Atwater estimate: 4 kcal/g protein,
	// 4 kcal/g carbs, 9 kcal/g fat. FDC's measured energy legitimately differs
	// (fiber, sugar alcohols, and specific-factor foods like nuts), which is
	// why calories are STORED rather than derived — but a 25% gap means
	// something is wrong, most likely a unit or scaling error.
	//
	// Skipped for near-zero-calorie foods, where a small absolute difference
	// is a huge relative one and the check would fire on every lettuce.
	atwater := 4*p.ProteinG + 4*p.CarbsG + 9*p.FatG
	const minKcalToCheck = 20
	if p.Kcal >= minKcalToCheck && atwater >= minKcalToCheck {
		deviation := math.Abs(p.Kcal-atwater) / atwater
		const maxDeviation = 0.25
		if deviation > maxDeviation {
			return fmt.Errorf(
				"energy %.0f kcal deviates %.0f%% from the %.0f kcal implied by macros (limit %.0f%%)",
				p.Kcal, deviation*100, atwater, maxDeviation*100)
		}
	}

	// A food with calories but no macros at all means the macros failed to
	// parse while energy succeeded — a partial-parse bug worth catching.
	if p.Kcal >= minKcalToCheck && atwater < 1 {
		return fmt.Errorf("food has %.0f kcal but essentially no macros", p.Kcal)
	}

	// 3. Category coherence. A food filed under 'protein' that reports no
	// protein is mismatched — either the wrong FDC record got linked, or the
	// category is wrong. Either way a human should look.
	if category == "protein" && p.ProteinG < 5 {
		return fmt.Errorf("category is 'protein' but food has only %.1fg protein per 100g", p.ProteinG)
	}

	return nil
}
