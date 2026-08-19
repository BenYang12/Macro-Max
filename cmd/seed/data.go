package main

// This file contains the curated food and nutrition catalog. Products and
// prices come exclusively from the University Place Kroger ingestion.

// NUTRITION VALUES: per-100g, in the shape USDA publishes. These are
// hand-entered approximations of USDA SR Legacy / Foundation values — good
// enough to build and demo the solver against. Phase 2 replaces every one of
// them with authoritative numbers pulled from the FoodData Central API and
// sets each food's fdc_id, which is exactly why fdc_id is left NULL here.
// Do not treat these as citable nutrition data.

// seedFood is one generic food in the nutrition catalog.
type seedFood struct {
	Name     string
	Category string   // must pass the schema CHECK constraint
	Tags     []string // dietary flags
	Kcal     float64  // per 100g
	Protein  float64  // per 100g
	Carbs    float64  // per 100g
	Fat      float64  // per 100g
	MaxGrams float64  // palatability cap, grams/week. 0 = no cap (-> NULL)
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
var seedFoods = []seedFood{
	// ---------------------------------------------------------------- proteins
	{
		Name: "Chicken Breast, raw", Category: "protein", Tags: tagsMeatGF,
		Kcal: 120, Protein: 22.5, Carbs: 0, Fat: 2.6,
	},
	{
		Name: "Chicken Thigh, raw", Category: "protein", Tags: tagsMeatGF,
		Kcal: 143, Protein: 19.7, Carbs: 0, Fat: 6.6,
	},
	{
		Name: "Ground Beef, 90/10, raw", Category: "protein", Tags: tagsMeatGF,
		Kcal: 176, Protein: 20.0, Carbs: 0, Fat: 10.0,
	},
	{
		Name: "Ground Turkey, 93/7, raw", Category: "protein", Tags: tagsMeatGF,
		Kcal: 150, Protein: 18.9, Carbs: 0, Fat: 8.3,
	},
	{
		Name: "Pork Loin, raw", Category: "protein", Tags: tagsMeatGF,
		Kcal: 143, Protein: 21.4, Carbs: 0, Fat: 5.7,
	},
	{
		Name: "Salmon, Atlantic farmed, raw", Category: "protein", Tags: tagsMeatGF,
		Kcal: 208, Protein: 20.4, Carbs: 0, Fat: 13.4,
	},
	{
		Name: "Tilapia, raw", Category: "protein", Tags: tagsMeatGF,
		Kcal: 96, Protein: 20.1, Carbs: 0, Fat: 1.7,
	},
	{
		Name: "Tuna, canned in water, drained", Category: "protein", Tags: tagsMeatGF,
		Kcal: 116, Protein: 25.5, Carbs: 0, Fat: 0.8,
	},
	{
		// The 'dozen' pack unit exercises non-mass package-size parsing.
		Name: "Eggs, whole, raw", Category: "protein", Tags: tagsVegGF,
		Kcal: 143, Protein: 12.6, Carbs: 0.7, Fat: 9.5,
	},
	{
		Name: "Egg Whites, liquid", Category: "protein", Tags: tagsVegGF,
		Kcal: 52, Protein: 10.9, Carbs: 0.7, Fat: 0.2,
	},
	{
		// Protein-dense and cheap per gram; cap weekly use so it cannot dominate
		// an otherwise varied basket.
		Name: "Whey Protein Isolate, powder", Category: "protein", Tags: tagsVegGF,
		Kcal: 370, Protein: 80.0, Carbs: 8.0, Fat: 3.0, MaxGrams: 1400,
	},
	{
		Name: "Tofu, firm", Category: "protein", Tags: tagsVegan,
		Kcal: 144, Protein: 17.3, Carbs: 2.8, Fat: 8.7,
	},
	{
		Name: "Black Beans, dried", Category: "protein", Tags: tagsVegan,
		Kcal: 341, Protein: 21.6, Carbs: 62.4, Fat: 1.4,
	},
	{
		Name: "Lentils, dried", Category: "protein", Tags: tagsVegan,
		Kcal: 352, Protein: 24.6, Carbs: 63.4, Fat: 1.1,
	},

	// ------------------------------------------------------------------- carbs
	{
		// STIGLER PLANT #2 + the TWO-PACK-SIZE plant. The 10 lb bag is ~0.20
		// c/g vs the 2 lb bag's ~0.27 c/g, so buying big is cheaper per gram
		// but wastes money if you only need a little. That trade-off is
		// exactly what integer pack variables exist to resolve.
		Name: "White Rice, long grain, dry", Category: "carb", Tags: tagsVegan,
		Kcal: 365, Protein: 7.1, Carbs: 80.0, Fat: 0.7, MaxGrams: 3000,
	},
	{
		Name: "Brown Rice, dry", Category: "carb", Tags: tagsVegan,
		Kcal: 370, Protein: 7.9, Carbs: 77.2, Fat: 2.9,
	},
	{
		Name: "Rolled Oats, dry", Category: "carb", Tags: tagsVegan,
		Kcal: 379, Protein: 13.2, Carbs: 67.7, Fat: 6.5,
	},
	{
		Name: "Pasta, dry", Category: "carb", Tags: tagsVeg,
		Kcal: 371, Protein: 13.0, Carbs: 74.7, Fat: 1.5,
	},
	{
		Name: "Potatoes, russet, raw", Category: "carb", Tags: tagsVegan,
		Kcal: 79, Protein: 2.1, Carbs: 18.1, Fat: 0.1,
	},
	{
		Name: "Sweet Potato, raw", Category: "carb", Tags: tagsVegan,
		Kcal: 86, Protein: 1.6, Carbs: 20.1, Fat: 0.1,
	},
	{
		Name: "Bread, whole wheat", Category: "carb", Tags: tagsVeg,
		Kcal: 254, Protein: 12.3, Carbs: 43.1, Fat: 3.6,
	},
	{
		Name: "Tortillas, flour", Category: "carb", Tags: tagsVeg,
		Kcal: 306, Protein: 8.2, Carbs: 51.2, Fat: 7.0,
	},
	{
		Name: "Quinoa, dry", Category: "carb", Tags: tagsVegan,
		Kcal: 368, Protein: 14.1, Carbs: 64.2, Fat: 6.1,
	},
	{
		Name: "Corn, frozen kernels", Category: "carb", Tags: tagsVegan,
		Kcal: 88, Protein: 3.1, Carbs: 20.8, Fat: 0.9,
	},

	// -------------------------------------------------------------------- fats
	{
		Name: "Olive Oil", Category: "fat", Tags: tagsVegan,
		Kcal: 884, Protein: 0, Carbs: 0, Fat: 100.0, MaxGrams: 400,
	},
	{
		// The cheapest calories in the catalog, capped to prevent dominance.
		Name: "Canola Oil", Category: "fat", Tags: tagsVegan,
		Kcal: 884, Protein: 0, Carbs: 0, Fat: 100.0, MaxGrams: 400,
	},
	{
		Name: "Peanut Butter", Category: "fat", Tags: tagsVegan,
		Kcal: 588, Protein: 25.1, Carbs: 19.6, Fat: 50.4, MaxGrams: 1000,
	},
	{
		Name: "Almonds, raw", Category: "fat", Tags: tagsVegan,
		Kcal: 579, Protein: 21.2, Carbs: 21.6, Fat: 49.9, MaxGrams: 700,
	},
	{
		Name: "Avocado", Category: "fat", Tags: tagsVegan,
		Kcal: 160, Protein: 2.0, Carbs: 8.5, Fat: 14.7,
	},

	// -------------------------------------------------------------- vegetables
	{
		Name: "Broccoli, raw", Category: "vegetable", Tags: tagsVegan,
		Kcal: 34, Protein: 2.8, Carbs: 6.6, Fat: 0.4,
	},
	{
		Name: "Spinach, raw", Category: "vegetable", Tags: tagsVegan,
		Kcal: 23, Protein: 2.9, Carbs: 3.6, Fat: 0.4,
	},
	{
		Name: "Carrots, raw", Category: "vegetable", Tags: tagsVegan,
		Kcal: 41, Protein: 0.9, Carbs: 9.6, Fat: 0.2,
	},
	{
		Name: "Bell Pepper, red, raw", Category: "vegetable", Tags: tagsVegan,
		Kcal: 31, Protein: 1.0, Carbs: 6.0, Fat: 0.3,
	},
	{
		Name: "Onions, yellow, raw", Category: "vegetable", Tags: tagsVegan,
		Kcal: 40, Protein: 1.1, Carbs: 9.3, Fat: 0.1,
	},
	{
		Name: "Mixed Vegetables, frozen", Category: "vegetable", Tags: tagsVegan,
		Kcal: 65, Protein: 3.3, Carbs: 13.0, Fat: 0.4,
	},

	// ------------------------------------------------------------------ fruits
	{
		Name: "Bananas", Category: "fruit", Tags: tagsVegan,
		Kcal: 89, Protein: 1.1, Carbs: 22.8, Fat: 0.3,
	},
	{
		Name: "Apples", Category: "fruit", Tags: tagsVegan,
		Kcal: 52, Protein: 0.3, Carbs: 13.8, Fat: 0.2,
	},
	{
		Name: "Blueberries, frozen", Category: "fruit", Tags: tagsVegan,
		Kcal: 57, Protein: 0.7, Carbs: 14.5, Fat: 0.3,
	},
	{
		Name: "Oranges", Category: "fruit", Tags: tagsVegan,
		Kcal: 47, Protein: 0.9, Carbs: 11.8, Fat: 0.1,
	},

	// ------------------------------------------------------------------- dairy
	{
		Name: "Milk, 2% reduced fat", Category: "dairy", Tags: tagsVegGF,
		Kcal: 50, Protein: 3.3, Carbs: 4.8, Fat: 2.0,
	},
	{
		Name: "Greek Yogurt, plain nonfat", Category: "dairy", Tags: tagsVegGF,
		Kcal: 59, Protein: 10.2, Carbs: 3.6, Fat: 0.4,
	},
	{
		Name: "Cheddar Cheese", Category: "dairy", Tags: tagsVegGF,
		Kcal: 403, Protein: 22.9, Carbs: 3.4, Fat: 33.1, MaxGrams: 500,
	},
}
