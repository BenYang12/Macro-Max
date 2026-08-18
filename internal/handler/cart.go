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
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("%s must be an absolute http or https URL", name)
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
		h.redirectResult(w, r, "token_exchange_failed")
		return
	}
	if !scopeContains(tok.Scope, kroger.ScopeCartWrite) {
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
func scopeContains(raw, want string) bool {
	for _, scope := range strings.Fields(raw) {
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
