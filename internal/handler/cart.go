package handler

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/BenYang12/Macro-Max/internal/kroger"
	"github.com/BenYang12/Macro-Max/internal/store"
)

const (
	cartStateTTL       = 10 * time.Minute
	maxCartStateLength = 1024
	cartNonceBytes     = 16
	cartNonceCookie    = "macro_max_cart_nonce"
)

var (
	krogerProductIDPattern = regexp.MustCompile(`^[0-9]{8,14}$`)
	errExpiredCartState    = errors.New("expired cart state")
)

type CartStore interface {
	GetTarget(ctx context.Context, id int64, capabilityDigest []byte) (store.UserTarget, error)
	LatestBasketForTarget(ctx context.Context, targetID int64) (store.Basket, []store.BasketLine, error)
	BasketByIDForTarget(ctx context.Context, basketID, targetID int64) (store.Basket, []store.BasketLine, error)
}

type CartClient interface {
	AuthorizeURL(redirectURI, state string) string
	ExchangeCode(ctx context.Context, code, redirectURI string) (kroger.UserToken, error)
	AddToCart(ctx context.Context, accessToken string, items []kroger.CartItem) error
}

type cartState struct {
	TargetID int64  `json:"target_id"`
	BasketID int64  `json:"basket_id"`
	Expires  int64  `json:"expires"`
	Nonce    string `json:"nonce"`
}

type CartHandler struct {
	Store        CartStore
	Kroger       CartClient
	RedirectURI  string
	WebAppURL    *url.URL
	stateKey     []byte
	secureCookie bool
	now          func() time.Time
}

func NewCartHandler(s CartStore, k CartClient, clientSecret, webAppURL string) (*CartHandler, error) {
	webURL, err := validateOriginURL("WEB_APP_URL", webAppURL)
	if err != nil {
		return nil, err
	}
	redirectURI := webURL.Scheme + "://" + webURL.Host + "/api/kroger/callback"

	derive := hmac.New(sha256.New, []byte(clientSecret))
	_, _ = derive.Write([]byte("macro-max:kroger-cart-state:v1"))
	return &CartHandler{Store: s, Kroger: k, RedirectURI: redirectURI, WebAppURL: webURL,
		stateKey: derive.Sum(nil), secureCookie: webURL.Scheme == "https", now: time.Now}, nil
}

func validateOriginURL(name, raw string) (*url.URL, error) {
	// A dashboard text box is a lossy channel: it happily stores a trailing
	// newline or a stray leading space that no human can see when re-reading
	// the field. Trimming here costs nothing and removes an entire category of
	// unfalsifiable "but I typed it correctly" deploy failures.
	trimmed := strings.TrimSpace(raw)

	// An UNSET variable and a MALFORMED one are different operator mistakes —
	// "you forgot to add it" versus "look closely at what you pasted" — and
	// they used to share one error message, which made a failing deploy
	// impossible to diagnose from the log alone. Say which one happened.
	if trimmed == "" {
		return nil, fmt.Errorf("%s is not set", name)
	}

	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		// %q quotes the value and escapes anything invisible, so surrounding
		// quote characters, tabs, or a missing scheme show up literally in the
		// log instead of being silently re-rendered as a plausible-looking URL.
		// The value is a public origin, never a credential, so logging it is safe.
		return nil, fmt.Errorf("%s must be an absolute http or https URL, got %q", name, trimmed)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("%s must be an origin without credentials, path, query, or fragment", name)
	}
	u.Path = ""
	return u, nil
}

type authorizeCartRequest struct {
	TargetID *int64 `json:"target_id"`
}

func (h *CartHandler) Authorize(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != h.WebAppURL.Scheme+"://"+h.WebAppURL.Host {
		errorResponse(w, http.StatusForbidden, "invalid_origin", "request origin is not allowed")
		return
	}
	var req authorizeCartRequest
	if err := readJSON(w, r, &req); err != nil {
		badRequestResponse(w, err)
		return
	}
	if req.TargetID == nil || *req.TargetID < 1 {
		failedValidationResponse(w, map[string]string{"target_id": "must be a positive integer"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	digest, ok := capabilityDigest(r)
	if !ok {
		notFoundResponse(w)
		return
	}
	if _, err := h.Store.GetTarget(r.Context(), *req.TargetID, digest); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFoundResponse(w)
		} else {
			serverErrorResponse(w, err)
		}
		return
	}
	basket, lines, err := h.Store.LatestBasketForTarget(r.Context(), *req.TargetID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			failedValidationResponse(w, map[string]string{"target_id": "has no solved basket"})
			return
		}
		serverErrorResponse(w, err)
		return
	}
	if code := validateCartBasket(basket, lines); code != "" {
		errorResponse(w, http.StatusUnprocessableEntity, code, "this basket cannot be added to Kroger")
		return
	}
	state, nonce, err := h.signState(*req.TargetID, basket.ID)
	if err != nil {
		serverErrorResponse(w, err)
		return
	}
	h.setNonceCookie(w, nonce, int(cartStateTTL/time.Second))
	if err := writeJSON(w, http.StatusOK, envelope{"authorize_url": h.Kroger.AuthorizeURL(h.RedirectURI, state)}); err != nil {
		serverErrorResponse(w, err)
	}
}

func (h *CartHandler) Callback(w http.ResponseWriter, r *http.Request) {
	rawState := r.URL.Query().Get("state")
	state, err := h.verifyState(rawState)
	if err != nil {
		code := "invalid_state"
		if errors.Is(err, errExpiredCartState) {
			code = "expired_state"
		}
		h.redirectResult(w, r, code)
		return
	}
	cookie, err := r.Cookie(cartNonceCookie)
	if err != nil || !constantTimeNonceEqual(cookie.Value, state.Nonce) {
		h.redirectResult(w, r, "browser_mismatch")
		return
	}
	// Consume browser binding before any external call. Replaying the callback
	// in the same browser fails even if Kroger has not yet rejected the code.
	h.setNonceCookie(w, "", -1)
	if r.URL.Query().Get("error") != "" {
		h.redirectResult(w, r, "authorization_denied")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		h.redirectResult(w, r, "invalid_callback")
		return
	}

	tok, err := h.Kroger.ExchangeCode(r.Context(), code, h.RedirectURI)
	if err != nil || tok.AccessToken == "" {
		// The redirect carries only an opaque code because the browser is an
		// untrusted audience, but SOMEBODY has to be told what actually broke.
		// The error text describes Kroger's rejection and never contains a
		// token, so the server log is the right audience for it.
		log.Printf("kroger cart: token exchange failed: %v", err)
		h.redirectResult(w, r, "token_exchange_failed")
		return
	}
	// The scope check is ADVISORY, and the empty case is why.
	//
	// RFC 6749 makes the `scope` field OPTIONAL in a token response: a server
	// may omit it to mean "you got exactly what you asked for". Kroger omits
	// it. Treating that silence as a denial rejected every real token — the
	// authorization succeeded, the app is granted cart.basic:write, and the
	// user still got told permission was refused. A check that cannot tell
	// "denied" from "not reported" is not a safety net, it is a coin flip.
	//
	// So: reject only when Kroger AFFIRMATIVELY listed scopes and cart write
	// was not among them. When nothing is reported, proceed and let the cart
	// call be the judge — AddToCart turns a real 403 into a precise error,
	// which was always the authoritative answer. The cost of guessing wrong is
	// one failed API call; the cost of the old behavior was a dead feature.
	if tok.Scope != "" && !scopeContains(tok.Scope, kroger.ScopeCartWrite) {
		// A granted scope names a permission, it does not confer one, so this
		// is safe to log — and it is the only record of why the flow stopped.
		log.Printf("kroger cart: token granted scopes %q, want %q", tok.Scope, kroger.ScopeCartWrite)
		h.redirectResult(w, r, "missing_cart_scope")
		return
	}
	basket, lines, err := h.Store.BasketByIDForTarget(r.Context(), state.BasketID, state.TargetID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			h.redirectResult(w, r, "basket_not_found")
		} else {
			h.redirectResult(w, r, "basket_load_failed")
		}
		return
	}
	if result := validateCartBasket(basket, lines); result != "" {
		h.redirectResult(w, r, result)
		return
	}
	items := make([]kroger.CartItem, 0, len(lines))
	for _, line := range lines {
		items = append(items, kroger.CartItem{UPC: line.ExternalID, Quantity: line.Packs, Modality: "PICKUP"})
	}
	if err := h.Kroger.AddToCart(r.Context(), tok.AccessToken, items); err != nil {
		// Now that the scope pre-check no longer blocks unreported scopes, THIS
		// is where a genuinely missing permission surfaces — as Kroger's 403,
		// which AddToCart already translates into a sentence naming the scope.
		// Losing that to an opaque redirect code would put the diagnosis right
		// back out of reach.
		log.Printf("kroger cart: add to cart failed: %v", err)
		h.redirectResult(w, r, "cart_add_failed")
		return
	}
	h.redirectResult(w, r, "success")
}

func validateCartBasket(basket store.Basket, lines []store.BasketLine) string {
	if basket.StoreID != store.UniversityPlaceStoreID {
		return "wrong_store"
	}
	if len(lines) == 0 {
		return "empty_basket"
	}
	for _, line := range lines {
		if !krogerProductIDPattern.MatchString(line.ExternalID) || line.Packs < 1 {
			return "invalid_product"
		}
	}
	return ""
}

func (h *CartHandler) signState(targetID, basketID int64) (string, string, error) {
	nonceBytes := make([]byte, cartNonceBytes)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", "", err
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	payload, err := json.Marshal(cartState{TargetID: targetID, BasketID: basketID, Expires: h.now().Add(cartStateTTL).Unix(), Nonce: nonce})
	if err != nil {
		return "", "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, h.stateKey)
	_, _ = mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nonce, nil
}

func (h *CartHandler) verifyState(raw string) (cartState, error) {
	if len(raw) == 0 || len(raw) > maxCartStateLength {
		return cartState{}, errors.New("invalid state length")
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return cartState{}, errors.New("malformed state")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(sig) != sha256.Size {
		return cartState{}, errors.New("malformed signature")
	}
	mac := hmac.New(sha256.New, h.stateKey)
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return cartState{}, errors.New("invalid signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return cartState{}, errors.New("malformed payload")
	}
	dec := json.NewDecoder(strings.NewReader(string(payload)))
	dec.DisallowUnknownFields()
	var state cartState
	if err := dec.Decode(&state); err != nil || state.TargetID < 1 || state.BasketID < 1 {
		return cartState{}, errors.New("invalid payload")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return cartState{}, errors.New("invalid trailing payload")
	}
	nonce, err := base64.RawURLEncoding.DecodeString(state.Nonce)
	if err != nil || len(nonce) != cartNonceBytes {
		return cartState{}, errors.New("invalid nonce")
	}
	now := h.now().Unix()
	if state.Expires <= now {
		return cartState{}, errExpiredCartState
	}
	if state.Expires > h.now().Add(cartStateTTL+time.Second).Unix() {
		return cartState{}, errors.New("invalid future expiry")
	}
	return state, nil
}

func constantTimeNonceEqual(a, b string) bool {
	aBytes, aErr := base64.RawURLEncoding.DecodeString(a)
	bBytes, bErr := base64.RawURLEncoding.DecodeString(b)
	return aErr == nil && bErr == nil && len(aBytes) == cartNonceBytes && len(bBytes) == cartNonceBytes && hmac.Equal(aBytes, bBytes)
}

// scopeContains reports whether a granted-scope list includes one scope.
//
// RFC 6749 says the list is space-delimited, and strings.Fields alone would be
// the letter-perfect reading of the spec. Real providers are looser than that:
// a comma-separated list is common enough to be worth accepting, and reading
// one as a single unsplittable scope would reject a token that DOES carry the
// permission — failing closed on a technicality rather than on a real denial.
// Splitting on both separators costs nothing and cannot widen the match: every
// candidate still has to equal `want` exactly.
func scopeContains(raw, want string) bool {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == ','
	})
	for _, scope := range fields {
		if scope == want {
			return true
		}
	}
	return false
}
func (h *CartHandler) setNonceCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: cartNonceCookie, Value: value, Path: "/api/kroger/callback", MaxAge: maxAge, HttpOnly: true, Secure: h.secureCookie, SameSite: http.SameSiteLaxMode})
}
func (h *CartHandler) redirectResult(w http.ResponseWriter, r *http.Request, result string) {
	u := *h.WebAppURL
	q := u.Query()
	if result == "success" {
		q.Set("cart", "success")
	} else {
		q.Set("cart_error", result)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}
