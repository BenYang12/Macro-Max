package main

// mapping.go — my curated food -> Kroger search term table, plus the
// grams-per-item table for count-based products.
//
// WHY SEARCH TERMS AND NOT PINNED PRODUCT IDS:
// A pinned productId is exact but brittle — it breaks the day Kroger
// discontinues that SKU, and it has to be re-curated for every store I add.
// A search term survives both. The cost is that a term can drift onto the wrong
// product, which is why matchesSearchTerm below is deliberately strict.
//
// The terms are still HUMAN-CHOSEN. "Chicken Breast, raw" is my food name, and
// "boneless skinless chicken breast" is what a shopper would actually type. No
// algorithm derives one from the other; I wrote each of these by thinking about
// what would come back.

// excludeWords are product-name substrings that mean "this is not the food I
// asked for, no matter how well the words match."
//
// THIS LIST EXISTS BECAUSE MY FIRST DRY RUN WAS WRONG. Searching "baby
// spinach" returned Babylife Organics baby-food pouches and a Simple Truth
// smoothie pouch — both contain the words "baby" and "spinach", so my
// all-words filter happily accepted them. "canola oil" matched Land O Lakes
// spreadable BUTTER and PAM cooking SPRAY.
//
// That is exactly the silent-wrong-data failure I've been designing against
// all project: no error, no symptom, just a solver quietly told that 100g of
// cooking spray is 100g of canola oil. The lesson I'm taking is that a
// positive filter ("all my words appear") can never be sufficient on its own,
// because product names are marketing copy and marketing copy contains
// everything.
//
// A global list rather than per-food terms: the junk categories are the same
// across foods (sprays, pouches, seasoned/prepared versions), so one list is
// less to curate and less to leave gaps in. When something new slips through, it
// gets one line here.
var excludeWords = []string{
	// Prepared/processed forms that aren't the raw ingredient.
	"spray", "cooking spray", "spreadable", "seasoned", "marinated",
	"breaded", "battered", "nugget", "patty", "patties", "tender",

	// Baby and toddler food, which collides badly with "baby spinach",
	// "baby carrots", and anything containing fruit names.
	"baby food", "baby snack", "teether", "stage 1", "stage 2", "stage 3",
	"babylife", "gerber", "pouch",

	// Drinks and smoothies that list vegetables in the name.
	"smoothie", "juice", "beverage", "drink mix",

	// Jarred/canned when I want fresh, plus condiments.
	"roasted red", "pickled", "dressing", "sauce", "dip", "hummus",

	// Cured/smoked forms. My catalog models RAW proteins, and smoked salmon
	// or cured meat has meaningfully different macros (and a lot more sodium)
	// than the fresh fillet my nutrition data describes.
	"smoked", "cured", "lox",

	// Prepared single-serve cups and instant versions of dry goods.
	"instant", "cup", "cups", "microwavable",

	// Supplements and bars that mention protein sources. "bar" is now matched
	// as a WHOLE WORD — as a substring it ate "Barilla" and threw out every
	// box of pasta on my second dry run.
	"bar", "bars", "shake", "meal replacement",

	// Pet food, which really does show up in meat searches.
	"dog", "dogs", "cat food", "pet", "pets",
}

// liquidDensity is grams per millilitre for the foods sold by volume.
//
// Everything else in this project refuses to convert volume to mass, because
// doing it without a density is a guess. This table is what makes the
// conversion legitimate for these four: a LOOKED-UP density is data, not an
// assumption.
//
// It also settles the bare-"oz" ambiguity. For a food in this table, "32 oz"
// is read as 32 FLUID ounces, because that's how liquids are labeled. For
// everything else "32 oz" stays weight. The distinction used to cost me every
// oil, milk, and liquid egg white on the shelf.
//
// Densities at room temperature:
//
//	olive oil   0.915  (lighter than water — oils float)
//	canola oil  0.920
//	milk 2%     1.032  (slightly denser than water; fat lowers it, solids raise it)
//	egg white   1.030
//
// A food NOT in this table still gets the old treatment: volume sizes are
// rejected outright. There is deliberately no default density, because a
// wrong default is exactly the silent error I've been avoiding all project.
var liquidDensity = map[string]float64{
	"Olive Oil":            0.915,
	"Canola Oil":           0.920,
	"Milk, 2% reduced fat": 1.032,
	"Egg Whites, liquid":   1.030,
}

// foodSearch maps one of my foods to how I'd search for it at Kroger.
type foodSearch struct {
	// FoodName must match foods.name EXACTLY — it's the lookup key.
	FoodName string

	// Term is what gets sent as filter.term.
	Term string

	// GramsPerItem is the weight of ONE unit for count-sold products
	// ("12 ct" eggs). 0 means "this food is never sold by count", and a count
	// size will be skipped rather than guessed.
	GramsPerItem float64
}

// searchTerms is the curated catalog.
//
// EVERY GramsPerItem VALUE BELOW NEEDS YOUR REVIEW. I derived them from the net
// weights already in cmd/seed/data.go rather than inventing new numbers, so
// they're at least consistent with the rest of the project — but they are
// estimates of typical retail sizes, not measurements. A wrong value here
// silently misstates how much food a pack contains.
//
//	eggs      600g/dozen  -> 50g each   (USDA "large" egg is ~50g)
//	avocado   600g/4ct    -> 150g each  (whole fruit; edible portion is less)
//	pepper    500g/3ct    -> 167g each
//	tortilla  450g/10ct   -> 45g each
//
// The avocado one is the shakiest: a 150g avocado is ~100g of edible flesh once
// the pit and skin are gone, and Kroger sells the whole fruit. If the solver
// starts recommending implausible amounts of avocado, this is the number to
// look at first.
var searchTerms = []foodSearch{
	// ---- proteins ----
	{FoodName: "Chicken Breast, raw", Term: "boneless skinless chicken breast"},
	{FoodName: "Chicken Thigh, raw", Term: "boneless skinless chicken thighs"},
	// Harris Teeter stocks 93/7 and 80/20, never 90/10. Requiring "93%"
	// picks the lean one and EXCLUDES the 80/20, whose macros are far off
	// (254 kcal vs 176 per 100g). My food models 90/10, so the real product
	// is a touch leaner than my nutrition says — closer than the alternative.
	{FoodName: "Ground Beef, 90/10, raw", Term: "93% lean ground beef"},
	{FoodName: "Ground Turkey, 93/7, raw", Term: "ground turkey 93 lean"},
	{FoodName: "Pork Loin, raw", Term: "boneless pork loin"},
	// Dropped "fillet": HT sells "Fresh Atlantic Salmon" with no such word,
	// so requiring it matched nothing.
	{FoodName: "Salmon, Atlantic farmed, raw", Term: "atlantic salmon"},
	{FoodName: "Tilapia, raw", Term: "tilapia fillets"},
	{FoodName: "Tuna, canned in water, drained", Term: "chunk light tuna water"},
	{FoodName: "Eggs, whole, raw", Term: "large grade a eggs", GramsPerItem: 50},
	{FoodName: "Egg Whites, liquid", Term: "liquid egg whites"},
	{FoodName: "Whey Protein Isolate, powder", Term: "whey protein isolate"},
	{FoodName: "Tofu, firm", Term: "firm tofu"},
	// "dried" -> "dry". HT labels them "Dry Black Turtle Beans", and the word
	// "dry" is what separates them from the wall of CANNED black beans that
	// dominate this search.
	{FoodName: "Black Beans, dried", Term: "dry black beans"},
	// Same "dried" -> "dry" fix, and it also filters out the instant/prepared
	// lentil cups that share the search.
	{FoodName: "Lentils, dried", Term: "dry lentils"},

	// ---- carbs ----
	{FoodName: "White Rice, long grain, dry", Term: "long grain white rice"},
	{FoodName: "Brown Rice, dry", Term: "long grain brown rice"},
	{FoodName: "Rolled Oats, dry", Term: "old fashioned rolled oats"},
	{FoodName: "Pasta, dry", Term: "spaghetti pasta"},
	{FoodName: "Potatoes, russet, raw", Term: "russet potatoes"},
	{FoodName: "Sweet Potato, raw", Term: "sweet potatoes"},
	{FoodName: "Bread, whole wheat", Term: "whole wheat bread"},
	{FoodName: "Tortillas, flour", Term: "flour tortillas", GramsPerItem: 45},
	{FoodName: "Quinoa, dry", Term: "quinoa"},
	{FoodName: "Corn, frozen kernels", Term: "frozen sweet corn"},

	// ---- fats ----
	// Oils are sold by VOLUME, so the parser will reject them and they'll be
	// skipped. That's the honest outcome: converting fl oz to grams needs a
	// density I don't have. They remain absent until a trustworthy mapping exists.
	{FoodName: "Olive Oil", Term: "extra virgin olive oil"},
	{FoodName: "Canola Oil", Term: "canola oil"},
	{FoodName: "Peanut Butter", Term: "creamy peanut butter"},
	{FoodName: "Almonds, raw", Term: "raw almonds"},
	// "hass avocados" returned nothing usable on my first run — Harris Teeter
	// seems to list them as plain "avocado". A reminder that these terms are
	// guesses about someone else's naming until a dry run proves otherwise.
	{FoodName: "Avocado", Term: "avocado", GramsPerItem: 150},

	// ---- vegetables ----
	{FoodName: "Broccoli, raw", Term: "broccoli florets"},
	{FoodName: "Spinach, raw", Term: "baby spinach"},
	{FoodName: "Carrots, raw", Term: "whole carrots"},
	{FoodName: "Bell Pepper, red, raw", Term: "red bell peppers", GramsPerItem: 167},
	{FoodName: "Onions, yellow, raw", Term: "yellow onions"},
	{FoodName: "Mixed Vegetables, frozen", Term: "frozen mixed vegetables"},

	// ---- fruits ----
	{FoodName: "Bananas", Term: "bananas"},
	{FoodName: "Apples", Term: "gala apples"},
	{FoodName: "Blueberries, frozen", Term: "frozen blueberries"},
	{FoodName: "Oranges", Term: "navel oranges"},

	// ---- dairy ----
	// Milk is sold by volume and will be skipped, same as the oils.
	{FoodName: "Milk, 2% reduced fat", Term: "2% reduced fat milk"},
	{FoodName: "Greek Yogurt, plain nonfat", Term: "plain nonfat greek yogurt"},
	{FoodName: "Cheddar Cheese", Term: "sharp cheddar cheese block"},
}
