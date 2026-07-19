-- price_history: append-only log of price changes per product.
-- Row is written only when a price actually changes (enforced in phase 5 Go code, not here)

CREATE TABLE price_history (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

   
    -- ON DELETE CASCADE is a clause used during foreign key configuration to automatically remove matching records from a child table when a corresponding record in the parent table is deleted
    -- History about a nonexistent product is garbage, so I decide to cascade here
    product_id BIGINT NOT NULL REFERENCES products(id) ON DELETE CASCADE,

    price_cents       INT NOT NULL CHECK (price_cents >= 0),
    promo_price_cents INT CHECK (promo_price_cents >= 0),

    recorded_at timestamptz NOT NULL DEFAULT now()
);

-- Composite index matching read direction (a primary key or unique key that consists of two or more columns to uniquely identify a row in a table.)
-- (product_id, recorded_at DESC) serves "latest price for product X"
-- and "price timeline for product X" — the only two queries this table gets.
-- DESC matches the read direction: newest first.
CREATE INDEX price_history_product_time_idx
    ON price_history (product_id, recorded_at DESC);
