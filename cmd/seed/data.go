package main

// data.go — the seed catalog: ~42 generic foods and their fake 'SEED'
// products. This file is DATA ONLY, no logic; main.go does all the work.

// This is my see data -> a real command I run (make seed)
// goes into the actual Postgres database
// The purpose is to populate a real DB so I can run and demo the app
// it sits in my dev database until I wipe it

// WHY SEEDS ARE NOT A MIGRATION:
// Migrations describe SCHEMA (the shape of the tables) and are applied exactly
// once, in order, in every environment including production.

// Seeds are DATA -> developer convenience, re-runnable, and not something production necessarily
// wants.

// NUTRITION VALUES: per-100g, in the shape USDA publishes. These are
// hand-entered approximations of USDA SR Legacy / Foundation values — good
// enough to build and demo the solver against. Phase 2 replaces every one of
// them with authoritative numbers pulled from the FoodData Central API and
// sets each food's fdc_id, which is exactly why fdc_id is left NULL here.
// Do not treat these as citable nutrition data.
//
// PRICES: invented but realistic US grocery prices, in integer cents.
// store_id is the literal 'SEED' so real Kroger data (Phase 5) can coexist in
// the same table without ever colliding with these.

// seedProduct is one purchasable pack of a food at the fake 'SEED' store.
//
// Note the deliberately SIMPLE field types: plain string and int64, no
// pointers. Nullable DB columns become pointers in store.Product, but here at
// the data-entry boundary pointers would make every row noisy. Instead a zero
// value means "absent" (PromoPriceCents == 0 -> not on sale, Brand == "" ->
// no brand), and main.go converts those zeros into real SQL NULLs. Keep the
// awkward representation at the edge where it's needed, not in the data.
type seedProduct struct {
	ExternalID string // fake store SKU; the ON CONFLICT upsert key with store_id
	Name       string
	Brand      string
	PackQty    float64 // raw label quantity, e.g. 2.5
	PackUnit   string  // raw label unit, e.g. "lb" (must pass the schema CHECK)
	NetWeightG float64 // the reconciled truth — the only mass the solver sees
	PriceCents int64
	PromoCents int64 // 0 = not on sale
}

// seedFood is one generic food plus the products that supply it.
type seedFood struct {
	Name     string
	Category string   // must pass the schema CHECK constraint
	Tags     []string // dietary flags
	Kcal     float64  // per 100g
	Protein  float64  // per 100g
	Carbs    float64  // per 100g
	Fat      float64  // per 100g
	MaxGrams float64  // palatability cap, grams/week. 0 = no cap (-> NULL)
	Products []seedProduct
}

// Tag shorthands. Declaring these once keeps the table below readable and
// makes a typo a COMPILE error rather than a silently-wrong tag in the DB.
var (
	tagsVegan  = []string{"vegetarian", "vegan", "gluten_free", "dairy_free"}
	tagsVegGF  = []string{"vegetarian", "gluten_free"}
	tagsVeg    = []string{"vegetarian"}
	tagsMeatGF = []string{"gluten_free", "dairy_free"}
)

// seedFoods is the whole catalog. Coverage is deliberate, per the plan:
// 14 proteins, 10 carbs, 5 fats, 6 vegetables, 4 fruits, 3 dairy.
//
// THREE INTENTIONAL PLANTS, each earning its place:
//  1. White rice has TWO pack sizes (2 lb and 10 lb, with the big bag cheaper
//     per gram). Phase 4's MILP must CHOOSE between them — with only one pack
//     size per food, integer-pack optimization has nothing to decide.
//  2. Cheap whey + cheap canola oil + cheap rice together make the classic
//     "Stigler diet" degenerate answer reachable: hit every macro target for
//     almost no money by eating three joyless things. Phase 3's LP is SUPPOSED
//     to return that — it's the demo of why Phase 4's variety constraints
//     exist. Success looks ugly on purpose.
//  3. Eggs are sold by the 'dozen' and avocados/peppers by 'each', exercising
//     the non-mass pack units that Phase 5's unit parser must handle.
var seedFoods = []seedFood{
	// ---------------------------------------------------------------- proteins
	{
		Name: "Chicken Breast, raw", Category: "protein", Tags: tagsMeatGF,
		Kcal: 120, Protein: 22.5, Carbs: 0, Fat: 2.6,
		Products: []seedProduct{
			{ExternalID: "seed-chicken-breast-3lb", Name: "Boneless Skinless Chicken Breast, 3 lb",
				Brand: "Store Brand", PackQty: 3, PackUnit: "lb", NetWeightG: 1360.8, PriceCents: 1047},
		},
	},
	{
		Name: "Chicken Thigh, raw", Category: "protein", Tags: tagsMeatGF,
		Kcal: 143, Protein: 19.7, Carbs: 0, Fat: 6.6,
		Products: []seedProduct{
			{ExternalID: "seed-chicken-thigh-2.5lb", Name: "Boneless Skinless Chicken Thighs, 2.5 lb",
				Brand: "Store Brand", PackQty: 2.5, PackUnit: "lb", NetWeightG: 1134.0, PriceCents: 624},
		},
	},
	{
		Name: "Ground Beef, 90/10, raw", Category: "protein", Tags: tagsMeatGF,
		Kcal: 176, Protein: 20.0, Carbs: 0, Fat: 10.0,
		Products: []seedProduct{
			{ExternalID: "seed-ground-beef-1lb", Name: "Ground Beef 90% Lean, 1 lb",
				Brand: "Store Brand", PackQty: 1, PackUnit: "lb", NetWeightG: 453.6, PriceCents: 699},
		},
	},
	{
		Name: "Ground Turkey, 93/7, raw", Category: "protein", Tags: tagsMeatGF,
		Kcal: 150, Protein: 18.9, Carbs: 0, Fat: 8.3,
		Products: []seedProduct{
			{ExternalID: "seed-ground-turkey-1lb", Name: "Ground Turkey 93% Lean, 1 lb",
				Brand: "Store Brand", PackQty: 1, PackUnit: "lb", NetWeightG: 453.6, PriceCents: 549},
		},
	},
	{
		Name: "Pork Loin, raw", Category: "protein", Tags: tagsMeatGF,
		Kcal: 143, Protein: 21.4, Carbs: 0, Fat: 5.7,
		Products: []seedProduct{
			{ExternalID: "seed-pork-loin-2lb", Name: "Boneless Pork Loin, 2 lb",
				Brand: "Store Brand", PackQty: 2, PackUnit: "lb", NetWeightG: 907.2, PriceCents: 798},
		},
	},
	{
		Name: "Salmon, Atlantic farmed, raw", Category: "protein", Tags: tagsMeatGF,
		Kcal: 208, Protein: 20.4, Carbs: 0, Fat: 13.4,
		Products: []seedProduct{
			{ExternalID: "seed-salmon-1lb", Name: "Atlantic Salmon Fillet, 1 lb",
				Brand: "Store Brand", PackQty: 1, PackUnit: "lb", NetWeightG: 453.6, PriceCents: 1299},
		},
	},
	{
		Name: "Tilapia, raw", Category: "protein", Tags: tagsMeatGF,
		Kcal: 96, Protein: 20.1, Carbs: 0, Fat: 1.7,
		Products: []seedProduct{
			{ExternalID: "seed-tilapia-2lb", Name: "Frozen Tilapia Fillets, 2 lb",
				Brand: "Store Brand", PackQty: 2, PackUnit: "lb", NetWeightG: 907.2, PriceCents: 998},
		},
	},
	{
		Name: "Tuna, canned in water, drained", Category: "protein", Tags: tagsMeatGF,
		Kcal: 116, Protein: 25.5, Carbs: 0, Fat: 0.8,
		Products: []seedProduct{
			{ExternalID: "seed-tuna-5oz", Name: "Chunk Light Tuna in Water, 5 oz",
				Brand: "Store Brand", PackQty: 5, PackUnit: "oz", NetWeightG: 141.7, PriceCents: 129},
		},
	},
	{
		// The 'dozen' pack unit — one of the non-mass units Phase 5 must parse.
		Name: "Eggs, whole, raw", Category: "protein", Tags: tagsVegGF,
		Kcal: 143, Protein: 12.6, Carbs: 0.7, Fat: 9.5,
		Products: []seedProduct{
			{ExternalID: "seed-eggs-dozen", Name: "Large Grade A Eggs, 12 ct",
				Brand: "Store Brand", PackQty: 1, PackUnit: "dozen", NetWeightG: 600.0, PriceCents: 349},
		},
	},
	{
		Name: "Egg Whites, liquid", Category: "protein", Tags: tagsVegGF,
		Kcal: 52, Protein: 10.9, Carbs: 0.7, Fat: 0.2,
		Products: []seedProduct{
			{ExternalID: "seed-egg-whites-32oz", Name: "Liquid Egg Whites, 32 fl oz",
				Brand: "Store Brand", PackQty: 32, PackUnit: "fl_oz", NetWeightG: 946.0, PriceCents: 599},
		},
	},
	{
		// STIGLER PLANT #1: absurdly protein-dense and cheap per gram of
		// protein. An unconstrained LP will happily buy nothing else.
		// MaxGrams caps it at 1400 g/week so Phase 4 can tax the monotony.
		Name: "Whey Protein Isolate, powder", Category: "protein", Tags: tagsVegGF,
		Kcal: 370, Protein: 80.0, Carbs: 8.0, Fat: 3.0, MaxGrams: 1400,
		Products: []seedProduct{
			{ExternalID: "seed-whey-5lb", Name: "Whey Protein Isolate, 5 lb Tub",
				Brand: "Value Nutrition", PackQty: 5, PackUnit: "lb", NetWeightG: 2268.0, PriceCents: 4499},
		},
	},
	{
		Name: "Tofu, firm", Category: "protein", Tags: tagsVegan,
		Kcal: 144, Protein: 17.3, Carbs: 2.8, Fat: 8.7,
		Products: []seedProduct{
			{ExternalID: "seed-tofu-14oz", Name: "Firm Tofu, 14 oz",
				Brand: "Store Brand", PackQty: 14, PackUnit: "oz", NetWeightG: 396.9, PriceCents: 229},
		},
	},
	{
		Name: "Black Beans, dried", Category: "protein", Tags: tagsVegan,
		Kcal: 341, Protein: 21.6, Carbs: 62.4, Fat: 1.4,
		Products: []seedProduct{
			{ExternalID: "seed-black-beans-1lb", Name: "Dried Black Beans, 1 lb",
				Brand: "Store Brand", PackQty: 1, PackUnit: "lb", NetWeightG: 453.6, PriceCents: 179},
		},
	},
	{
		Name: "Lentils, dried", Category: "protein", Tags: tagsVegan,
		Kcal: 352, Protein: 24.6, Carbs: 63.4, Fat: 1.1,
		Products: []seedProduct{
			{ExternalID: "seed-lentils-1lb", Name: "Dried Green Lentils, 1 lb",
				Brand: "Store Brand", PackQty: 1, PackUnit: "lb", NetWeightG: 453.6, PriceCents: 199},
		},
	},

	// ------------------------------------------------------------------- carbs
	{
		// STIGLER PLANT #2 + the TWO-PACK-SIZE plant. The 10 lb bag is ~0.20
		// c/g vs the 2 lb bag's ~0.27 c/g, so buying big is cheaper per gram
		// but wastes money if you only need a little. That trade-off is
		// exactly what integer pack variables exist to resolve.
		Name: "White Rice, long grain, dry", Category: "carb", Tags: tagsVegan,
		Kcal: 365, Protein: 7.1, Carbs: 80.0, Fat: 0.7, MaxGrams: 3000,
		Products: []seedProduct{
			{ExternalID: "seed-white-rice-2lb", Name: "Long Grain White Rice, 2 lb",
				Brand: "Store Brand", PackQty: 2, PackUnit: "lb", NetWeightG: 907.2, PriceCents: 249},
			{ExternalID: "seed-white-rice-10lb", Name: "Long Grain White Rice, 10 lb",
				Brand: "Store Brand", PackQty: 10, PackUnit: "lb", NetWeightG: 4536.0, PriceCents: 899},
		},
	},
	{
		Name: "Brown Rice, dry", Category: "carb", Tags: tagsVegan,
		Kcal: 370, Protein: 7.9, Carbs: 77.2, Fat: 2.9,
		Products: []seedProduct{
			{ExternalID: "seed-brown-rice-2lb", Name: "Long Grain Brown Rice, 2 lb",
				Brand: "Store Brand", PackQty: 2, PackUnit: "lb", NetWeightG: 907.2, PriceCents: 299},
		},
	},
	{
		Name: "Rolled Oats, dry", Category: "carb", Tags: tagsVegan,
		Kcal: 379, Protein: 13.2, Carbs: 67.7, Fat: 6.5,
		Products: []seedProduct{
			{ExternalID: "seed-oats-42oz", Name: "Old Fashioned Rolled Oats, 42 oz",
				Brand: "Store Brand", PackQty: 42, PackUnit: "oz", NetWeightG: 1190.7, PriceCents: 449},
		},
	},
	{
		Name: "Pasta, dry", Category: "carb", Tags: tagsVeg,
		Kcal: 371, Protein: 13.0, Carbs: 74.7, Fat: 1.5,
		Products: []seedProduct{
			{ExternalID: "seed-pasta-1lb", Name: "Spaghetti, 1 lb",
				Brand: "Store Brand", PackQty: 1, PackUnit: "lb", NetWeightG: 453.6, PriceCents: 149},
		},
	},
	{
		Name: "Potatoes, russet, raw", Category: "carb", Tags: tagsVegan,
		Kcal: 79, Protein: 2.1, Carbs: 18.1, Fat: 0.1,
		Products: []seedProduct{
			{ExternalID: "seed-potatoes-5lb", Name: "Russet Potatoes, 5 lb Bag",
				Brand: "Store Brand", PackQty: 5, PackUnit: "lb", NetWeightG: 2268.0, PriceCents: 499},
		},
	},
	{
		Name: "Sweet Potato, raw", Category: "carb", Tags: tagsVegan,
		Kcal: 86, Protein: 1.6, Carbs: 20.1, Fat: 0.1,
		Products: []seedProduct{
			{ExternalID: "seed-sweet-potato-3lb", Name: "Sweet Potatoes, 3 lb",
				Brand: "Store Brand", PackQty: 3, PackUnit: "lb", NetWeightG: 1360.8, PriceCents: 449},
		},
	},
	{
		Name: "Bread, whole wheat", Category: "carb", Tags: tagsVeg,
		Kcal: 254, Protein: 12.3, Carbs: 43.1, Fat: 3.6,
		Products: []seedProduct{
			{ExternalID: "seed-bread-20oz", Name: "100% Whole Wheat Bread, 20 oz",
				Brand: "Store Brand", PackQty: 20, PackUnit: "oz", NetWeightG: 567.0, PriceCents: 329},
		},
	},
	{
		Name: "Tortillas, flour", Category: "carb", Tags: tagsVeg,
		Kcal: 306, Protein: 8.2, Carbs: 51.2, Fat: 7.0,
		Products: []seedProduct{
			{ExternalID: "seed-tortillas-10ct", Name: "Flour Tortillas, 10 ct",
				Brand: "Store Brand", PackQty: 10, PackUnit: "each", NetWeightG: 450.0, PriceCents: 299},
		},
	},
	{
		Name: "Quinoa, dry", Category: "carb", Tags: tagsVegan,
		Kcal: 368, Protein: 14.1, Carbs: 64.2, Fat: 6.1,
		Products: []seedProduct{
			{ExternalID: "seed-quinoa-16oz", Name: "Organic Quinoa, 16 oz",
				Brand: "Store Brand", PackQty: 16, PackUnit: "oz", NetWeightG: 453.6, PriceCents: 549},
		},
	},
	{
		Name: "Corn, frozen kernels", Category: "carb", Tags: tagsVegan,
		Kcal: 88, Protein: 3.1, Carbs: 20.8, Fat: 0.9,
		Products: []seedProduct{
			{ExternalID: "seed-corn-16oz", Name: "Frozen Sweet Corn, 16 oz",
				Brand: "Store Brand", PackQty: 16, PackUnit: "oz", NetWeightG: 453.6, PriceCents: 129},
		},
	},

	// -------------------------------------------------------------------- fats
	{
		Name: "Olive Oil", Category: "fat", Tags: tagsVegan,
		Kcal: 884, Protein: 0, Carbs: 0, Fat: 100.0, MaxGrams: 400,
		Products: []seedProduct{
			{ExternalID: "seed-olive-oil-25oz", Name: "Extra Virgin Olive Oil, 25.4 fl oz",
				Brand: "Store Brand", PackQty: 25.4, PackUnit: "fl_oz", NetWeightG: 686.0, PriceCents: 999},
		},
	},
	{
		// STIGLER PLANT #3: the cheapest calories in the entire catalog.
		// ~0.38 c/g for 9 kcal/g. An unconstrained LP loves this.
		Name: "Canola Oil", Category: "fat", Tags: tagsVegan,
		Kcal: 884, Protein: 0, Carbs: 0, Fat: 100.0, MaxGrams: 400,
		Products: []seedProduct{
			{ExternalID: "seed-canola-oil-48oz", Name: "Canola Oil, 48 fl oz",
				Brand: "Store Brand", PackQty: 48, PackUnit: "fl_oz", NetWeightG: 1305.0, PriceCents: 499},
		},
	},
	{
		Name: "Peanut Butter", Category: "fat", Tags: tagsVegan,
		Kcal: 588, Protein: 25.1, Carbs: 19.6, Fat: 50.4, MaxGrams: 1000,
		Products: []seedProduct{
			{ExternalID: "seed-peanut-butter-40oz", Name: "Creamy Peanut Butter, 40 oz",
				Brand: "Store Brand", PackQty: 40, PackUnit: "oz", NetWeightG: 1134.0, PriceCents: 649},
		},
	},
	{
		Name: "Almonds, raw", Category: "fat", Tags: tagsVegan,
		Kcal: 579, Protein: 21.2, Carbs: 21.6, Fat: 49.9, MaxGrams: 700,
		Products: []seedProduct{
			{ExternalID: "seed-almonds-16oz", Name: "Raw Almonds, 16 oz",
				Brand: "Store Brand", PackQty: 16, PackUnit: "oz", NetWeightG: 453.6, PriceCents: 799},
		},
	},
	{
		Name: "Avocado", Category: "fat", Tags: tagsVegan,
		Kcal: 160, Protein: 2.0, Carbs: 8.5, Fat: 14.7,
		Products: []seedProduct{
			{ExternalID: "seed-avocado-4ct", Name: "Hass Avocados, 4 ct",
				Brand: "Store Brand", PackQty: 4, PackUnit: "each", NetWeightG: 600.0, PriceCents: 500},
		},
	},

	// -------------------------------------------------------------- vegetables
	{
		Name: "Broccoli, raw", Category: "vegetable", Tags: tagsVegan,
		Kcal: 34, Protein: 2.8, Carbs: 6.6, Fat: 0.4,
		Products: []seedProduct{
			{ExternalID: "seed-broccoli-12oz", Name: "Broccoli Florets, 12 oz",
				Brand: "Store Brand", PackQty: 12, PackUnit: "oz", NetWeightG: 340.2, PriceCents: 249},
		},
	},
	{
		Name: "Spinach, raw", Category: "vegetable", Tags: tagsVegan,
		Kcal: 23, Protein: 2.9, Carbs: 3.6, Fat: 0.4,
		Products: []seedProduct{
			{ExternalID: "seed-spinach-10oz", Name: "Baby Spinach, 10 oz",
				Brand: "Store Brand", PackQty: 10, PackUnit: "oz", NetWeightG: 283.5, PriceCents: 349},
		},
	},
	{
		Name: "Carrots, raw", Category: "vegetable", Tags: tagsVegan,
		Kcal: 41, Protein: 0.9, Carbs: 9.6, Fat: 0.2,
		Products: []seedProduct{
			{ExternalID: "seed-carrots-2lb", Name: "Whole Carrots, 2 lb",
				Brand: "Store Brand", PackQty: 2, PackUnit: "lb", NetWeightG: 907.2, PriceCents: 229},
		},
	},
	{
		Name: "Bell Pepper, red, raw", Category: "vegetable", Tags: tagsVegan,
		Kcal: 31, Protein: 1.0, Carbs: 6.0, Fat: 0.3,
		Products: []seedProduct{
			{ExternalID: "seed-bell-pepper-3ct", Name: "Red Bell Peppers, 3 ct",
				Brand: "Store Brand", PackQty: 3, PackUnit: "each", NetWeightG: 500.0, PriceCents: 399},
		},
	},
	{
		Name: "Onions, yellow, raw", Category: "vegetable", Tags: tagsVegan,
		Kcal: 40, Protein: 1.1, Carbs: 9.3, Fat: 0.1,
		Products: []seedProduct{
			{ExternalID: "seed-onions-3lb", Name: "Yellow Onions, 3 lb Bag",
				Brand: "Store Brand", PackQty: 3, PackUnit: "lb", NetWeightG: 1360.8, PriceCents: 349},
		},
	},
	{
		Name: "Mixed Vegetables, frozen", Category: "vegetable", Tags: tagsVegan,
		Kcal: 65, Protein: 3.3, Carbs: 13.0, Fat: 0.4,
		Products: []seedProduct{
			{ExternalID: "seed-mixed-veg-12oz", Name: "Frozen Mixed Vegetables, 12 oz",
				Brand: "Store Brand", PackQty: 12, PackUnit: "oz", NetWeightG: 340.2, PriceCents: 179},
		},
	},

	// ------------------------------------------------------------------ fruits
	{
		Name: "Bananas", Category: "fruit", Tags: tagsVegan,
		Kcal: 89, Protein: 1.1, Carbs: 22.8, Fat: 0.3,
		Products: []seedProduct{
			{ExternalID: "seed-bananas-3lb", Name: "Bananas, 3 lb",
				Brand: "Store Brand", PackQty: 3, PackUnit: "lb", NetWeightG: 1360.8, PriceCents: 177},
		},
	},
	{
		Name: "Apples", Category: "fruit", Tags: tagsVegan,
		Kcal: 52, Protein: 0.3, Carbs: 13.8, Fat: 0.2,
		Products: []seedProduct{
			{ExternalID: "seed-apples-3lb", Name: "Gala Apples, 3 lb Bag",
				Brand: "Store Brand", PackQty: 3, PackUnit: "lb", NetWeightG: 1360.8, PriceCents: 499},
		},
	},
	{
		Name: "Blueberries, frozen", Category: "fruit", Tags: tagsVegan,
		Kcal: 57, Protein: 0.7, Carbs: 14.5, Fat: 0.3,
		Products: []seedProduct{
			{ExternalID: "seed-blueberries-16oz", Name: "Frozen Blueberries, 16 oz",
				Brand: "Store Brand", PackQty: 16, PackUnit: "oz", NetWeightG: 453.6, PriceCents: 399},
		},
	},
	{
		Name: "Oranges", Category: "fruit", Tags: tagsVegan,
		Kcal: 47, Protein: 0.9, Carbs: 11.8, Fat: 0.1,
		Products: []seedProduct{
			{ExternalID: "seed-oranges-4lb", Name: "Navel Oranges, 4 lb Bag",
				Brand: "Store Brand", PackQty: 4, PackUnit: "lb", NetWeightG: 1814.4, PriceCents: 599},
		},
	},

	// ------------------------------------------------------------------- dairy
	{
		Name: "Milk, 2% reduced fat", Category: "dairy", Tags: tagsVegGF,
		Kcal: 50, Protein: 3.3, Carbs: 4.8, Fat: 2.0,
		Products: []seedProduct{
			{ExternalID: "seed-milk-gallon", Name: "2% Reduced Fat Milk, 1 gal",
				Brand: "Store Brand", PackQty: 128, PackUnit: "fl_oz", NetWeightG: 3878.0, PriceCents: 399},
		},
	},
	{
		Name: "Greek Yogurt, plain nonfat", Category: "dairy", Tags: tagsVegGF,
		Kcal: 59, Protein: 10.2, Carbs: 3.6, Fat: 0.4,
		Products: []seedProduct{
			{ExternalID: "seed-greek-yogurt-32oz", Name: "Plain Nonfat Greek Yogurt, 32 oz",
				Brand: "Store Brand", PackQty: 32, PackUnit: "oz", NetWeightG: 907.2, PriceCents: 549,
				PromoCents: 449}, // one item on sale, so COALESCE has real work to do
		},
	},
	{
		Name: "Cheddar Cheese", Category: "dairy", Tags: tagsVegGF,
		Kcal: 403, Protein: 22.9, Carbs: 3.4, Fat: 33.1, MaxGrams: 500,
		Products: []seedProduct{
			{ExternalID: "seed-cheddar-8oz", Name: "Sharp Cheddar Cheese Block, 8 oz",
				Brand: "Store Brand", PackQty: 8, PackUnit: "oz", NetWeightG: 226.8, PriceCents: 349},
		},
	},
}
