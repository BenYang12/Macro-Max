package kroger

// Tests against a fake Kroger. Same technique as my FDC client: httptest gives
// me a real HTTP server I fully control, so I can exercise token refresh, 401
// handling, and rate limiting without touching Kroger's quota or needing
// credentials to exist.

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeKroger stands in for both the token endpoint and the API.
type fakeKroger struct {
	tokenCalls int
	apiCalls   int
	mu         sync.Mutex

	expiresIn  int    // seconds; 0 -> 1800
	tokenValue string // "" -> "test-token-N"
	apiStatus  int    // 0 -> 200
	apiBody    string
}

func (f *fakeKroger) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		if strings.Contains(r.URL.Path, "/connect/oauth2/token") {
			f.tokenCalls++
			exp := f.expiresIn
			if exp == 0 {
				exp = 1800
			}
			tok := f.tokenValue
			if tok == "" {
				tok = fmt.Sprintf("test-token-%d", f.tokenCalls)
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"access_token":%q,"token_type":"bearer","expires_in":%d}`, tok, exp)
			return
		}

		f.apiCalls++
		if f.apiStatus != 0 && f.apiStatus != 200 {
			w.WriteHeader(f.apiStatus)
			return
		}
		body := f.apiBody
		if body == "" {
			body = `{"data":[]}`
		}
		w.Write([]byte(body))
	}
}

func newTestClient(t *testing.T, f *fakeKroger) *Client {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)

	c := New("test-id", "test-secret")
	c.BaseURL = srv.URL
	c.tokens.tokenURL = srv.URL + "/v1/connect/oauth2/token"
	c.HTTP = srv.Client()
	c.tokens.http = srv.Client()
	c.minGap = 0 // no rate limiting in tests; it's tested separately
	return c
}

// The credentials must go in a Basic auth header, base64 encoded — not in the
// body. Getting this wrong produces a 401 that looks like bad credentials.
func TestToken_UsesBasicAuthHeader(t *testing.T) {
	var gotAuth, gotBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b := make([]byte, r.ContentLength)
		r.Body.Read(b)
		gotBody = string(b)
		fmt.Fprint(w, `{"access_token":"tok","expires_in":1800}`)
	}))
	defer srv.Close()

	c := New("my-id", "my-secret")
	c.tokens.tokenURL = srv.URL
	c.tokens.http = srv.Client()

	if _, err := c.tokens.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}

	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("my-id:my-secret"))
	if gotAuth != want {
		t.Errorf("Authorization = %q; want %q", gotAuth, want)
	}
	if !strings.Contains(gotBody, "grant_type=client_credentials") {
		t.Errorf("body %q should request client_credentials", gotBody)
	}
	if !strings.Contains(gotBody, "product.compact") {
		t.Errorf("body %q should request the product.compact scope", gotBody)
	}
	// The secret must never appear in the body — it belongs in the header only.
	if strings.Contains(gotBody, "my-secret") {
		t.Error("the client secret leaked into the request body")
	}
}

// A cached token must be REUSED. If this fails I'm burning quota on every call.
func TestToken_IsReusedWhileValid(t *testing.T) {
	f := &fakeKroger{}
	c := newTestClient(t, f)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if _, err := c.SearchProducts(ctx, "oats", "09700117", 1); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	if f.tokenCalls != 1 {
		t.Errorf("authenticated %d times; want 1 (the token should be cached)", f.tokenCalls)
	}
	if f.apiCalls != 5 {
		t.Errorf("made %d API calls; want 5", f.apiCalls)
	}
}

// A token close to expiry must be refreshed EARLY, not used until it dies.
// expires_in of 20s is inside my 30s safety margin, so every call re-auths.
func TestToken_RefreshesInsideTheSafetyMargin(t *testing.T) {
	f := &fakeKroger{expiresIn: 20}
	c := newTestClient(t, f)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := c.SearchProducts(ctx, "oats", "09700117", 1); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	if f.tokenCalls != 3 {
		t.Errorf("authenticated %d times; want 3 — a token expiring in 20s is inside the 30s margin", f.tokenCalls)
	}
}

// THE THUNDERING HERD TEST. Ten goroutines want a token at once; exactly one
// refresh must happen. Run this with -race to also prove the mutex is doing its
// job.
func TestToken_ConcurrentCallersCauseOneRefresh(t *testing.T) {
	f := &fakeKroger{}
	c := newTestClient(t, f)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.SearchProducts(ctx, "oats", "09700117", 1)
		}()
	}
	wg.Wait()

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tokenCalls != 1 {
		t.Errorf("authenticated %d times under concurrency; want exactly 1", f.tokenCalls)
	}
}

// Bad credentials must say so, because that's the most likely first failure.
func TestToken_BadCredentialsGiveAUsefulError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New("bad", "bad")
	c.tokens.tokenURL = srv.URL
	c.tokens.http = srv.Client()

	_, err := c.tokens.Token(context.Background())
	if err == nil {
		t.Fatal("expected an error for bad credentials")
	}
	if !strings.Contains(err.Error(), "KROGER_CLIENT_ID") {
		t.Errorf("error %q should name the env vars to check", err)
	}
}

func TestSearchProducts_RequiresLocationID(t *testing.T) {
	c := newTestClient(t, &fakeKroger{})

	_, err := c.SearchProducts(context.Background(), "chicken", "", 10)
	if err == nil {
		t.Fatal("expected an error when locationId is missing")
	}
	// Without a location Kroger returns products with NO PRICES, which would
	// silently produce a catalog of free food.
	if !strings.Contains(err.Error(), "no prices") {
		t.Errorf("error %q should explain why locationId matters", err)
	}
}

func TestSearchProducts_DecodesTheNestedItemsArray(t *testing.T) {
	f := &fakeKroger{apiBody: `{"data":[{
		"productId":"0001111041195",
		"brand":"Kroger",
		"description":"Kroger Boneless Skinless Chicken Breast",
		"items":[{"size":"16 oz","price":{"regular":3.99,"promo":2.99},
		          "inventory":{"stockLevel":"HIGH"}}]
	}]}`}
	c := newTestClient(t, f)

	products, err := c.SearchProducts(context.Background(), "chicken breast", "01400376", 10)
	if err != nil {
		t.Fatalf("SearchProducts: %v", err)
	}
	if len(products) != 1 {
		t.Fatalf("got %d products; want 1", len(products))
	}
	p := products[0]
	if p.ProductID != "0001111041195" {
		t.Errorf("productId = %q", p.ProductID)
	}
	// Size and price live on the ITEM, not the product — the nesting that
	// surprised me when I first read the API docs.
	if len(p.Items) != 1 {
		t.Fatalf("got %d items; want 1", len(p.Items))
	}
	if p.Items[0].Size != "16 oz" {
		t.Errorf("size = %q; want 16 oz", p.Items[0].Size)
	}
	if p.Items[0].Price.Regular != 3.99 {
		t.Errorf("regular = %v; want 3.99", p.Items[0].Price.Regular)
	}
}

func TestGet_TypedErrorsForEachStatus(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, "token rejected"},
		{http.StatusForbidden, "lacks permission"},
		{http.StatusTooManyRequests, "rate limited"},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			c := newTestClient(t, &fakeKroger{apiStatus: tc.status})

			_, err := c.SearchProducts(context.Background(), "oats", "09700117", 1)
			if err == nil {
				t.Fatalf("expected an error for %d", tc.status)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should contain %q", err, tc.want)
			}
		})
	}
}

// The money border. Kroger sends float dollars; everything past this point is
// integer cents.
func TestDollarsToCents(t *testing.T) {
	tests := []struct {
		dollars float64
		want    int64
	}{
		{3.99, 399}, // the classic: 3.99*100 is 398.9999... in binary
		{0.99, 99},
		{10.00, 1000},
		{2.50, 250},
		{0, 0},
		{-1, 0}, // a negative price is nonsense; clamp rather than propagate
		{12.345, 1235},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprint(tc.dollars), func(t *testing.T) {
			if got := DollarsToCents(tc.dollars); got != tc.want {
				t.Errorf("DollarsToCents(%v) = %d; want %d", tc.dollars, got, tc.want)
			}
		})
	}
}

// The rate limiter must actually delay. Three calls at 20/sec should take at
// least two gaps.
func TestRateLimiter_PacesRequests(t *testing.T) {
	f := &fakeKroger{}
	c := newTestClient(t, f)
	c.minGap = 25 * time.Millisecond

	start := time.Now()
	for i := 0; i < 3; i++ {
		_, _ = c.SearchProducts(context.Background(), "oats", "09700117", 1)
	}
	elapsed := time.Since(start)

	if elapsed < 50*time.Millisecond {
		t.Errorf("3 calls took %v; want >= 50ms with a 25ms gap", elapsed)
	}
}
