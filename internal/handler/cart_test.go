package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/BenYang12/Macro-Max/internal/kroger"
	"github.com/BenYang12/Macro-Max/internal/store"
)

type fakeCartStore struct {
	basket                   store.Basket
	lines                    []store.BasketLine
	exactLines               []store.BasketLine
	latestErr, exactErr      error
	gotBasketID, gotTargetID int64
	gotDigest                []byte
	targetErr                error
}

func (f *fakeCartStore) GetTarget(_ context.Context, _ int64, digest []byte) (store.UserTarget, error) {
	f.gotDigest = append([]byte(nil), digest...)
	return store.UserTarget{ID: 42}, f.targetErr
}

func (f *fakeCartStore) LatestBasketForTarget(context.Context, int64) (store.Basket, []store.BasketLine, error) {
	return f.basket, f.lines, f.latestErr
}
func (f *fakeCartStore) BasketByIDForTarget(_ context.Context, basketID, targetID int64) (store.Basket, []store.BasketLine, error) {
	f.gotBasketID, f.gotTargetID = basketID, targetID
	lines := f.lines
	if f.exactLines != nil {
		lines = f.exactLines
	}
	return f.basket, lines, f.exactErr
}

type fakeCartClient struct {
	token               kroger.UserToken
	exchangeErr, addErr error
	gotToken            string
	gotItems            []kroger.CartItem
}

func (f *fakeCartClient) AuthorizeURL(redirectURI, state string) string {
	q := url.Values{"redirect_uri": {redirectURI}, "state": {state}}
	return "https://kroger.example/authorize?" + q.Encode()
}
func (f *fakeCartClient) ExchangeCode(context.Context, string, string) (kroger.UserToken, error) {
	return f.token, f.exchangeErr
}
func (f *fakeCartClient) AddToCart(_ context.Context, token string, items []kroger.CartItem) error {
	f.gotToken, f.gotItems = token, items
	return f.addErr
}

func validStore() *fakeCartStore {
	return &fakeCartStore{basket: store.Basket{ID: 7, TargetID: 42, StoreID: store.UniversityPlaceStoreID}, lines: []store.BasketLine{{ExternalID: "0001111041700", Packs: 2}}}
}
func newCartHandler(t *testing.T, st *fakeCartStore, client *fakeCartClient, secret ...string) *CartHandler {
	t.Helper()
	s := "secret"
	if len(secret) > 0 {
		s = secret[0]
	}
	h, err := NewCartHandler(st, client, s, "http://localhost:3000")
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func authorize(t *testing.T, h *CartHandler, body string, origin string) (string, *http.Cookie, *httptest.ResponseRecorder) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/kroger/authorize", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", origin)
	r.Header.Set("Authorization", "Bearer "+testCapabilityToken)
	rr := httptest.NewRecorder()
	h.Authorize(rr, r)
	if rr.Code != http.StatusOK {
		return "", nil, rr
	}
	var out struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(out.AuthorizeURL)
	res := rr.Result()
	cookies := res.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies=%d", len(cookies))
	}
	return u.Query().Get("state"), cookies[0], rr
}
func callback(h *CartHandler, query string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/v1/kroger/callback?"+query, nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	h.Callback(rr, r)
	return rr
}
func result(t *testing.T, rr *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	u, _ := url.Parse(rr.Header().Get("Location"))
	return u.Query().Get("cart"), u.Query().Get("cart_error")
}

func TestAuthorizeOriginJSONPrevalidationAndCookie(t *testing.T) {
	st := validStore()
	h := newCartHandler(t, st, &fakeCartClient{})
	_, _, rr := authorize(t, h, `{"target_id":42}`, "https://evil.example")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("origin status=%d", rr.Code)
	}
	_, _, rr = authorize(t, h, `{"target_id":42,"extra":true}`, "http://localhost:3000")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown status=%d", rr.Code)
	}
	_, _, rr = authorize(t, h, `{}`, "http://localhost:3000")
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing status=%d", rr.Code)
	}
	bad := validStore()
	bad.lines[0].Packs = 0
	_, _, rr = authorize(t, newCartHandler(t, bad, &fakeCartClient{}), `{"target_id":42}`, "http://localhost:3000")
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("pack status=%d", rr.Code)
	}
	state, cookie, rr := authorize(t, h, `{"target_id":42}`, "http://localhost:3000")
	if rr.Code != http.StatusOK || state == "" {
		t.Fatalf("valid authorize status=%d", rr.Code)
	}
	if string(st.gotDigest) != string(testCapabilityDigest()) {
		t.Fatal("cart authorize did not propagate the bearer token's exact digest")
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q; want no-store", got)
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge <= 0 {
		t.Fatalf("cookie=%+v", cookie)
	}
}

func TestAuthorizeRejectsMissingMalformedAndWrongCapabilities(t *testing.T) {
	for _, tc := range []struct{ name, token string }{
		{"missing", ""}, {"malformed", "not-base64!"}, {"wrong", testWrongCapabilityToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := validStore()
			if tc.name == "wrong" {
				st.targetErr = store.ErrNotFound
			}
			h := newCartHandler(t, st, &fakeCartClient{})
			req := httptest.NewRequest(http.MethodPost, "/v1/kroger/authorize", strings.NewReader(`{"target_id":42}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "http://localhost:3000")
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rr := httptest.NewRecorder()
			h.Authorize(rr, req)
			if rr.Code != http.StatusNotFound {
				t.Fatalf("status = %d; want 404", rr.Code)
			}
		})
	}
}

func TestStateSecurityProperties(t *testing.T) {
	h := newCartHandler(t, validStore(), &fakeCartClient{})
	now := time.Unix(1_700_000_000, 0)
	h.now = func() time.Time { return now }
	a, na, err := h.signState(42, 7)
	if err != nil {
		t.Fatal(err)
	}
	b, nb, _ := h.signState(42, 7)
	if a == b || na == nb {
		t.Fatal("nonce reused")
	}
	decoded, err := h.verifyState(a)
	if err != nil || decoded.BasketID != 7 {
		t.Fatalf("state=%+v err=%v", decoded, err)
	}
	if _, err := newCartHandler(t, validStore(), &fakeCartClient{}, "other").verifyState(a); err == nil {
		t.Fatal("cross-secret state accepted")
	}
	h.now = func() time.Time { return now.Add(cartStateTTL) }
	if _, err := h.verifyState(a); !errors.Is(err, errExpiredCartState) {
		t.Fatalf("boundary err=%v", err)
	}
	if _, err := h.verifyState(strings.Repeat("x", maxCartStateLength+1)); err == nil {
		t.Fatal("oversized state accepted")
	}
}

func TestCallbackBrowserBindingReplayExactBasketAndSuccess(t *testing.T) {
	st := validStore()
	client := &fakeCartClient{token: kroger.UserToken{AccessToken: "access", Scope: kroger.ScopeCartWrite}}
	h := newCartHandler(t, st, client)
	state, _, _ := authorize(t, h, `{"target_id":42}`, "http://localhost:3000")
	_, failure := result(t, callback(h, "code=abc&state="+url.QueryEscape(state), nil))
	if failure != "browser_mismatch" {
		t.Fatalf("missing cookie=%q", failure)
	}
	state, cookie, _ := authorize(t, h, `{"target_id":42}`, "http://localhost:3000")
	rr := callback(h, "code=abc&state="+url.QueryEscape(state), cookie)
	ok, failure := result(t, rr)
	if ok != "success" || failure != "" {
		t.Fatalf("result=%q/%q", ok, failure)
	}
	if st.gotBasketID != 7 || st.gotTargetID != 42 {
		t.Fatalf("exact ids=%d/%d", st.gotBasketID, st.gotTargetID)
	}
	if client.gotToken != "access" || len(client.gotItems) != 1 {
		t.Fatal("cart not filled")
	}
	deleted := rr.Result().Cookies()
	if len(deleted) == 0 || deleted[0].MaxAge >= 0 {
		t.Fatalf("nonce cookie not deleted: %+v", deleted)
	}
	_, failure = result(t, callback(h, "code=abc&state="+url.QueryEscape(state), nil))
	if failure != "browser_mismatch" {
		t.Fatalf("replay=%q", failure)
	}
}

func TestCallbackRequiresStateOnDenialAndScope(t *testing.T) {
	h := newCartHandler(t, validStore(), &fakeCartClient{})
	_, failure := result(t, callback(h, "error=access_denied", nil))
	if failure != "invalid_state" {
		t.Fatalf("denial=%q", failure)
	}
	for _, tc := range []struct {
		name  string
		token kroger.UserToken
		want  string
	}{{"empty token", kroger.UserToken{Scope: kroger.ScopeCartWrite}, "token_exchange_failed"}, {"missing scope", kroger.UserToken{AccessToken: "a", Scope: "profile.compact"}, "missing_cart_scope"}} {
		t.Run(tc.name, func(t *testing.T) {
			c := &fakeCartClient{token: tc.token}
			h := newCartHandler(t, validStore(), c)
			state, cookie, _ := authorize(t, h, `{"target_id":42}`, "http://localhost:3000")
			_, failure := result(t, callback(h, "code=abc&state="+url.QueryEscape(state), cookie))
			if failure != tc.want {
				t.Fatalf("failure=%q", failure)
			}
		})
	}
}

func TestCallbackUsesBoundBasketAndMapsFailures(t *testing.T) {
	tests := []struct {
		name   string
		store  *fakeCartStore
		client *fakeCartClient
		want   string
	}{{"exact missing", func() *fakeCartStore { s := validStore(); s.exactErr = store.ErrNotFound; return s }(), &fakeCartClient{token: kroger.UserToken{AccessToken: "a", Scope: kroger.ScopeCartWrite}}, "basket_not_found"}, {"invalid exact basket", func() *fakeCartStore {
		s := validStore()
		s.exactLines = []store.BasketLine{{ExternalID: "0001111041700", Packs: 0}}
		return s
	}(), &fakeCartClient{token: kroger.UserToken{AccessToken: "a", Scope: kroger.ScopeCartWrite}}, "invalid_product"}, {"exchange", validStore(), &fakeCartClient{exchangeErr: errors.New("oauth")}, "token_exchange_failed"}, {"cart", validStore(), &fakeCartClient{token: kroger.UserToken{AccessToken: "a", Scope: kroger.ScopeCartWrite}, addErr: errors.New("cart")}, "cart_add_failed"}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newCartHandler(t, tc.store, tc.client)
			state, cookie, _ := authorize(t, h, `{"target_id":42}`, "http://localhost:3000")
			_, failure := result(t, callback(h, "code=abc&state="+url.QueryEscape(state), cookie))
			if failure != tc.want {
				t.Fatalf("failure=%q want=%q", failure, tc.want)
			}
		})
	}
}

func TestURLValidation(t *testing.T) {
	for _, raw := range []string{"javascript:alert(1)", "https://user@example.com", "https://example.com/path"} {
		if _, err := validateOriginURL("WEB_APP_URL", raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	if _, err := NewCartHandler(validStore(), &fakeCartClient{}, "s", "not-a-url"); err == nil {
		t.Fatal("bad web origin accepted")
	}
	h, err := NewCartHandler(validStore(), &fakeCartClient{}, "s", "https://app.example.com")
	if err != nil {
		t.Fatal(err)
	}
	_, cookie, rr := authorize(t, h, `{"target_id":42}`, "https://app.example.com")
	if rr.Code != http.StatusOK || !cookie.Secure {
		t.Fatalf("HTTPS callback cookie = %+v", cookie)
	}
	if h.RedirectURI != "https://app.example.com/api/kroger/callback" {
		t.Fatalf("derived redirect URI = %q", h.RedirectURI)
	}
	if cookie.Path != "/api/kroger/callback" {
		t.Fatalf("cookie path = %q", cookie.Path)
	}
}
