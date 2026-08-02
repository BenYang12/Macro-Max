package handler

// cart.go — the three endpoints that put a solved basket into a real cart.
//
//	GET  /v1/kroger/authorize   send the user to Kroger to grant access
//	GET  /v1/kroger/callback    Kroger sends them back here with a code
//	POST /v1/kroger/cart        add the latest basket to their cart
//
// The first two are pure OAuth plumbing; the third is the one that does
// something. Splitting them this way means the interesting endpoint contains no
// OAuth at all — it asks for a valid token and gets one, or gets told to go
// authorize. That separation is worth more than the extra route.
//
// THE HONEST CAVEAT, stated here because it shapes the design below: Kroger's
// cart API is write-only and additive. There is no way to read the cart back,
// no way to remove what I added, and calling add twice doubles the quantities.
// So this endpoint cannot be idempotent no matter how I write it, and the
// response says so rather than pretending otherwise.

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/BenYang12/Macro-Max/internal/crypt"
	"github.com/BenYang12/Macro-Max/internal/kroger"
	"github.com/BenYang12/Macro-Max/internal/store"
)

// CartStore is this handler's slice of the database.
type CartStore interface {
	LatestBasketForTarget(ctx context.Context, targetID int64) (store.Basket, []store.BasketLine, error)
	GetKrogerToken(ctx context.Context, box *crypt.Box, accountKey string) (store.KrogerToken, error)
	SaveKrogerToken(ctx context.Context, box *crypt.Box, accountKey string, t store.KrogerToken) error
}

// CartClient is the Kroger dependency behind an interface, so the tests below
// never open a socket or need a developer account.
type CartClient interface {
	AuthorizeURL(redirectURI, state string) string
	ExchangeCode(ctx context.Context, code, redirectURI string) (kroger.UserToken, error)
	RefreshUserToken(ctx context.Context, refreshToken string) (kroger.UserToken, error)
	AddToCart(ctx context.Context, accessToken string, items []kroger.CartItem) error
}

type CartHandler struct {
	Store       CartStore
	Kroger      CartClient
	Box         *crypt.Box
	RedirectURI string

	// PENDING STATE, in memory.
	//
	// The `state` parameter has to survive between the authorize redirect and
	// the callback, and a map is honest for a single-user, single-process app.
	// It does mean a server restart mid-flow invalidates an in-progress
	// authorization — which is a five-second annoyance (click the link again),
	// not data loss, and it is the correct trade against a Redis round trip and
	// another moving part.
	//
	// The mutex is NOT optional even here. Two browser tabs are enough to race
	// this, and a concurrent map write in Go is not a subtle corruption — the
	// runtime detects it and kills the process.
	mu      sync.Mutex
	pending map[string]time.Time
}

func NewCartHandler(s CartStore, k CartClient, box *crypt.Box, redirectURI string) *CartHandler {
	return &CartHandler{
		Store:       s,
		Kroger:      k,
		Box:         box,
		RedirectURI: redirectURI,
		pending:     make(map[string]time.Time),
	}
}

// stateTTL bounds how long an authorization may sit half-finished. Long enough
// to log in and read a consent screen, short enough that an abandoned attempt
// doesn't linger as a valid credential.
const stateTTL = 10 * time.Minute

// Authorize handles GET /v1/kroger/authorize by redirecting to Kroger.
func (h *CartHandler) Authorize(w http.ResponseWriter, r *http.Request) {
	state, err := kroger.NewState()
	if err != nil {
		serverErrorResponse(w, err)
		return
	}

	h.mu.Lock()
	// Sweep expired entries on the way past. A background goroutine would be
	// the "proper" answer, but this map gains an entry only when a human clicks
	// a link — opportunistic cleanup at the one place entries are created is
	// exactly proportionate, and it can't leak a goroutine.
	now := time.Now()
	for s, t := range h.pending {
		if now.Sub(t) > stateTTL {
			delete(h.pending, s)
		}
	}
	h.pending[state] = now
	h.mu.Unlock()

	// 302, not JSON. This endpoint is meant to be opened in a browser — it's
	// the one route in the API a human visits directly rather than a client
	// calling it — so the useful response is a redirect they can follow.
	http.Redirect(w, r, h.Kroger.AuthorizeURL(h.RedirectURI, state), http.StatusFound)
}

// Callback handles GET /v1/kroger/callback, where Kroger returns the user.
func (h *CartHandler) Callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	// Kroger reports a declined consent as ?error=access_denied rather than by
	// omitting the code. Checking it first means "I clicked Deny" produces a
	// message saying so, instead of a confusing "missing code".
	if e := q.Get("error"); e != "" {
		errorResponse(w, http.StatusBadRequest, "authorization_denied",
			"Kroger did not grant access: "+e)
		return
	}

	code, state := q.Get("code"), q.Get("state")
	if code == "" || state == "" {
		errorResponse(w, http.StatusBadRequest, "bad_request",
			"callback is missing the code or state parameter")
		return
	}

	// THE CSRF CHECK. Consuming the state — delete, not just read — is what
	// makes a replayed callback fail: the value is good for exactly one
	// redemption, so capturing a callback URL from a log buys nothing.
	h.mu.Lock()
	issued, ok := h.pending[state]
	delete(h.pending, state)
	h.mu.Unlock()

	if !ok {
		errorResponse(w, http.StatusBadRequest, "invalid_state",
			"this authorization did not originate here, or it was already used — start again at /v1/kroger/authorize")
		return
	}
	if time.Since(issued) > stateTTL {
		errorResponse(w, http.StatusBadRequest, "expired_state",
			"this authorization took too long — start again at /v1/kroger/authorize")
		return
	}

	tok, err := h.Kroger.ExchangeCode(r.Context(), code, h.RedirectURI)
	if err != nil {
		serverErrorResponse(w, err)
		return
	}

	if err := h.Store.SaveKrogerToken(r.Context(), h.Box, store.DefaultAccountKey, store.KrogerToken{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    tok.ExpiresAt,
		Scope:        tok.Scope,
	}); err != nil {
		serverErrorResponse(w, err)
		return
	}

	// The response says what was granted and what to do next, and deliberately
	// contains NO PART of the token. A browser lands on this page; its URL and
	// body end up in history, screenshots, and bug reports.
	if err := writeJSON(w, http.StatusOK, envelope{
		"connected": map[string]any{
			"scope":      tok.Scope,
			"expires_at": tok.ExpiresAt,
			"next":       "POST /v1/kroger/cart with a target_id to fill your cart",
		},
	}); err != nil {
		serverErrorResponse(w, err)
	}
}

type addToCartRequest struct {
	TargetID *int64 `json:"target_id"`
}

// AddBasket handles POST /v1/kroger/cart.
func (h *CartHandler) AddBasket(w http.ResponseWriter, r *http.Request) {
	var req addToCartRequest
	if err := readJSON(w, r, &req); err != nil {
		badRequestResponse(w, err)
		return
	}
	if req.TargetID == nil {
		failedValidationResponse(w, map[string]string{"target_id": "must be provided"})
		return
	}

	ctx := r.Context()

	accessToken, err := h.validToken(ctx)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// 401 with a pointer to the fix. Not 403: the user isn't forbidden,
			// they simply haven't connected an account yet, and the difference
			// tells them whether to click a link or file a complaint.
			errorResponse(w, http.StatusUnauthorized, "kroger_not_connected",
				"no Kroger account is connected — visit /v1/kroger/authorize first")
			return
		}
		serverErrorResponse(w, err)
		return
	}

	_, lines, err := h.Store.LatestBasketForTarget(ctx, *req.TargetID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			failedValidationResponse(w, map[string]string{
				"target_id": "this target has no solved basket yet — POST /v1/solve first",
			})
			return
		}
		serverErrorResponse(w, err)
		return
	}

	items := make([]kroger.CartItem, 0, len(lines))
	skipped := []string{}
	for _, l := range lines {
		if l.ExternalID == "" {
			// A seeded product with no real Kroger id. Skipping it and SAYING
			// SO beats both alternatives: failing the whole request over one
			// line, or silently shipping an incomplete cart the user only
			// discovers at the store.
			skipped = append(skipped, l.ProductName)
			continue
		}
		items = append(items, kroger.CartItem{
			UPC:      l.ExternalID,
			Quantity: l.Packs,
			Modality: "PICKUP",
		})
	}

	if len(items) == 0 {
		failedValidationResponse(w, map[string]string{
			"target_id": "no items in this basket have a Kroger product id — was it solved against seeded data rather than a live store?",
		})
		return
	}

	if err := h.Kroger.AddToCart(ctx, accessToken, items); err != nil {
		serverErrorResponse(w, err)
		return
	}

	body := map[string]any{
		"items_added": len(items),
		// The caveat, in the response rather than only in the docs. Kroger's
		// add is additive with no way to read the cart back or undo, so a user
		// who retries gets double — and they should learn that from the
		// success message, not from a surprising receipt.
		"note": "Items were ADDED to your Kroger cart. Calling this again will add them a second time; Kroger's API cannot read back or remove cart contents.",
	}
	if len(skipped) > 0 {
		body["skipped"] = skipped
	}

	if err := writeJSON(w, http.StatusOK, envelope{"cart": body}); err != nil {
		serverErrorResponse(w, err)
	}
}

// validToken returns a usable access token, refreshing it if it has expired.
//
// Extracted so AddBasket contains no OAuth: it asks for a token and either gets
// one or gets an error it can map to a status. If a second cart endpoint ever
// exists, this is already the shared piece.
func (h *CartHandler) validToken(ctx context.Context) (string, error) {
	stored, err := h.Store.GetKrogerToken(ctx, h.Box, store.DefaultAccountKey)
	if err != nil {
		return "", err
	}

	user := kroger.UserToken{
		AccessToken:  stored.AccessToken,
		RefreshToken: stored.RefreshToken,
		ExpiresAt:    stored.ExpiresAt,
		Scope:        stored.Scope,
	}
	if user.Valid() {
		return user.AccessToken, nil
	}

	// Expired. The refresh token outlives the access token by months, so this
	// is the ordinary path after ~30 minutes of idleness — not an error.
	refreshed, err := h.Kroger.RefreshUserToken(ctx, stored.RefreshToken)
	if err != nil {
		return "", err
	}

	// Persist the new token BEST-EFFORT. Failing the user's request because I
	// couldn't write to the database would be backwards: I have a working token
	// in hand and the cart add can proceed. The cost of the lost write is one
	// extra refresh next time, which is not worth a failed request.
	if err := h.Store.SaveKrogerToken(ctx, h.Box, store.DefaultAccountKey, store.KrogerToken{
		AccessToken:  refreshed.AccessToken,
		RefreshToken: refreshed.RefreshToken,
		ExpiresAt:    refreshed.ExpiresAt,
		Scope:        refreshed.Scope,
	}); err != nil {
		log.Printf("warning: refreshed the Kroger token but could not persist it: %v", err)
	}

	return refreshed.AccessToken, nil
}
