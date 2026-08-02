package crypt

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// A fixed valid key. Hard-coded in a test file is fine and is NOT a leaked
// secret: it protects nothing, and a test that generates its own key each run
// can't assert on the cross-key failure below.
const testKey = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func newTestBox(t *testing.T) *Box {
	t.Helper()
	b, err := NewBox(testKey)
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}
	return b
}

func TestRoundTrip(t *testing.T) {
	b := newTestBox(t)

	const secret = "kroger-refresh-token-abc123"
	sealed, err := b.Encrypt(secret)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// The obvious-but-essential assertion: the plaintext must not survive into
	// the stored bytes. An "encryption" that leaves the secret readable in a
	// hexdump would pass a naive round-trip test and fail at its only job.
	if strings.Contains(string(sealed), secret) {
		t.Fatal("plaintext is visible in the ciphertext")
	}

	got, err := b.Decrypt(sealed)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if got != secret {
		t.Errorf("round trip = %q, want %q", got, secret)
	}
}

func TestEncryptUsesAFreshNonceEveryTime(t *testing.T) {
	b := newTestBox(t)

	// Encrypting the SAME plaintext twice must produce different bytes. If it
	// doesn't, the nonce is being reused — the one mistake that turns GCM from
	// strong into catastrophically broken. This test is the reason the nonce
	// comes from crypto/rand on every call rather than being stored on the Box.
	a, err := b.Encrypt("same input")
	if err != nil {
		t.Fatal(err)
	}
	c, err := b.Encrypt("same input")
	if err != nil {
		t.Fatal(err)
	}

	if string(a) == string(c) {
		t.Fatal("two encryptions of the same plaintext produced identical ciphertext — the nonce is being reused")
	}

	// Both must still decrypt correctly, or "different every time" would be
	// satisfied by simply being broken.
	for _, sealed := range [][]byte{a, c} {
		got, err := b.Decrypt(sealed)
		if err != nil || got != "same input" {
			t.Errorf("Decrypt = %q, %v; want %q, nil", got, err, "same input")
		}
	}
}

func TestDecryptDetectsTampering(t *testing.T) {
	b := newTestBox(t)

	sealed, err := b.Encrypt("kroger-refresh-token-abc123")
	if err != nil {
		t.Fatal(err)
	}

	// Flip one bit in the last byte — inside the authentication tag. This is
	// what AEAD buys over plain AES: with unauthenticated encryption this would
	// decrypt to slightly-wrong bytes and nobody would notice.
	tampered := make([]byte, len(sealed))
	copy(tampered, sealed)
	tampered[len(tampered)-1] ^= 0x01

	if _, err := b.Decrypt(tampered); err == nil {
		t.Fatal("expected tampered ciphertext to be rejected")
	}
}

func TestDecryptFailsUnderADifferentKey(t *testing.T) {
	sealed, err := newTestBox(t).Encrypt("kroger-refresh-token-abc123")
	if err != nil {
		t.Fatal(err)
	}

	other, err := NewBox(strings.Repeat("ab", KeyBytes))
	if err != nil {
		t.Fatal(err)
	}

	// The realistic production incident: somebody regenerated
	// TOKEN_ENCRYPTION_KEY. The error message needs to name that cause, because
	// "authentication failed" alone sends you hunting for an attacker when the
	// answer is a changed env var.
	_, err = other.Decrypt(sealed)
	if err == nil {
		t.Fatal("expected decryption under a different key to fail")
	}
	if !strings.Contains(err.Error(), "TOKEN_ENCRYPTION_KEY") {
		t.Errorf("error should name the likely cause; got: %v", err)
	}
}

func TestDecryptRejectsShortInput(t *testing.T) {
	// A truncated or empty BYTEA column. Must be an error, not a panic — a
	// panic inside a handler kills the request with no usable message.
	if _, err := newTestBox(t).Decrypt([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected an error for input shorter than a nonce")
	}
}

func TestNewBox_EmptyKeyIsErrNoKey(t *testing.T) {
	// Distinct sentinel, because "not configured" and "configured wrong" want
	// different responses: the first disables a feature, the second is a
	// mistake worth failing loudly over.
	_, err := NewBox("")
	if !errors.Is(err, ErrNoKey) {
		t.Fatalf("err = %v, want ErrNoKey", err)
	}
}

func TestNewBox_RejectsBadKeys(t *testing.T) {
	tests := map[string]string{
		"not hex":       "zzzz",
		"too short":     hex.EncodeToString(make([]byte, 16)),
		"too long":      hex.EncodeToString(make([]byte, 64)),
		"odd hex chars": "abc",
	}
	for name, key := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := NewBox(key); err == nil {
				t.Errorf("expected an error for a %s key", name)
			}
		})
	}
}
