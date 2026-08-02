package store

// kroger_tokens_integration_test.go — the round trip through real Postgres for
// the one table that stores an encrypted secret.
//
// The unit tests in internal/crypt already prove the encryption is correct in
// isolation. What they CAN'T prove is that the ciphertext survives a trip
// through a BYTEA column unmangled — and that's exactly the kind of thing that
// breaks quietly. A driver that treated the bytes as a UTF-8 string would
// corrupt them in a way that only shows up as a decryption failure, weeks
// later, on the one request that mattered.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BenYang12/Macro-Max/internal/crypt"
)

func testBox(t *testing.T) *crypt.Box {
	t.Helper()
	box, err := crypt.NewBox(strings.Repeat("7f", crypt.KeyBytes))
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}
	return box
}

func TestKrogerToken_RoundTrip(t *testing.T) {
	st := newTestStore(t)
	box := testBox(t)
	ctx := context.Background()

	const key = "__test_account__"
	t.Cleanup(func() {
		_ = st.DeleteKrogerToken(context.Background(), key)
	})

	// Truncated to the second: Postgres timestamptz has microsecond precision
	// and Go's time.Time has nanoseconds, so comparing the raw values would
	// fail on a rounding difference that means nothing.
	expires := time.Now().Add(30 * time.Minute).Truncate(time.Second)

	want := KrogerToken{
		AccessToken:  "access-token-value-12345",
		RefreshToken: "refresh-token-value-67890",
		ExpiresAt:    expires,
		Scope:        "cart.basic:write",
	}

	if err := st.SaveKrogerToken(ctx, box, key, want); err != nil {
		t.Fatalf("SaveKrogerToken: %v", err)
	}

	got, err := st.GetKrogerToken(ctx, box, key)
	if err != nil {
		t.Fatalf("GetKrogerToken: %v", err)
	}

	if got.AccessToken != want.AccessToken {
		t.Errorf("access token = %q, want %q", got.AccessToken, want.AccessToken)
	}
	if got.RefreshToken != want.RefreshToken {
		t.Errorf("refresh token = %q, want %q", got.RefreshToken, want.RefreshToken)
	}
	if got.Scope != want.Scope {
		t.Errorf("scope = %q, want %q", got.Scope, want.Scope)
	}
	// .Equal, not ==. Two time.Time values can represent the same instant with
	// different monotonic clock readings or locations, and == compares the
	// struct fields rather than the instant.
	if !got.ExpiresAt.Truncate(time.Second).Equal(expires) {
		t.Errorf("expires_at = %v, want %v", got.ExpiresAt, expires)
	}
}

func TestKrogerToken_StoredBytesAreNotPlaintext(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	const key = "__test_plaintext_check__"
	const secret = "super-secret-refresh-token"
	t.Cleanup(func() {
		_ = st.DeleteKrogerToken(context.Background(), key)
	})

	err := st.SaveKrogerToken(ctx, testBox(t), key, KrogerToken{
		AccessToken:  "a",
		RefreshToken: secret,
		ExpiresAt:    time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Read the RAW COLUMN, bypassing GetKrogerToken entirely. This is the
	// assertion the whole table exists for: someone with a database dump must
	// not be able to read the token. Going through the normal accessor would
	// decrypt it and prove nothing.
	var raw []byte
	if err := st.Pool.QueryRow(ctx,
		`SELECT refresh_token_enc FROM kroger_tokens WHERE account_key = $1`, key).Scan(&raw); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(raw), secret) {
		t.Fatal("the refresh token is readable in the database — it was stored in plaintext")
	}
	if len(raw) == 0 {
		t.Fatal("stored ciphertext is empty")
	}
}

func TestKrogerToken_SaveIsAnUpsert(t *testing.T) {
	st := newTestStore(t)
	box := testBox(t)
	ctx := context.Background()

	const key = "__test_upsert__"
	t.Cleanup(func() {
		_ = st.DeleteKrogerToken(context.Background(), key)
	})

	base := KrogerToken{AccessToken: "first", RefreshToken: "r1", ExpiresAt: time.Now().Add(time.Hour)}
	if err := st.SaveKrogerToken(ctx, box, key, base); err != nil {
		t.Fatal(err)
	}

	// Re-authorizing is NORMAL, not exceptional — a user can revoke and grant
	// again, and refresh tokens can rotate. A plain INSERT would fail on the
	// UNIQUE constraint the second time, which is the bug this pins down.
	base.AccessToken, base.RefreshToken = "second", "r2"
	if err := st.SaveKrogerToken(ctx, box, key, base); err != nil {
		t.Fatalf("second save should upsert, not fail: %v", err)
	}

	got, err := st.GetKrogerToken(ctx, box, key)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "second" || got.RefreshToken != "r2" {
		t.Errorf("upsert did not overwrite: %+v", got)
	}

	// Still exactly one row. An upsert that quietly inserted a duplicate would
	// pass the read above (the SELECT would just pick one) and slowly rot.
	var n int
	if err := st.Pool.QueryRow(ctx,
		`SELECT count(*) FROM kroger_tokens WHERE account_key = $1`, key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("row count = %d, want 1", n)
	}
}

func TestKrogerToken_MissingIsErrNotFound(t *testing.T) {
	st := newTestStore(t)

	_, err := st.GetKrogerToken(context.Background(), testBox(t), "__no_such_account__")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestKrogerToken_WrongKeyFailsToDecrypt(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	const key = "__test_key_rotation__"
	t.Cleanup(func() {
		_ = st.DeleteKrogerToken(context.Background(), key)
	})

	if err := st.SaveKrogerToken(ctx, testBox(t), key, KrogerToken{
		AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	// The realistic incident: somebody regenerated TOKEN_ENCRYPTION_KEY. This
	// must be a clear error, not silent garbage — an AEAD guarantees that, and
	// this test is what proves the guarantee survives the storage layer.
	otherBox, err := crypt.NewBox(strings.Repeat("11", crypt.KeyBytes))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetKrogerToken(ctx, otherBox, key); err == nil {
		t.Fatal("expected decryption under a rotated key to fail")
	}
}
