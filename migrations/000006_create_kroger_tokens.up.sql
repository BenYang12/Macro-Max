-- kroger_tokens: the OAuth credentials for writing to a real person's cart.
--
-- WHY THIS TABLE EXISTS AT ALL, given that Phase 5 already talks to Kroger
-- without one: these are different credentials for a different flow. Phase 5
-- uses CLIENT CREDENTIALS — my app authenticating as itself to read a public
-- product catalog — and those tokens live in Redis because losing one costs a
-- single round trip to get another.
--
-- This is the AUTHORIZATION CODE flow: a real human logged into Kroger and
-- granted my app write access to their cart. Losing that token means sending
-- them back through a browser consent screen, so it belongs in durable storage,
-- not a cache that evicts under memory pressure.

CREATE TABLE kroger_tokens (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    -- There is no users table — the project is single-user by design, and
    -- inventing authentication to store one row would be the wrong trade.
    -- account_key is the seam for when that changes: today every row is
    -- 'default', and the day real accounts exist this becomes a user id with
    -- no schema change. UNIQUE makes the upsert in kroger_tokens.go trivial
    -- and makes "two rows for one account" unrepresentable.
    account_key TEXT NOT NULL UNIQUE,

    -- BYTEA, not TEXT, and _enc because the contents are AES-GCM ciphertext,
    -- not tokens. See internal/crypt: a database dump of this table is useless
    -- without TOKEN_ENCRYPTION_KEY, which lives only in the environment.
    --
    -- This is the one table in the project holding a credential that belongs to
    -- a PERSON rather than to my app, and that difference is what earns it
    -- encryption at rest when nothing else here has it.
    access_token_enc  BYTEA NOT NULL,
    refresh_token_enc BYTEA NOT NULL,

    -- When the ACCESS token dies (typically 30 minutes). The refresh token
    -- outlives it by months, which is exactly why the refresh token is the one
    -- worth stealing and the reason both are encrypted rather than just one.
    expires_at timestamptz NOT NULL,

    -- What the user actually granted. Recorded rather than assumed: if Kroger
    -- narrows a scope, or the app is reconfigured to ask for less, I want the
    -- failure to be a readable "this token lacks cart.basic:write" instead of
    -- a bare 403 from a live request.
    scope TEXT NOT NULL DEFAULT '',

    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
