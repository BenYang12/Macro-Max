// Package crypt encrypts small secrets before they go into Postgres.
//
// It exists for exactly one thing: the Kroger user refresh token. That token
// grants long-lived write access to a real person's grocery cart, and unlike
// every other value in my database it isn't mine to lose. A `pg_dump` shared
// for debugging, a backup on a laptop, a snapshot in the wrong bucket — none of
// those should hand over someone's account.
//
// SCOPE, honestly stated: this protects data AT REST, and nothing else. An
// attacker who can read the process environment already has the key, and one
// who can call my API doesn't need the token at all. That's a real limit, not
// a flaw — encryption at rest defends against a specific and very common
// failure (the database copy that escapes), and pretending it does more would
// be worse than not having it.
//
// WHY I DIDN'T ROLL MY OWN: I'm using crypto/cipher's AES-GCM directly rather
// than composing AES-CTR with an HMAC, because the composed version has half a
// dozen ways to be subtly wrong (nonce reuse, MAC-then-encrypt ordering,
// non-constant-time comparison) and AEAD has none of them. The general rule
// worth internalizing: use the highest-level primitive that does the job.
package crypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

// KeyBytes is the required key length. AES-256 — the 256 refers to key BITS,
// and 256/8 is 32 bytes.
const KeyBytes = 32

// ErrNoKey means encryption was requested with no key configured. A distinct
// sentinel so callers can say "the cart feature is disabled" rather than
// reporting a crypto failure for what is really a missing setting.
var ErrNoKey = errors.New("no encryption key configured")

// Box holds a parsed key and does the sealing.
type Box struct {
	aead cipher.AEAD
}

// NewBox parses a hex-encoded 32-byte key, as produced by:
//
//	openssl rand -hex 32
//
// Hex rather than raw bytes because the key travels through an environment
// variable and a .env file, both of which are text. Base64 would work equally
// well; hex wins on being unambiguous to eyeball and impossible to mangle with
// a stray URL-safe/standard alphabet mixup.
//
// An empty key returns ErrNoKey rather than an error about length, so the
// caller can distinguish "not configured" (fine — feature off) from
// "configured wrong" (a mistake worth shouting about).
func NewBox(hexKey string) (*Box, error) {
	if hexKey == "" {
		return nil, ErrNoKey
	}

	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("TOKEN_ENCRYPTION_KEY is not valid hex: %w", err)
	}
	if len(key) != KeyBytes {
		// The message names both the expected byte count AND the hex character
		// count, because the most likely mistake is generating 16 bytes and
		// counting the 32 hex characters as success.
		return nil, fmt.Errorf(
			"TOKEN_ENCRYPTION_KEY must be %d bytes (%d hex characters), got %d bytes; generate one with: openssl rand -hex %d",
			KeyBytes, KeyBytes*2, len(key), KeyBytes)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("building AES cipher: %w", err)
	}

	// GCM = Galois/Counter Mode, an AEAD: Authenticated Encryption with
	// Associated Data. The "authenticated" half is what makes this the right
	// choice and plain AES the wrong one — GCM appends a tag that makes
	// tampering DETECTABLE. Without it, someone with write access to the
	// database could flip bits in the ciphertext and I'd decrypt the result
	// into garbage without ever knowing it had been altered.
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("building GCM: %w", err)
	}

	return &Box{aead: aead}, nil
}

// Encrypt seals plaintext. The returned bytes are nonce || ciphertext || tag,
// safe to store directly in a BYTEA column.
func (b *Box) Encrypt(plaintext string) ([]byte, error) {
	// THE NONCE IS THE WHOLE BALLGAME in GCM. It must be unique for every
	// single encryption under a given key — not secret, but never repeated.
	// Reusing one with the same key doesn't merely weaken the encryption, it
	// catastrophically breaks it: an attacker who sees two messages sharing a
	// nonce can recover the XOR of the plaintexts and forge new messages.
	//
	// crypto/rand, never math/rand. math/rand is seeded predictably and would
	// produce a repeatable nonce sequence, which is the failure above with
	// extra steps. At 12 random bytes, a collision needs on the order of 2^48
	// encryptions to become likely — vastly more tokens than this app will ever
	// store.
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}

	// Seal's first argument is a destination to APPEND to. Passing the nonce
	// itself is the standard idiom: the output becomes nonce||ciphertext in one
	// allocation, and Open below reads it back the same way. The nonce is not
	// secret, so storing it alongside the ciphertext is correct — it just has
	// to be there, because decryption is impossible without it.
	return b.aead.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

// Decrypt opens what Encrypt sealed.
func (b *Box) Decrypt(sealed []byte) (string, error) {
	ns := b.aead.NonceSize()
	if len(sealed) < ns {
		// Guard the slice below. Without it a truncated or empty column would
		// panic with an index-out-of-range instead of returning an error, and
		// a panic in a handler takes down the request with no useful message.
		return "", errors.New("ciphertext is too short to contain a nonce")
	}

	nonce, ct := sealed[:ns], sealed[ns:]

	plaintext, err := b.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		// Open fails for two reasons that look identical from here: the data
		// was tampered with, or the key is different from the one that
		// encrypted it. In practice the second is overwhelmingly more likely —
		// somebody rotated TOKEN_ENCRYPTION_KEY — so the message names it,
		// while staying honest that both are possible.
		return "", fmt.Errorf(
			"could not decrypt: the data was tampered with, or TOKEN_ENCRYPTION_KEY changed since it was stored: %w", err)
	}
	return string(plaintext), nil
}
