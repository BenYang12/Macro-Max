package main

// mapping.go — the human-curated food name -> FDC id table.
//
// WHY THIS IS HAND-CURATED AND NOT AUTOMATED:
// FoodData Central holds well over half a million records. Searching
// "chicken breast" returns breaded frozen nuggets, rotisserie deli meat, and
// canned chicken alongside the raw boneless breast we actually mean. An
// automatic top-hit match would silently link some foods to the wrong record,
// and wrong nutrition data corrupts every solve that food appears in — with no
// error message and no obvious symptom.
//
// So a human picks each id once, and it is recorded here in version control.
// This is the plan's recommended answer to its own open question ("hand-map
// each seeded food to a Foundation fdc_id vs trusting search").
//
// HOW TO POPULATE IT:
//
//	# 1. Get candidate ids for every seeded food, formatted for pasting here:
//	go run ./cmd/fdcimport -suggest
//
//	# 2. Sanity-check each suggestion. Read the description; confirm it is the
//	#    RAW, UNPREPARED form unless the food name says otherwise.
//	go run ./cmd/fdcimport -search "chicken breast raw"
//
//	# 3. Paste verified entries below, then dry-run before writing:
//	go run ./cmd/fdcimport -all -dry-run
//	go run ./cmd/fdcimport -all
//
// PREFER Foundation > SR Legacy > Branded. Foundation is current lab-measured
// data; SR Legacy is the older but still authoritative reference set; Branded
// is manufacturer-submitted label data with per-serving math and no oversight.

// foodMapping links one row in our foods table to one FDC record.
//
// FoodName must match foods.name EXACTLY — it is the WHERE key of the UPDATE.
// A typo here fails loudly ("no food named ..."), which is the intended
// behavior: better a clear error than a silent no-op.
type foodMapping struct {
	FoodName string
	FdcID    int64
	// Note is free-text provenance: which FDC description was chosen and why.
	// Six months from now this is the only record of the judgment call.
	Note string
}

// curatedMapping is DELIBERATELY EMPTY until a human verifies each entry.
//
// It is not empty because the work was skipped — it is empty because filling it
// in requires reading FDC descriptions and deciding whether each is the right
// food, and that decision cannot be delegated to a tool (or an LLM) without
// reintroducing exactly the silent-wrong-data risk the curation exists to
// prevent. Guessed ids would look identical to verified ones in this file,
// which is what makes guessing here worse than leaving it blank.
//
// `-suggest` does the tedious 90%: it searches every seeded food and prints
// ready-to-paste entries. The remaining 10% — confirming each description
// actually describes the food — is the part that needs eyes.
var curatedMapping = []foodMapping{
	// Example of the intended shape, commented out so `-all` reports an empty
	// mapping rather than importing a single unverified food:
	//
	// {
	// 	FoodName: "Chicken Breast, raw",
	// 	FdcID:    171077,
	// 	Note:     "SR Legacy: Chicken, broilers or fryers, breast, meat only, raw",
	// },
}
