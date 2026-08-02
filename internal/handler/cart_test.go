package handler

// cart_test.go — unit tests for the Kroger cart routes.
//
// The OAuth ones are the reason this file exists. Everything else here is
// ordinary status-code plumbing, but the `state` check is a SECURITY control,
// and a security control that isn't tested is a security control you're hoping
// works. Three of the tests below (forged state, replayed state, expired state)
// are each a real attack the check is supposed to stop.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenYang12/Macro-Max/internal/crypt"
	"github.com/BenYang12/Macro-Max/internal/kroger"
	"github.com/BenYang12/Macro-Max/internal/store"
)

type fakeCartStore struct {
	lines     []store.BasketLine
	basketErr error

	token    store.KrogerToken
	tokenErr error

	saved     *store.KrogerToken
	saveErr   error
	saveCalls int
}

func (f *fakeCartStore) LatestBasketForTarget(ctx context.Context, targetID int64) (store.Basket, []store.BasketLine, error) {
	if f.basketErr != nil {
		return store.Basket{}, nil, f.basketErr
	}
	return store.Basket{ID: 1, TargetID: targetID}, f.lines, nil
}

func (f *fakeCartStore) GetKrogerToken(ctx context.Context, box *crypt.Box, key string) (store.KrogerToken, error) {
	return f.token, f.tokenErr
}

func (f *fakeCartStore) SaveKrogerToken(ctx context.Context, box *crypt.Box, key string, t store.KrogerToken) error {
	f.saveCalls++
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = &t
	return nil
}

type fakeCartClient struct {
	exchanged kroger.UserToken
	exchErr   error

	refreshed  kroger.UserToken
	refreshErr error

	// gotItems captures what was actually sent to Kroger — the only way to
	// assert on the UPC/quantity mapping, since AddToCart returns nothing.
	gotItems  []kroger.CartItem
	gotToken  string
	addErr    error
	addCalled bool
}

func (f *fakeCartClient) AuthorizeURL(redirectURI, state string) string {
	return "https://kroger.example/authorize?state=" + state + "&redirect_uri=" + redirectURI
}

func (f *fakeCartClient) ExchangeCode(ctx context.Context, code, redirectURI string) (kroger.UserToken, error) {
	return f.exchanged, f.exchErr
}

func (f *fakeCartClient) RefreshUserToken(ctx context.Context, refreshToken string) (kroger.UserToken, error) {
	return f.refreshed, f.refreshErr
}

func (f *fakeCartClient) AddToCart(ctx context.Context, accessToken string, items []kroger.CartItem) error {
	f.addCalled = true
	f.gotToken = accessToken
	f.gotItems = items
	return f.addErr
}

func newCartHandler(t *testing.T, st *fakeCartStore, kc *fakeCartClient) *CartHandler {
	t.Helper()
	box, err := crypt.NewBox(strings.Repeat("0a", crypt.KeyBytes))
	if err != nil {
		t.Fatal(err)
	}
	return NewCartHandler(st, kc, box, "http://localhost:4000/v1/kroger/callback")
}

// liveToken builds a token fake that is valid well past the refresh margin, so
// tests exercising the happy path don't accidentally exercise the refresh path.
func liveToken() store.KrogerToken {
	return store.KrogerToken{
		AccessToken:  "live-access",
		RefreshToken: "live-refresh",
		ExpiresAt:    time.Now().Add(time.Hour),
		Scope:        kroger.ScopeCartWrite,
	}
}

func basketLines() []store.BasketLine {
	return []store.BasketLine{
		{ProductID: 1, ExternalID: "0001111041700", ProductName: "Lentils 454g", FoodName: "Lentils, dry", Packs: 2},
		{ProductID: 2, ExternalID: "0001111060903", ProductName: "Frozen Broccoli", FoodName: "Broccoli, frozen", Packs: 3},
	}
}

// ------------------------------------------------------------------ authorize

func TestAuthorize_RedirectsAndRemembersState(t *testing.T) {
	h := newCartHandler(t, &fakeCartStore{}, &fakeCartClient{})

	rr := httptest.NewRecorder()
	h.Authorize(rr, httptest.NewRequest(http.MethodGet, "/v1/kroger/authorize", nil))

	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rr.Code)
	}
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "state=") {
		t.Errorf("redirect has no state parameter: %s", loc)
	}

	// One pending state recorded. Without this the callback could never
	// succeed, and the CSRF check would be a check against nothing.
	h.mu.Lock()
	n := len(h.pending)
	h.mu.Unlock()
	if n != 1 {
		t.Errorf("pending states = %d, want 1", n)
	}
}

// ------------------------------------------------------------------- callback

// authorizeAndGetState runs the real Authorize handler and digs the state back
// out, so the callback tests use a state that was genuinely issued rather than
// one poked into the map. That keeps them testing the two halves together.
func authorizeAndGetState(t *testing.T, h *CartHandler) string {
	t.Helper()
	rr := httptest.NewRecorder()
	h.Authorize(rr, httptest.NewRequest(http.MethodGet, "/v1/kroger/authorize", nil))

	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.pending {
		return s
	}
	t.Fatal("no state was recorded")
	return ""
}

func callback(t *testing.T, h *CartHandler, query string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.Callback(rr, httptest.NewRequest(http.MethodGet, "/v1/kroger/callback?"+query, nil))
	return rr
}

func TestCallback_ExchangesCodeAndStoresToken(t *testing.T) {
	st := &fakeCartStore{}
	kc := &fakeCartClient{exchanged: kroger.UserToken{
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
		ExpiresAt:    time.Now().Add(30 * time.Minute),
		Scope:        kroger.ScopeCartWrite,
	}}
	h := newCartHandler(t, st, kc)

	rr := callback(t, h, "code=abc&state="+authorizeAndGetState(t, h))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if st.saved == nil || st.saved.RefreshToken != "new-refresh" {
		t.Fatalf("token not persisted: %+v", st.saved)
	}

	// THE LEAK CHECK. A browser lands on this page: its URL and body end up in
	// history, screenshots, and pasted bug reports. Neither token may appear.
	body := rr.Body.String()
	if strings.Contains(body, "new-access") || strings.Contains(body, "new-refresh") {
		t.Errorf("token leaked into the callback response body: %s", body)
	}
}

func TestCallback_RejectsForgedState(t *testing.T) {
	h := newCartHandler(t, &fakeCartStore{}, &fakeCartClient{})

	// The attack: an attacker sends the victim a callback URL carrying a code
	// for the ATTACKER'S Kroger account. Without the state check my server
	// would store their token, and the victim would fill a stranger's cart.
	// No Authorize call happened here, so nothing is pending and this must fail.
	rr := callback(t, h, "code=attacker-code&state=made-up")

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "invalid_state") {
		t.Errorf("expected invalid_state; got: %s", rr.Body.String())
	}
}

func TestCallback_StateIsSingleUse(t *testing.T) {
	st := &fakeCartStore{}
	h := newCartHandler(t, st, &fakeCartClient{
		exchanged: kroger.UserToken{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)},
	})

	state := authorizeAndGetState(t, h)

	if rr := callback(t, h, "code=abc&state="+state); rr.Code != http.StatusOK {
		t.Fatalf("first callback: status = %d, want 200", rr.Code)
	}

	// Replaying the SAME callback URL — out of a proxy log, browser history, or
	// a Referer header — must fail. Consuming the state on first use is what
	// makes a captured URL worthless.
	if rr := callback(t, h, "code=abc&state="+state); rr.Code != http.StatusBadRequest {
		t.Errorf("replayed callback: status = %d, want 400", rr.Code)
	}
}

func TestCallback_RejectsExpiredState(t *testing.T) {
	h := newCartHandler(t, &fakeCartStore{}, &fakeCartClient{})
	state := authorizeAndGetState(t, h)

	// Backdate the issue time past the TTL rather than sleeping for ten
	// minutes. Reaching into the handler's state is acceptable here precisely
	// because the alternative is a test nobody will ever wait for.
	h.mu.Lock()
	h.pending[state] = time.Now().Add(-stateTTL - time.Minute)
	h.mu.Unlock()

	rr := callback(t, h, "code=abc&state="+state)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "expired_state") {
		t.Errorf("expected expired_state; got: %s", rr.Body.String())
	}
}

func TestCallback_ReportsDeniedConsent(t *testing.T) {
	h := newCartHandler(t, &fakeCartStore{}, &fakeCartClient{})

	// Kroger signals "the user clicked Deny" with ?error=, not by omitting the
	// code. Handling it separately is the difference between "you declined" and
	// a baffling "missing code parameter".
	rr := callback(t, h, "error=access_denied&state=whatever")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "authorization_denied") {
		t.Errorf("expected authorization_denied; got: %s", rr.Body.String())
	}
}

// ------------------------------------------------------------------ add basket

func postCart(t *testing.T, h *CartHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.AddBasket(rr, httptest.NewRequest(http.MethodPost, "/v1/kroger/cart", strings.NewReader(body)))
	return rr
}

func TestAddBasket_MapsLinesToUPCsAndQuantities(t *testing.T) {
	st := &fakeCartStore{token: liveToken(), lines: basketLines()}
	kc := &fakeCartClient{}
	h := newCartHandler(t, st, kc)

	rr := postCart(t, h, `{"target_id": 7}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	if len(kc.gotItems) != 2 {
		t.Fatalf("items = %d, want 2: %+v", len(kc.gotItems), kc.gotItems)
	}
	// UPC comes from ExternalID, and quantity from PACKS — not grams. Buying
	// 908 units of lentils instead of 2 bags is the bug this pins down, and it
	// would be an expensive one to find in production.
	if kc.gotItems[0].UPC != "0001111041700" || kc.gotItems[0].Quantity != 2 {
		t.Errorf("first item = %+v", kc.gotItems[0])
	}
	if kc.gotItems[1].Quantity != 3 {
		t.Errorf("second item quantity = %d, want 3", kc.gotItems[1].Quantity)
	}
	if kc.gotItems[0].Modality != "PICKUP" {
		t.Errorf("modality = %q, want PICKUP", kc.gotItems[0].Modality)
	}
	if kc.gotToken != "live-access" {
		t.Errorf("sent token %q, want the stored access token", kc.gotToken)
	}
}

func TestAddBasket_WarnsThatAddingIsNotIdempotent(t *testing.T) {
	h := newCartHandler(t, &fakeCartStore{token: liveToken(), lines: basketLines()}, &fakeCartClient{})

	rr := postCart(t, h, `{"target_id": 7}`)

	// Kroger's cart API is additive with no read-back and no undo. A user who
	// retries gets double, and they should learn that from the success message
	// rather than from a surprising pickup order.
	if !strings.Contains(rr.Body.String(), "again") {
		t.Errorf("success response should warn about double-adding; got: %s", rr.Body.String())
	}
}

func TestAddBasket_SkipsLinesWithoutAKrogerID(t *testing.T) {
	lines := append(basketLines(), store.BasketLine{
		ProductID: 3, ExternalID: "", ProductName: "Seeded Oats", FoodName: "Oats", Packs: 1,
	})
	kc := &fakeCartClient{}
	h := newCartHandler(t, &fakeCartStore{token: liveToken(), lines: lines}, kc)

	rr := postCart(t, h, `{"target_id": 7}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	// Partial success, reported. Failing the whole request over one seeded row
	// would be worse, and silently dropping it would be worse still — the user
	// finds out at the store.
	if len(kc.gotItems) != 2 {
		t.Errorf("items = %d, want 2 (the third has no UPC)", len(kc.gotItems))
	}
	if !strings.Contains(rr.Body.String(), "Seeded Oats") {
		t.Errorf("skipped item should be named in the response: %s", rr.Body.String())
	}
}

func TestAddBasket_AllSeededItemsIs422(t *testing.T) {
	lines := []store.BasketLine{{ProductID: 1, ExternalID: "", ProductName: "Seeded Oats", Packs: 1}}
	kc := &fakeCartClient{}
	h := newCartHandler(t, &fakeCartStore{token: liveToken(), lines: lines}, kc)

	rr := postCart(t, h, `{"target_id": 7}`)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rr.Code)
	}
	if kc.addCalled {
		t.Error("should not call Kroger with an empty item list")
	}
}

func TestAddBasket_NoStoredTokenIs401(t *testing.T) {
	st := &fakeCartStore{tokenErr: store.ErrNotFound, lines: basketLines()}
	h := newCartHandler(t, st, &fakeCartClient{})

	rr := postCart(t, h, `{"target_id": 7}`)

	// 401, not 403. The user isn't forbidden — they haven't connected an
	// account — and the response says where to go do that.
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "/v1/kroger/authorize") {
		t.Errorf("response should point at the authorize route: %s", rr.Body.String())
	}
}

func TestAddBasket_RefreshesAnExpiredToken(t *testing.T) {
	st := &fakeCartStore{
		lines: basketLines(),
		token: store.KrogerToken{
			AccessToken:  "stale-access",
			RefreshToken: "good-refresh",
			ExpiresAt:    time.Now().Add(-time.Minute), // expired
		},
	}
	kc := &fakeCartClient{refreshed: kroger.UserToken{
		AccessToken:  "fresh-access",
		RefreshToken: "good-refresh",
		ExpiresAt:    time.Now().Add(30 * time.Minute),
	}}
	h := newCartHandler(t, st, kc)

	rr := postCart(t, h, `{"target_id": 7}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// The refreshed token must be the one sent to Kroger. Sending the stale one
	// would 401 on every request made more than 30 minutes after connecting —
	// which is to say, essentially always.
	if kc.gotToken != "fresh-access" {
		t.Errorf("sent token %q, want the refreshed one", kc.gotToken)
	}
	if st.saved == nil || st.saved.AccessToken != "fresh-access" {
		t.Errorf("refreshed token was not persisted: %+v", st.saved)
	}
}

func TestAddBasket_SucceedsEvenIfPersistingTheRefreshFails(t *testing.T) {
	st := &fakeCartStore{
		lines:   basketLines(),
		token:   store.KrogerToken{AccessToken: "stale", RefreshToken: "r", ExpiresAt: time.Now().Add(-time.Minute)},
		saveErr: errors.New("database is down"),
	}
	h := newCartHandler(t, st, &fakeCartClient{refreshed: kroger.UserToken{
		AccessToken: "fresh", ExpiresAt: time.Now().Add(time.Hour),
	}})

	// I have a working token in hand. Failing the user's request because I
	// couldn't write it down is backwards — the cost of the lost write is one
	// extra refresh next time.
	if rr := postCart(t, h, `{"target_id": 7}`); rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 despite the failed save", rr.Code)
	}
}

func TestAddBasket_UnsolvedTargetIs422(t *testing.T) {
	st := &fakeCartStore{token: liveToken(), basketErr: store.ErrNotFound}
	h := newCartHandler(t, st, &fakeCartClient{})

	rr := postCart(t, h, `{"target_id": 7}`)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "/v1/solve") {
		t.Errorf("message should point at the next action: %s", rr.Body.String())
	}
}

func TestAddBasket_MissingTargetIDIs422(t *testing.T) {
	h := newCartHandler(t, &fakeCartStore{token: liveToken()}, &fakeCartClient{})

	if rr := postCart(t, h, `{}`); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rr.Code)
	}
}
