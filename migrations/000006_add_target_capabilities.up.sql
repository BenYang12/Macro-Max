ALTER TABLE user_targets
ADD COLUMN capability_digest BYTEA;

-- Existing anonymous rows intentionally become inaccessible: these digests
-- have no corresponding 32-byte bearer token that was ever issued.
UPDATE user_targets
SET capability_digest = sha256(convert_to(
    'macro-max:legacy-inaccessible:' || gen_random_uuid()::text || ':' || id::text,
    'UTF8'
));

ALTER TABLE user_targets
ALTER COLUMN capability_digest SET NOT NULL,
ADD CHECK (octet_length(capability_digest) = 32);
