-- user_targets: what the user wants — daily macros, weekly budget, store, etc.
-- No auth yet, so rows are distinguished by a human label like 'cutting' or 'bulk'.


CREATE TABLE user_targets (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    label TEXT NOT NULL,

    -- Most macro targets are measured in Whole Grams 
    protein_g_daily INT NOT NULL CHECK (protein_g_daily >= 0),
    carbs_g_daily   INT NOT NULL CHECK (carbs_g_daily >= 0),
    fat_g_daily     INT NOT NULL CHECK (fat_g_daily >= 0),

    -- NULL = no calorie ceiling (macros-only mode).
    calories_max_daily INT CHECK (calories_max_daily > 0),

    -- Money = integer cents, weekly period since I'm helping users budget food for a week
    budget_cents_weekly INT NOT NULL CHECK (budget_cents_weekly > 0),

    -- Which store's prices to solve against ('SEED' until Phase 5).
    store_id TEXT NOT NULL,

    -- Filters applied when selecting candidate foods for the solve:
    -- foods must carry every tag in diet_tags, and never be in exclude list.
    diet_tags        TEXT[]   NOT NULL DEFAULT '{}',
    exclude_food_ids BIGINT[] NOT NULL DEFAULT '{}',

    created_at timestamptz NOT NULL DEFAULT now()
);
