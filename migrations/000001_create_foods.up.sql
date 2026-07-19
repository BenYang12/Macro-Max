-- My database schema currently lives nowhere
-- If I "make down-v", every table is gone with no record of what existed. 
-- When this app deploys, the production database needs to end up with the exact same schema as my laptop. 

-- Thus I rely on migrations -> Migrations make schema changes into versioned, ordered, executable files in the repo. 
-- Three rules define the system:
-- 1. Numbered and ordered. 000001 runs before 000002
-- 2. Every migration is a pair: .up.sql makes the change, .down.sql undoes it 
-- 3. Applied at most once. The migrate tool creates a bookkeeping table in my database called schema-migrations that records the current version number

-- Project Schema conventions
-- BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY -> modern standard auto-ID
-- Money is INT cents. Floats can't represent 0.1 exactly. 
-- Macros are NUMERIC(p,s) per-100g. "NUMERIC(6,2)" means 6 significant digits, 2 after the decimal. 
-- Timestamps are timestamtz, stored as UTC. 
-- TEXT everywhere, never VARCHAR(n). 
-- CHECK constraints are the database refusing bad data.









------------------------- Foods Migration------------------------------
-- foods: the catalog of GENERIC foods and their per-100g nutrition.

CREATE TABLE foods(
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,

    -- USDA FoodData Central id. Null until phase 2 links each food to its official USDA record. 
    -- UNIQUE still allows many NULLs in Postgres.
    fdc_id BIGINT UNIQUE,

    -- Check constraint -> rule that the database enforces on every insert and update: category in ('protein', 'carb', ...) is true only if the value exactly matches one of those seven strings.
    -- Anything else makes the database throw an error and refuse the row
    -- essentially an enum
    -- My solver's variety constraints (">= 3 protein sources, >= 2 vegetables") depends on this column being trustworthy
    category TEXT NOT NULL CHECK (category IN
        ('protein','carb','fat','vegetable','fruit','dairy','pantry')),

    -- TEXT[] is a Postgres ARRAY column: {'vegan', 'gluten_free'}
    -- Holds dietary flags like {'vegetarian','gluten_free'}. DEFAULT '{}' = empty array, never NULL.
    tags TEXT[] NOT NULL DEFAULT '{}',

    -- Per-100g macros, NUMERIC = exact decimal (matches USDA publishing).
    -- NUMERIC(6,2): up to 6 digits, 2 after the decimal point.
    -- Calories are STORED, not derived: USDA energy != Atwater 4/4/9 math.
    -- (The 4P+4C+9F ±25% sanity check lives in the seeder, not the schema.)
    kcal_per_100g     NUMERIC(7,2) NOT NULL CHECK (kcal_per_100g >= 0),
    protein_g_per_100g NUMERIC(6,2) NOT NULL CHECK (protein_g_per_100g BETWEEN 0 AND 100),
    carbs_g_per_100g   NUMERIC(6,2) NOT NULL CHECK (carbs_g_per_100g BETWEEN 0 AND 100),
    fat_g_per_100g     NUMERIC(6,2) NOT NULL CHECK (fat_g_per_100g BETWEEN 0 AND 100),

    -- Palatability cap for the Phase 4 MILP. I don't want to recommend a user 1 tub of whey and 10 chicken breasts 
    max_grams_per_week NUMERIC(8,1) CHECK (max_grams_per_week > 0),

    -- timestamptz = timestamp WITH time zone (stored as UTC). Always this,
    -- never plain timestamp.
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);



-- A GIN (Generalized Inverted) index.
-- Normal btree index sorts whole values; it cannot answer "which rows contain 'vegan'?"
-- GIN indexes each element, making WHERE tags @> '{vegan}' fast.
CREATE INDEX foods_tags_gin ON foods USING GIN (tags);
