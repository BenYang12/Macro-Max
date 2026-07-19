-- baskets: one solver run's result. basket_items: its line items.
-- Two tables, one migration: they're a single concept (a result and its
-- lines), created and dropped together.

CREATE TABLE baskets (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    target_id BIGINT NOT NULL REFERENCES user_targets(id),

    -- Denormalized copy of the store solved against (targets can be edited
    -- later; the basket records what was actually used).
    store_id TEXT NOT NULL,

    -- sha256 cache key of the solve request + a prices fingerprint (Phase 4).
    -- Indexed so "have we already solved exactly this?" is one lookup.
    solve_key TEXT NOT NULL,

    -- Solver outcome. INFEASIBLE is a first-class result, not an error:
    -- "your macros can't be met at this budget" is the product's best insight.
    status TEXT NOT NULL CHECK (status IN
        ('optimal','feasible','infeasible','error')),

    total_cost_cents INT NOT NULL DEFAULT 0 CHECK (total_cost_cents >= 0),

    -- JSONB: schemaless blob for solver diagnostics (solve time, gap, etc.).
    -- Fine for debug data nothing queries relationally.
    solver_stats JSONB,

    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX baskets_solve_key_idx ON baskets (solve_key);

CREATE TABLE basket_items (
    -- Cascade: delete a basket, its lines go too — lines without their
    -- basket are meaningless.
    basket_id  BIGINT NOT NULL REFERENCES baskets(id) ON DELETE CASCADE,
    product_id BIGINT NOT NULL REFERENCES products(id),

    -- Whole packs BOUGHT (integer — you can't buy 1.4 bags of rice)...
    packs INT NOT NULL CHECK (packs > 0),

    -- ...vs grams actually CONSUMED by the plan. grams <= packs * net_weight_g;
    -- the difference is leftover. This gap is the entire point of the
    -- Phase 4 MILP.
    grams NUMERIC(8,1) NOT NULL CHECK (grams >= 0),

    cost_cents INT NOT NULL CHECK (cost_cents >= 0),

    -- COMPOSITE primary key: no id column. "This product in this basket" IS
    -- the identity, and a duplicate line would be a bug — the PK enforces that.
    PRIMARY KEY (basket_id, product_id)
);
