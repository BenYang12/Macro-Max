package kroger

// token.go — the OAuth client-credentials token manager.
//
// Kroger doesn't take an API key on each request the way USDA does. Instead I
// exchange my client id and secret for a short-lived ACCESS TOKEN (~30 minutes),
// then send that token as a bearer credential. When it expires I exchange again.
//
// This is the "client credentials" OAuth flow, and the thing that makes it the
// SIMPLE one is that there's no user involved: my program is authenticating as
// itself, not on someone's behalf. Phase 7's add-to-cart is the other kind —
// authorization code flow, where a real human logs in and grants access — and
// it's substantially more involved. Good to know now that these are two
// different things wearing the same word.
//
// THE CONCURRENCY PROBLEM this file solves:
// My ingestion runs a worker pool, so several goroutines will want a token at
// the same moment. Without coordination they'd each notice the token is expired
// and each fire off a refresh — a "thundering herd" that wastes quota and can
// trip rate limits. A mutex means exactly one refresh happens and everyone else
// waits for it.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// tokenEndpoint is the OAuth token URL. Separate from the API base URL because
// they're genuinely different services and tests override them independently.
const defaultTokenURL = "https://api.kroger.com/v1/connect/oauth2/token"

// The scope I need for Phase 5: product search and location lookup. Explicitly
// NOT cart.basic:write — that one requires a logged-in user and belongs to
// Phase 7. Asking only for what I need is the least-privilege habit, and it
// also means a leaked token from this flow cannot touch anyone's cart.
const scopeProductCompact = "product.compact"

// refreshMargin is how early I treat a token as expired.
//
// Without it there's a race I cannot win: I check the token at 29:59, decide
// it's valid, and by the time my request lands it's 30:01 and the token is
// dead. Refreshing 30 seconds early makes that window impossible. This is the
// same reasoning as any clock-skew buffer — the cost is one extra refresh per
// hour, and the benefit is never serving a request with a stale credential.
const refreshMargin = 30 * time.Second

// TokenStore is optional persistence for the token across process restarts.
// An interface rather than a *redis.Client so this package doesn't depend on
// Redis at all, and so tests can supply a map.
type TokenStore interface {
	GetToken(ctx context.Context) (token string, expiresAt time.Time, ok bool)
	SetToken(ctx context.Context, token string, expiresAt time.Time)
}

// tokenManager holds the current token and refreshes it on demand.
type tokenManager struct {
	clientID     string
	clientSecret string
	tokenURL     string
	http         *http.Client
	store        TokenStore // may be nil

	// mu guards the two fields below. This is my first real use of a mutex in
	// this project, and the rule is the one every Go programmer learns: the
	// mutex and the data it protects live together, and NOTHING touches that
	// data without holding it.
	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// tokenResponse mirrors Kroger's OAuth reply.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"` // seconds
}

// Token returns a valid access token, refreshing if necessary.
func (t *tokenManager) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Holding the lock across the whole function — including the network call —
	// is deliberate. The alternative (unlock, fetch, relock) lets several
	// goroutines fetch at once, which is exactly the herd I'm preventing.
	// Serializing here costs one goroutine's wait every ~30 minutes and saves
	// N-1 wasted token requests each time.

	if t.token != "" && time.Now().Before(t.expiresAt.Add(-refreshMargin)) {
		return t.token, nil
	}

	// Second chance before hitting the network: another PROCESS may have
	// refreshed recently and left the token in Redis. This is why the store
	// exists — a CLI that runs every few minutes shouldn't re-authenticate
	// every single time.
	if t.store != nil {
		if tok, exp, ok := t.store.GetToken(ctx); ok &&
			tok != "" && time.Now().Before(exp.Add(-refreshMargin)) {
			t.token, t.expiresAt = tok, exp
			return tok, nil
		}
	}

	if err := t.refresh(ctx); err != nil {
		return "", err
	}
	return t.token, nil
}

// refresh performs the client-credentials exchange. Caller must hold t.mu.
func (t *tokenManager) refresh(ctx context.Context) error {
	// The credentials go in an HTTP Basic auth header as
	// base64(client_id:client_secret) — NOT in the body. That's what the OAuth
	// spec calls client_secret_basic, and it's what Kroger expects.
	credentials := base64.StdEncoding.EncodeToString(
		[]byte(t.clientID + ":" + t.clientSecret))

	// The body is form-encoded, not JSON. OAuth predates JSON-everywhere and
	// the token endpoint still speaks application/x-www-form-urlencoded.
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("scope", scopeProductCompact)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.tokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("building token request: %w", err)
	}
	req.Header.Set("Authorization", "Basic "+credentials)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.http.Do(req)
	if err != nil {
		return fmt.Errorf("requesting token: %w", err)
	}
	// The _ = is for errcheck: closing a response body can technically fail,
	// and there is nothing useful to do about it inside a defer. Being explicit
	// beats an exclusion rule that would also hide closes I DO care about.
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		// 401 here means the credentials themselves are wrong, which is a
		// completely different problem from a 401 on an API call (expired
		// token). Saying so saves real debugging time.
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf(
				"kroger rejected my credentials (401): check KROGER_CLIENT_ID and KROGER_CLIENT_SECRET")
		}
		return fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, body)
	}

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return fmt.Errorf("decoding token response: %w", err)
	}
	if tr.AccessToken == "" {
		return fmt.Errorf("token endpoint returned an empty access_token")
	}

	// expires_in is a DURATION in seconds, not a timestamp. Converting to an
	// absolute time here means every later comparison is a simple Before().
	t.token = tr.AccessToken
	t.expiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)

	if t.store != nil {
		t.store.SetToken(ctx, t.token, t.expiresAt)
	}
	return nil
}
