package store

import "time"

// My design -> three-layer stack
// Each layer knows only about the one below it

// HTTP request (outside world) -> handler -> store (SQL) -> Postgres
// The Store layer is aware that a database (Postgres) exists and holds specific tables. However, Postgres does not know your store layer exists.

// Food struct =  one row in the foods table (migration 000001).
// Every column becomes a struct field.

// When I Scan a row, columns map to fields POSITIONALLY (first column -> first &field), so I keep the field
// order here matching the column order I SELECT — purely as a sanity habit.
// I installed pgx -> go driver -> a specialized software component that acts as a translator between a Go application and a specific database system.

// TYPE MAPPING:
//	BIGINT        -> int64     (both are 64-bit integers, exact match)
//	TEXT          -> string
//	TEXT[]        -> []string  (pgx translates Postgres arrays natively)
//	NUMERIC(6,2)  -> float64
//	timestamptz   -> time.Time (pgx hands me real Go time values, in UTC)

// Notes:
// - NULLABLE COLUMNS -> POINTER can be nil, so *int64 gives three state honesty: nil = SQL NULL. Rule for my project -> NULL-able column takes a pointer field, NOT NULL column takes a plain field
// STRUCT TAGS — the `json:"..."` backtick strings:
// Same mechanism as the Health handler. encoding/json reads these at runtime
// via reflection to name the JSON keys, so my API speaks snake_case while my
// Go speaks CamelCase. A nil pointer encodes as JSON null — the honest answer
// for "no fdc_id yet".

// maps to `foods` table, each food struct is one row
type Food struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	FdcID    *int64   `json:"fdc_id"` // NULL until Phase 2 links USDA records
	Category string   `json:"category"`
	Tags     []string `json:"tags"`

	KcalPer100g     float64 `json:"kcal_per_100g"`
	ProteinGPer100g float64 `json:"protein_g_per_100g"`
	CarbsGPer100g   float64 `json:"carbs_g_per_100g"`
	FatGPer100g     float64 `json:"fat_g_per_100g"`

	MaxGramsPerWeek *float64 `json:"max_grams_per_week"` // NULL = no palatability cap

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Product is one row of the products table (migration 00002)
// One pack size of one food at one store
// money is int64 and not float64 do to schema saying INT cents

// WHY MONEY IS int64 AND NOT float64:
// The schema says INT cents, and the project law says money is integer cents
type Product struct {
	ID     int64 `json:"id"`
	FoodID int64 `json:"food_id"`

	StoreID    string `json:"store_id"`    // Kroger locationId
	ExternalID string `json:"external_id"` // the store's own product id

	Name  string  `json:"name"`
	Brand *string `json:"brand"` // nullable column means I need to use a pointer field

	// Raw label size, for display/debug only.
	PackSizeQty  float64 `json:"pack_size_qty"`
	PackSizeUnit string  `json:"pack_size_unit"`

	// The single reconciled truth — the only mass the solver ever sees.
	NetWeightG float64 `json:"net_weight_g"`

	PriceCents      int64  `json:"price_cents"`
	PromoPriceCents *int64 `json:"promo_price_cents"` // NULL = not on sale

	// A COALESCE is a SQL function that returns the first non-NULL value from a list I give it.
	// "use this, but if its NULL, fall back to that" -> COALESCE(a,b,c) -> Postgres checks left to right and returns the first one that isn't NULL

	// EffectivePriceCents is NOT a column. It's COALESCE(promo, price),
	// computed by the query — see products.go.
	// It is a A DERIVED field: the database answers "what would I actually pay?" once, so no caller re-derives it
	// and gets it subtly wrong.
	EffectivePriceCents int64 `json:"effective_price_cents"`

	Available bool      `json:"available"`
	FetchedAt time.Time `json:"fetched_at"`

	// FoodName is joined in from the foods table for display, so a client
	// listing products doesn't have to make a second request per row to learn
	// what food each product IS.

	// A JOIN combines rows from two tables into one result, matching them on a shared value.
	// food_id in products references foods(id)
	// each product row points at a food row by storing its id

	// `omitempty` is a NEW struct-tag option: omit this key from the JSON
	// entirely when the value is the zero value (here, ""). Queries that don't
	// JOIN foods leave it empty, and the API then simply has no food_name key
	// rather than a misleading "food_name": "".
	FoodName string `json:"food_name,omitempty"`
}

// UserTarget is one row of user_targets (migration 000004): what the user wants the solver to hit
// no auth yet -> rows are told apart by a human

// PERIOD SUFFIXES ARE THE POINT. Macros are entered DAILY
// budget is WEEKLY, and solver works in weekly totals
// x7 conversion happens in the handler at solve time, exactly once
type UserTarget struct {
	ID    int64  `json:"id"`
	Label string `json:"label"`

	ProteinGDaily int `json:"protein_g_daily"`
	CarbsGDaily   int `json:"carbs_g_daily"`
	FatGDaily     int `json:"fat_g_daily"`

	// NULL = no calorie ceiling ("macros-only mode"). Pointer, as always for
	// a nullable column — 0 would mean "a ceiling of zero calories".
	CaloriesMaxDaily *int `json:"calories_max_daily"`

	BudgetCentsWeekly int    `json:"budget_cents_weekly"`
	StoreID           string `json:"store_id"`

	// Postgres arrays. pgx maps TEXT[] <-> []string and BIGINT[] <-> []int64
	// natively, so these need no special handling.
	DietTags       []string `json:"diet_tags"`
	ExcludeFoodIDs []int64  `json:"exclude_food_ids"`

	CreatedAt        time.Time `json:"created_at"`
	CapabilityDigest []byte    `json:"-"`
}
