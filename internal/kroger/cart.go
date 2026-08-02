package kroger

// cart.go — the OTHER OAuth flow, and writing to a real person's cart.
//
// token.go does CLIENT CREDENTIALS: my app authenticating as itself to read a
// public catalog. This file does AUTHORIZATION CODE, and the difference is the
// whole point:
//
//	client credentials          authorization code
//	--------------------------  ------------------------------------------
//	my app IS the identity      a HUMAN is the identity; my app acts for them
//	no browser involved         the user must visit Kroger and click Approve
//	token = 30 min, re-fetch    access ~30 min + a refresh token lasting months
//	reads public data           writes to their account
//
// That last row is why everything here is more careful than token.go: a leaked
// client-credentials token exposes grocery prices, which are on a shelf in
// public. A leaked refresh token lets someone else fill a stranger's cart.
//
// THE SHAPE OF THE FLOW, since the terminology hides how simple it is:
//
//  1. I send the user to Kroger's authorize URL with my client id, the scopes
//     I want, my redirect URI, and a random `state`.
//  2. They log in and approve.
//  3. Kroger redirects the browser to my redirect URI with ?code=...&state=...
//  4. I check the state matches, then POST the code to the token endpoint and
//     get back an access token + a refresh token.
//  5. From then on I use the access token, and swap the refresh token for a new
//     access token whenever it expires.
//
// The `code` is single-use and short-lived precisely because it travels through
// a browser URL — visible in history, in Referer headers, and in server logs.
// It's a claim ticket, not a credential.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultAuthorizeURL is where the USER goes (in a browser), as opposed to
// defaultTokenURL which is where MY SERVER goes. Confusing these is a classic
// OAuth mistake: the authorize endpoint returns an HTML login page, so posting
// to it yields a baffling "invalid JSON" error.
const defaultAuthorizeURL = "https://api.kroger.com/v1/connect/oauth2/authorize"

// ScopeCartWrite is the permission to add items to the signed-in user's cart.
//
// It is requested ONLY here. Phase 5's ingestion asks for product.compact and
// nothing else, which means a compromised ingestion token cannot touch a cart —
// least privilege, enforced by asking for less rather than by hoping.
const ScopeCartWrite = "cart.basic:write"

// UserToken is one user's granted credentials.
type UserToken struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Scope        string
}

// Valid reports whether the access token can still be used, with the same
// 30-second margin token.go uses — and for the same reason: a token that passes
// the check at 29:59 can still be dead by the time the request lands.
func (t UserToken) Valid() bool {
	return t.AccessToken != "" && time.Now().Before(t.ExpiresAt.Add(-refreshMargin))
}

// NewState generates the CSRF token for the authorize redirect.
//
// WHAT `state` ACTUALLY DEFENDS AGAINST, because "CSRF token" makes it sound
// like boilerplate: without it, an attacker can complete step 1 themselves,
// obtain a `code` for THEIR Kroger account, and then trick you into visiting
//
//	https://myapp.example/v1/kroger/callback?code=<attacker's code>
//
// My server would dutifully exchange it and store the attacker's token against
// your session. You'd then add groceries to their cart — and if they'd granted
// a broader scope, hand them whatever else flows through that connection.
//
// The defense: generate a random value, remember it, and refuse any callback
// whose state doesn't match. The attacker can't know the value, so they can't
// forge a callback that survives the check.
//
// crypto/rand, not math/rand — a predictable "random" value defeats the entire
// mechanism. URL-safe base64 because this goes in a query string, where the
// standard alphabet's + and / would need escaping.
func NewState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// AuthorizeURL builds the URL to send the user's BROWSER to.
//
// Note that nothing secret appears here. The client id is public by design;
// the client SECRET is used only in the back-channel token exchange, which
// happens server to server and never touches the browser. That split is the
// core security property of the authorization-code flow.
func (c *Client) AuthorizeURL(redirectURI, state string) string {
	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", c.tokens.clientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("scope", ScopeCartWrite)
	params.Set("state", state)

	return c.AuthorizeBaseURL + "?" + params.Encode()
}

// ExchangeCode trades the single-use authorization code for real tokens.
//
// This is the back channel: server to server, over TLS, authenticated with the
// client secret. The code alone is not enough to get a token, which is what
// makes it safe for the code to have travelled through the user's browser.
func (c *Client) ExchangeCode(ctx context.Context, code, redirectURI string) (UserToken, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	// redirect_uri is sent AGAIN even though Kroger already knows it. That's
	// required by the OAuth spec, not redundancy: the server compares it to the
	// one from step 1, which stops an attacker who stole a code from redeeming
	// it against a different (attacker-controlled) redirect.
	form.Set("redirect_uri", redirectURI)

	return c.postTokenForm(ctx, form)
}

// RefreshUserToken swaps a refresh token for a fresh access token.
//
// A subtlety worth knowing: some providers ROTATE the refresh token, returning
// a new one that invalidates the old. Kroger's response may or may not include
// one, so postTokenForm carries the old value forward when the field is absent
// — dropping it would silently log the user out weeks later, which is a
// miserable bug to trace back to this line.
func (c *Client) RefreshUserToken(ctx context.Context, refreshToken string) (UserToken, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)

	tok, err := c.postTokenForm(ctx, form)
	if err != nil {
		return UserToken{}, err
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = refreshToken
	}
	return tok, nil
}

// userTokenResponse mirrors the token endpoint's reply for these two grants.
// Same endpoint as client credentials, two extra fields.
type userTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

// postTokenForm is the shared exchange. Both grants POST a form to the same
// endpoint with the same Basic auth, differing only in the fields — so the
// plumbing lives once.
func (c *Client) postTokenForm(ctx context.Context, form url.Values) (UserToken, error) {
	credentials := base64.StdEncoding.EncodeToString(
		[]byte(c.tokens.clientID + ":" + c.tokens.clientSecret))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokens.tokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return UserToken{}, fmt.Errorf("building token request: %w", err)
	}
	req.Header.Set("Authorization", "Basic "+credentials)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return UserToken{}, fmt.Errorf("requesting user token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		// 400 from this endpoint almost always means the code expired (they're
		// good for minutes) or was already redeemed (single-use). Both are
		// recoverable by starting the flow over, and saying so beats making
		// someone decode Kroger's generic invalid_grant.
		if resp.StatusCode == http.StatusBadRequest {
			return UserToken{}, fmt.Errorf(
				"kroger rejected the grant (400): the code likely expired or was already used — start the authorization again. Response: %s", body)
		}
		return UserToken{}, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, body)
	}

	var tr userTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return UserToken{}, fmt.Errorf("decoding user token response: %w", err)
	}
	if tr.AccessToken == "" {
		return UserToken{}, fmt.Errorf("token endpoint returned an empty access_token")
	}

	return UserToken{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
		Scope:        tr.Scope,
	}, nil
}

// ------------------------------------------------------------------- the cart

// CartItem is one line to add. Kroger identifies products by UPC here, not by
// the productId that search returns — a real inconsistency in their API and
// the reason store.BasketLine carries ExternalID all the way from the database.
type CartItem struct {
	UPC      string `json:"upc"`
	Quantity int    `json:"quantity"`
	// Modality is PICKUP or DELIVERY. Kroger requires it, and the default in
	// their docs is PICKUP, which is also the correct one for this project: the
	// whole premise is a store you walk to.
	Modality string `json:"modality"`
}

// AddToCart adds items to the signed-in user's cart.
//
// PUT, not POST, and it is ADDITIVE rather than idempotent — calling it twice
// with the same body results in double the quantity, not the same cart. That
// combination is unusual enough to be a trap, and it's why the handler warns
// about it rather than offering a retry button.
func (c *Client) AddToCart(ctx context.Context, accessToken string, items []CartItem) error {
	if len(items) == 0 {
		return fmt.Errorf("no items to add")
	}

	body, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		return fmt.Errorf("encoding cart items: %w", err)
	}

	// Rate limiting applies here too. It's one request rather than 42, but the
	// limiter is on the client and skipping it for "just this one" is how a
	// consistent invariant becomes an inconsistent one.
	c.waitForSlot(ctx)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		c.BaseURL+"/cart/add", strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("building cart request: %w", err)
	}
	// The USER's token, not the app's. Passing it in as an argument rather than
	// reading it from the client is deliberate: this Client is shared across
	// requests, and stashing a user credential on it would be exactly the kind
	// of shared mutable state that leaks one user's token into another's call.
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("adding to cart: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// 204 No Content is the documented success. Accepting the whole 2xx range
	// is the tolerant reading — I care that it worked, not which flavor of
	// success they chose to send.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		// The access token expired or was revoked. Distinguished from 403
		// because the fixes are different: this one is a refresh, that one is a
		// re-authorization.
		return fmt.Errorf("kroger rejected the token (401): it expired or was revoked")
	case http.StatusForbidden:
		return fmt.Errorf(
			"kroger returned 403: the token lacks the %s scope — reauthorize at /v1/kroger/authorize", ScopeCartWrite)
	default:
		return fmt.Errorf("cart add returned %d: %s", resp.StatusCode, msg)
	}
}
