package store

// foods.go: every query that touches the foods table.
// Pattern:
// - list queries -> Pool.Query (many rows: loop + Scan each)
// - get-one	-> Pool.QueryRow (exactly one row: single scan)
//   - "not found"  -> translated into OUR sentinel error, so callers never need to know pgx exists.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrNotFound is a sentinel error: pre-made, named error value that I define once and reuse
// Why my own sentinel error instead of pgx.ErrNoRows escape? -> The store translates "pgx says no rows" into "the store says
// not found", and the handler maps THAT to a 404. Each layer speaks only its
// own vocabulary.

var ErrNotFound = errors.New("resource not found")

// FoodFilter struct carries optional list filters (?category=protein&tag=vegan)
// Struct > two bare string parameters because:
// 1. call sites read better
// 2. adding a filter later changes ONE struct, not every caller
// zero value FoodFilter{} means "no filters, list everything"
type FoodFilter struct {
	Category string // "" = any category
	Tag      string // "" = any tag
}


// ListFoods returns all foods matching the filter, orderd by name
// (s *Store) makes this a METHOD on Store -> anyone holding a *STORE can call st.ListFoods().
func (s *Store) ListFoods (ctx context.Context, filter FoodFilter) ([]Food, error){
	// Recall from SQL:
	// Read Query has three parts:
	// 1. SELECT id, name, category, tags -> WHICH columns I want back
	// 2. FROM foods -> WHICH table
	// 3. WHERE category = 'protein' -> WHICH rows to keep
	// 4. ORDER BY name -> what ORDER to return them in

	// Postgres walks the table one row at a time, and asks my WHERE expression: "for this row, is this true or false": true -> row included. false -> row is skipped
	// Trick to make a filter optional: WHERE ($1 = '' OR category = $1) -> $1 is empty string
	// @> means "contains" -> tags @> ARRAY[$2] asks "does the tags list contain everything on the right?"


	// ListFoods runs ONE fixed query that covers all cases: no filter, category
	// only, tag only, or both. We never build SQL strings by hand — that's how
	// injection bugs happen.
	
	// The query looks like:
	//   SELECT ... FROM foods
	//   WHERE ($1 = '' OR category = $1)          -- category filter (optional)
	//     AND ($2 = '' OR tags @> ARRAY[$2]::text[])  -- tag filter (optional)
	//   ORDER BY name
	
	// HOW "WHERE" WORKS: Postgres checks each row and keeps it only if the
	// expression is true for that row. So WHERE is a yes/no test, once per row.
	//
	// THE OPTIONAL-FILTER TRICK:  ($1 = '' OR category = $1)
	//   - No category given  -> we pass $1 = ""  -> '' = '' is TRUE
	//     -> the OR is true for every row -> filter does nothing (off).
	//   - Category given      -> '' = $1 is FALSE
	//     -> the OR now depends on  category = $1  -> real filtering (on).
	//   The empty string is the filter's "off switch." Tag filter works the same.
	//
	// PLACEHOLDERS ($1, $2): the SQL text and the values are sent to Postgres
	// separately. Values are slotted in as pure DATA, never as SQL, so a food
	// named  '); DROP TABLE foods;--  is just a weird name, not an attack.
	//
	// THE TAG LINE:  tags @> ARRAY[$2]::text[]
	//   - tags is a LIST of strings, not one value.
	//   - @> means "contains": does the tags list contain everything on the right?
	//   - ARRAY[$2] wraps our one tag into a 1-element list (so list meets list).
	//   - ::text[] casts that list to "array of text" — Postgres can't infer the
	//     type from a bare placeholder, so we say it explicitly.
	//   - This is exactly the operator the GIN index (migration 000001) speeds up.
	query := `
		SELECT id, name, fdc_id, category, tags,
		       kcal_per_100g, protein_g_per_100g, carbs_g_per_100g, fat_g_per_100g,
		       max_grams_per_week, created_at, updated_at
		FROM foods
		WHERE ($1 = '' OR category = $1)
		  AND ($2 = '' OR tags @> ARRAY[$2]::text[])
		ORDER BY name`

	// query borrows a connection from pool, sends the SQL, and returns a CURSOR -> rows stream as I iterate, not all at once.
	rows, err := s.Pool.Query(ctx, query, filter.Category, filter.Tag) //pgxpool.Pool.Query(ctx, sql, args ... any )
	if err != nil {
		return nil, fmt.Errorf("querying foods: %w", err)
	}

	// cursor HOLDS the pooled connection until closed. 
	// defer, or connections leak until pool runs dry -> classic outage
	defer rows.Close()

	//[]Food{} (empty slice)
	// not `var foods []Food` (nil slice)
	// empty slice -> [], nil slice -> null. "No matches" should be
	// {"foods": []}, never {"foods": null} — frontends .map() over [] safely.
	foods := []Food{}

	//rows.Next() advances to the next row and reports whether one exists.
	// It returns false both when we're DONE and when the stream BROKE - which is why rows.Err() is checked after the loop
	for rows.Next(){
		var f Food
		// Scan copies this row's columns into these pointers, IN ORDER.
		// passes the ADDRESS so Scan can write through it. Nullable columns scan into the pointer fields: NULL -> pointer stays nil, value -> pgx allocates and fills it
		// In short, scan takes each row's columns, and pours them into these specific go variables of mine.
		err := rows.Scan(
			&f.ID, &f.Name, &f.FdcID, &f.Category, &f.Tags,
			&f.KcalPer100g, &f.ProteinGPer100g, &f.CarbsGPer100g, &f.FatGPer100g,
			&f.MaxGramsPerWeek, &f.CreatedAt, &f.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning food row: %w", err)
		}
		foods = append(foods, f)
	}

	// Crucial and easy to forget: Next() returning false does NOT distinguish
	// "no more rows" from "the connection died mid-stream". rows.Err() holds
	// any error that ended iteration early. Skip this and a TRUNCATED list
	// gets returned as if it were complete.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating food rows: %w", err)
	}

	return foods, nil

}

// GetFood returns a single food by primary key, or ErrNotFound
func (s *Store) GetFood(ctx context.Context, id int64) (Food, error){
	query := `
		SELECT id, name, fdc_id, category, tags,
		       kcal_per_100g, protein_g_per_100g, carbs_g_per_100g, fat_g_per_100g,
		       max_grams_per_week, created_at, updated_at
		FROM foods
		WHERE id = $1`

	var f Food
	// QueryRow is the one-row convenience. defers all errors to Scan - including pgx.ErrNoRows when nothing matched. 
	// Scan handles one row per call
	err := s.Pool.QueryRow(ctx, query, id).Scan(
		&f.ID, &f.Name, &f.FdcID, &f.Category, &f.Tags,
		&f.KcalPer100g, &f.ProteinGPer100g, &f.CarbsGPer100g, &f.FatGPer100g,
		&f.MaxGramsPerWeek, &f.CreatedAt, &f.UpdatedAt,
	)

	if err != nil {
		// The translation point: pgx vocabulary -> store vocabulary.
		// errors.Is (not ==) so wrapping never breaks the comparison.
		if errors.Is(err, pgx.ErrNoRows) {
			return Food{}, ErrNotFound
		}
		return Food{}, fmt.Errorf("querying food %d: %w", id, err)
	}
	return f, nil

}