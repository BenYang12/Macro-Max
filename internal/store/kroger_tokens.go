package store

// kroger_tokens.go — durable, encrypted storage for one user's cart credentials.
//
// This is the only place in the project that writes a secret to Postgres, and
// the encryption happens HERE rather than in the handler on purpose: if the
// only way to reach the column is through these two methods, then "somebody
// forgot to encrypt it" stops being a possible bug. Making the wrong thing
// impossible beats remembering to do the right thing.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenYang12/Macro-Max/internal/crypt"
)

// DefaultAccountKey is the single-user placeholder.
//
// The project has no users table by design, and adding authentication just to
// key one row would be a large detour for no benefit. This constant is the seam
// where that changes: swap it for a real user id and the schema, the queries,
// and the encryption all keep working untouched.
const DefaultAccountKey = "default"

// KrogerToken is the decrypted form. Nothing outside this file ever sees the
// ciphertext, which is the point.
type KrogerToken struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Scope        string
}

// SaveKrogerToken encrypts and upserts the token for an account.
//
// UPSERT rather than insert because re-authorizing is normal, not exceptional:
// a user can revoke access and grant it again, or the refresh token can rotate.
// An insert would fail on the UNIQUE constraint the second time, and a
// delete-then-insert would leave a window with no token at all.
func (s *Store) SaveKrogerToken(ctx context.Context, box *crypt.Box, accountKey string, t KrogerToken) error {
	if box == nil {
		return crypt.ErrNoKey
	}

	accessEnc, err := box.Encrypt(t.AccessToken)
	if err != nil {
		return fmt.Errorf("encrypting access token: %w", err)
	}
	refreshEnc, err := box.Encrypt(t.RefreshToken)
	if err != nil {
		return fmt.Errorf("encrypting refresh token: %w", err)
	}

	_, err = s.Pool.Exec(ctx, `
		INSERT INTO kroger_tokens
			(account_key, access_token_enc, refresh_token_enc, expires_at, scope)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (account_key) DO UPDATE SET
			access_token_enc  = EXCLUDED.access_token_enc,
			refresh_token_enc = EXCLUDED.refresh_token_enc,
			expires_at        = EXCLUDED.expires_at,
			scope             = EXCLUDED.scope,
			updated_at        = now()`,
		accountKey, accessEnc, refreshEnc, t.ExpiresAt, t.Scope)
	if err != nil {
		// The wrapped error deliberately says nothing about the token values.
		// A pgx error can echo parameters in some configurations, and this is
		// the one query in the project where that would be a credential leak
		// into the logs.
		return fmt.Errorf("saving kroger token: %w", err)
	}
	return nil
}

// GetKrogerToken loads and decrypts. Returns ErrNotFound when the user has
// never authorized, which the handler turns into "connect your account first"
// rather than a 500.
func (s *Store) GetKrogerToken(ctx context.Context, box *crypt.Box, accountKey string) (KrogerToken, error) {
	if box == nil {
		return KrogerToken{}, crypt.ErrNoKey
	}

	var accessEnc, refreshEnc []byte
	var t KrogerToken

	err := s.Pool.QueryRow(ctx, `
		SELECT access_token_enc, refresh_token_enc, expires_at, scope
		FROM kroger_tokens
		WHERE account_key = $1`, accountKey).Scan(
		&accessEnc, &refreshEnc, &t.ExpiresAt, &t.Scope)
	if err != nil {
		if err == pgx.ErrNoRows {
			return KrogerToken{}, ErrNotFound
		}
		return KrogerToken{}, fmt.Errorf("querying kroger token: %w", err)
	}

	if t.AccessToken, err = box.Decrypt(accessEnc); err != nil {
		return KrogerToken{}, fmt.Errorf("decrypting access token: %w", err)
	}
	if t.RefreshToken, err = box.Decrypt(refreshEnc); err != nil {
		return KrogerToken{}, fmt.Errorf("decrypting refresh token: %w", err)
	}

	return t, nil
}

// DeleteKrogerToken forgets a stored authorization.
//
// Worth having even though nothing in the happy path calls it: "let me
// disconnect this" is a reasonable thing to want, and the alternative —
// telling someone to go run SQL — is not an answer. It's also the fix for a
// token that's become undecryptable after a key rotation.
func (s *Store) DeleteKrogerToken(ctx context.Context, accountKey string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM kroger_tokens WHERE account_key = $1`, accountKey)
	if err != nil {
		return fmt.Errorf("deleting kroger token: %w", err)
	}
	return nil
}
