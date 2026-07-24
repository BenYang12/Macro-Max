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

// maps to `foods` table
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