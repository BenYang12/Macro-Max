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

// curatedMapping links each seeded food to the USDA record that describes it.
//
// Every id below came from a live FDC search whose descriptions were read
// before choosing — none was recalled from memory or inferred from a pattern,
// which is the failure mode the warning above is about. Each entry was then
// confirmed end to end with `-all -dry-run`, which fetches the record,
// normalizes it to per-100g, and runs fdc.Validate against the food's stored
// category. An id that names the wrong food usually fails that check; an id
// that names a *plausible but wrong* food does not, which is why the Note on
// each line records the exact description chosen.
//
// Three rules decided the picks, and they matter more than the ids:
//
//  1. Foundation over SR Legacy. Foundation records are current lab analyses;
//     SR Legacy is the frozen 2019 reference set. SR Legacy appears below only
//     where Foundation has no equivalent record, and those lines say so.
//
//  2. Match the form BOUGHT, not the form eaten. The catalog prices raw
//     chicken and dry rice, so every entry is the raw/dry record. Cooked
//     numbers would silently inflate protein per gram — cooking drives off
//     water — and every solve would buy too little of everything.
//
//  3. Match the search term in cmd/krogeringest/mapping.go. That file decides
//     which product lands in the catalog; this one decides the nutrition
//     attached to it. When the two disagree the database is quietly
//     self-inconsistent, so "boneless skinless chicken breast" is paired with
//     the boneless skinless record, "gala apples" with the gala record.
//
// Deliberately NOT used: Branded records (per-serving label data tied to one
// manufacturer) and "0% moisture" laboratory records such as fdc 747444 for
// black beans. A 0% moisture basis describes a fully dried sample, but dried
// beans are sold at roughly 11% moisture, so that record would overstate
// protein per gram by about an eighth.
var curatedMapping = []foodMapping{
	// ---------------------------------------------------------------- proteins
	{FoodName: "Chicken Breast, raw", FdcID: 2646170, Note: "Foundation: Chicken, breast, boneless, skinless, raw"},
	{FoodName: "Chicken Thigh, raw", FdcID: 2646171, Note: "Foundation: Chicken, thigh, boneless, skinless, raw"},
	{FoodName: "Ground Beef, 90/10, raw", FdcID: 2514743, Note: "Foundation: Beef, ground, 90% lean meat / 10% fat, raw"},
	{FoodName: "Ground Turkey, 93/7, raw", FdcID: 2514747, Note: "Foundation: Turkey, ground, 93% lean/ 7% fat, raw"},
	{FoodName: "Pork Loin, raw", FdcID: 2646168, Note: "Foundation: Pork, loin, boneless, raw"},
	{FoodName: "Salmon, Atlantic farmed, raw", FdcID: 2684441, Note: "Foundation: Fish, salmon, Atlantic, farm raised, raw"},
	{FoodName: "Tilapia, raw", FdcID: 2684442, Note: "Foundation: Fish, tilapia, farm raised, raw"},
	{FoodName: "Tuna, canned in water, drained", FdcID: 171986, Note: "SR Legacy: Fish, tuna, light, canned in water, without salt, drained solids — Foundation 334194 is search-only, 404 on detail"},
	{FoodName: "Eggs, whole, raw", FdcID: 171287, Note: "SR Legacy: Egg, whole, raw, fresh — FDC has no Foundation whole-egg record"},
	{FoodName: "Egg Whites, liquid", FdcID: 172183, Note: "SR Legacy: Egg, white, raw, fresh — Foundation 747997 is search-only, 404 on detail"},
	// Whey Protein Isolate, powder is DELIBERATELY ABSENT.
	//
	// The only generic FDC candidate is fdc 173177 "Beverages, Whey protein
	// powder isolate", which reports 58.1g protein and 29.1g carbs per 100g.
	// A real isolate is 85-90% protein and 1-3% carbs, so that record describes
	// a blended or flavored beverage powder, not an isolate. It passes
	// fdc.Validate — the macros are internally consistent and the category
	// check only requires protein >= 5 — which makes it precisely the
	// "plausible but wrong" case this file's header warns about.
	//
	// Every other candidate is Branded (per-serving label data) or soy. Omitting
	// the food leaves the seeded approximation in place with fdc_id NULL, which
	// is what the nullable column is for: "not yet verified" is honest, and a
	// confidently wrong 58g protein would quietly distort every solve that
	// picks whey.
	{FoodName: "Tofu, firm", FdcID: 172475, Note: "SR Legacy: Tofu, raw, firm, prepared with calcium sulfate — generic; the alternatives are brand records"},
	{FoodName: "Black Beans, dried", FdcID: 173734, Note: "SR Legacy: Beans, black, mature seeds, raw — as-sold moisture, unlike the 0% moisture record"},
	{FoodName: "Lentils, dried", FdcID: 2644283, Note: "Foundation: Lentils, dry"},

	// ------------------------------------------------------------------- carbs
	{FoodName: "White Rice, long grain, dry", FdcID: 2512381, Note: "Foundation: Rice, white, long grain, unenriched, raw"},
	{FoodName: "Brown Rice, dry", FdcID: 2512380, Note: "Foundation: Rice, brown, long grain, unenriched, raw"},
	{FoodName: "Rolled Oats, dry", FdcID: 173904, Note: "SR Legacy: Cereals, oats, regular and quick, not fortified, dry — generic, not the QUAKER record"},
	{FoodName: "Pasta, dry", FdcID: 169736, Note: "SR Legacy: Pasta, dry, enriched — Foundation 2758998 carries no protein/carb/fat nutrients"},
	{FoodName: "Potatoes, russet, raw", FdcID: 2346401, Note: "Foundation: Potatoes, russet, without skin, raw"},
	{FoodName: "Sweet Potato, raw", FdcID: 2346404, Note: "Foundation: Sweet potatoes, orange flesh, without skin, raw"},
	{FoodName: "Bread, whole wheat", FdcID: 172688, Note: "SR Legacy: Bread, whole-wheat, commercially prepared — Foundation 2758994 carries no protein/carb/fat nutrients"},
	{FoodName: "Tortillas, flour", FdcID: 175037, Note: "SR Legacy: Tortillas, ready-to-bake or -fry, flour, refrigerated — Foundation 2758996 carries no protein/carb/fat nutrients"},
	{FoodName: "Quinoa, dry", FdcID: 168874, Note: "SR Legacy: Quinoa, uncooked — Foundation has only quinoa flour"},
	{FoodName: "Corn, frozen kernels", FdcID: 168398, Note: "SR Legacy: Corn, sweet, yellow, frozen, kernels cut off cob, unprepared"},

	// -------------------------------------------------------------------- fats
	{FoodName: "Olive Oil", FdcID: 171413, Note: "SR Legacy: Oil, olive, salad or cooking"},
	{FoodName: "Canola Oil", FdcID: 172336, Note: "SR Legacy: Oil, canola — Foundation 748278 reports only fatty-acid detail, no macro nutrients"},
	{FoodName: "Peanut Butter", FdcID: 174266, Note: "SR Legacy: Peanut butter, smooth style, with salt"},
	{FoodName: "Almonds, raw", FdcID: 2346393, Note: "Foundation: Nuts, almonds, whole, raw"},
	{FoodName: "Avocado", FdcID: 2710824, Note: "Foundation: Avocado, Hass, peeled, raw"},

	// -------------------------------------------------------------- vegetables
	{FoodName: "Broccoli, raw", FdcID: 170379, Note: "SR Legacy: Broccoli, raw — Foundation 747447 is search-only, 404 on detail"},
	{FoodName: "Spinach, raw", FdcID: 1999632, Note: "Foundation: Spinach, baby — matches the 'baby spinach' ingest term"},
	{FoodName: "Carrots, raw", FdcID: 2258586, Note: "Foundation: Carrots, mature, raw — matches the 'whole carrots' ingest term"},
	{FoodName: "Bell Pepper, red, raw", FdcID: 2258590, Note: "Foundation: Peppers, bell, red, raw"},
	{FoodName: "Onions, yellow, raw", FdcID: 790646, Note: "Foundation: Onions, yellow, raw"},
	{FoodName: "Mixed Vegetables, frozen", FdcID: 170471, Note: "SR Legacy: Vegetables, mixed, frozen, unprepared"},

	// ------------------------------------------------------------------ fruits
	{FoodName: "Bananas", FdcID: 173944, Note: "SR Legacy: Bananas, raw"},
	{FoodName: "Apples", FdcID: 1750341, Note: "Foundation: Apples, gala, with skin, raw — matches the 'gala apples' ingest term"},
	{FoodName: "Blueberries, frozen", FdcID: 173950, Note: "SR Legacy: Blueberries, frozen, unsweetened"},
	{FoodName: "Oranges", FdcID: 169917, Note: "SR Legacy: Oranges, raw, navels — Foundation 746771 is search-only, 404 on detail"},

	// ------------------------------------------------------------------- dairy
	{FoodName: "Milk, 2% reduced fat", FdcID: 171267, Note: "SR Legacy: Milk, reduced fat, 2% milkfat, added vit A and D — Foundation 746778 is search-only, 404 on detail"},
	{FoodName: "Greek Yogurt, plain nonfat", FdcID: 171312, Note: "SR Legacy: Yogurt, Greek, nonfat, plain, CHOBANI — brand-specific, but Foundation 330137 is search-only and no generic nonfat Greek record is retrievable"},
	{FoodName: "Cheddar Cheese", FdcID: 170899, Note: "SR Legacy: Cheese, cheddar, sharp, sliced — matches the 'sharp cheddar' ingest term; Foundation 328637 is search-only"},
}
