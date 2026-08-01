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

	// Supplements and bars that mention protein sources. "bar" is now matched
	// as a WHOLE WORD — as a substring it ate "Barilla" and threw out every
	// box of pasta on my second dry run.
	"bar", "bars", "shake", "meal replacement",

	// Pet food, which really does show up in meat searches.
	"dog", "dogs", "cat food", "pet", "pets",
}

// liquidFoods are foods sold by volume, where a bare "oz" on the label almost
// certainly means FLUID ounces, not weight.
//
// My parser reads bare "oz" as mass, which is right for cheese and wrong for
// oil. For a 40 fl oz bottle of canola that's about a 4% overstatement of the
// weight — small, invisible, and exactly the kind of quiet error I'd rather
// not have. So for these foods a bare "oz" is REJECTED instead of assumed.
//
// The cost is real: it means most oils and milks get no Kroger price at all,
// and their SEED products carry the solver. I'm choosing that over being
// slightly wrong without knowing it. A density table would recover them, and
// that's a reasonable later addition — but densities are per-food estimates
// too, so it trades one assumption for another.
var liquidFoods = map[string]bool{
	"Olive Oil":            true,
	"Canola Oil":           true,
	"Milk, 2% reduced fat": true,
	"Egg Whites, liquid":   true,
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
	{FoodName: "Ground Beef, 90/10, raw", Term: "ground beef 90 lean"},
	{FoodName: "Ground Turkey, 93/7, raw", Term: "ground turkey 93 lean"},
	{FoodName: "Pork Loin, raw", Term: "boneless pork loin"},
	{FoodName: "Salmon, Atlantic farmed, raw", Term: "atlantic salmon fillet"},
	{FoodName: "Tilapia, raw", Term: "tilapia fillets"},
	{FoodName: "Tuna, canned in water, drained", Term: "chunk light tuna water"},
	{FoodName: "Eggs, whole, raw", Term: "large grade a eggs", GramsPerItem: 50},
	{FoodName: "Egg Whites, liquid", Term: "liquid egg whites"},
	{FoodName: "Whey Protein Isolate, powder", Term: "whey protein isolate"},
	{FoodName: "Tofu, firm", Term: "firm tofu"},
	{FoodName: "Black Beans, dried", Term: "dried black beans"},
	{FoodName: "Lentils, dried", Term: "dried lentils"},

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
	// density I don't have. Their SEED products keep the solver working.
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
