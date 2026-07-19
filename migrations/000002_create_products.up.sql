-- products: a purchasable item — one pack size of one food at one store.
-- Phase 1 uses fake products (store_id = 'SEED'); Phase 5 adds real Kroger.

CREATE TABLE products (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    -- REFERENCES = foreign key: food_id must be a real foods.id.
    -- Now, if I insert a product with food_id = 999, when no such food exists, an error occurs
    -- Also, deleting a food that still has products is refused
    food_id BIGINT NOT NULL REFERENCES foods(id),

    -- Kroger locationId, or the literal 'SEED' for our fake dev products.
    store_id TEXT NOT NULL,

    -- The store's own id for this product (Kroger productId).
    external_id TEXT NOT NULL,

    name  TEXT NOT NULL,
    brand TEXT,

    -- Raw label size, kept for display/debugging: "2.5" + "lb".
    pack_size_qty  NUMERIC(8,2) NOT NULL CHECK (pack_size_qty > 0),
    pack_size_unit TEXT NOT NULL CHECK (pack_size_unit IN
        ('g','kg','oz','lb','ml','l','fl_oz','each','dozen')),

    -- The single reconciled truth. "2.5 lb" -> 1133.98. "2.5lb", "59 fl oz", are all converted to grams
    -- exactly once, at ingestion, by one Go parser I will implement later in phase 5
    -- Every downstream consumer - solver, API, frontend, will read this one column and NEVER convert units again
    net_weight_g NUMERIC(8,1) NOT NULL CHECK (net_weight_g > 0),

    -- Money = integer cents, project-wide law. Effective price is
    -- COALESCE(promo_price_cents, price_cents), computed in queries.
    price_cents       INT NOT NULL CHECK (price_cents >= 0),
    promo_price_cents INT CHECK (promo_price_cents >= 0),

    -- Vanished SKUs get available=false, never deleted
    available BOOLEAN NOT NULL DEFAULT TRUE,

    -- When we last fetched this price from the store.
    fetched_at timestamptz NOT NULL DEFAULT now(),

    -- Composite UNIQUE: "this store's catalog entry". This is the upsert key
    -- for Phase 5 ingestion: ON CONFLICT (store_id, external_id) DO UPDATE.
    UNIQUE (store_id, external_id)
);

-- The solver's exact fetch pattern: "all products at store X for foods
-- in my candidate set" — so index (store_id, food_id) in that order.
CREATE INDEX products_store_food_idx ON products (store_id, food_id);
