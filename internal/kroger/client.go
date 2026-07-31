package kroger

// client.go — the Kroger API client: locations and product search.
//
// Same shape as my FDC client from Phase 2 (own *http.Client with a timeout,
// context-aware, typed errors), with one addition: every request carries a
// bearer token from the token manager, refreshed automatically.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const defaultBaseURL = "https://api.kroger.com/v1"

// Kroger's published limit is 10,000 requests per day per app. My whole catalog
// is ~42 foods, so one full ingestion is ~43 requests — comfortably under it.
// I still rate-limit, because the failure mode of getting this wrong is having
// my app throttled for a day.
const defaultRequestsPerSecond = 5

type Client struct {
	BaseURL string
	HTTP    *http.Client
	tokens  *tokenManager

	// limiter paces outgoing requests. See waitForSlot.
	limiterMu sync.Mutex
	lastCall  time.Time
	minGap    time.Duration
}

// New builds a client. store may be nil (in-memory tokens only).
func New(clientID, clientSecret string, store TokenStore) *Client {
	httpClient := &http.Client{Timeout: 15 * time.Second}

	return &Client{
		BaseURL: defaultBaseURL,
		HTTP:    httpClient,
		tokens: &tokenManager{
			clientID:     clientID,
			clientSecret: clientSecret,
			tokenURL:     defaultTokenURL,
			http:         httpClient,
			store:        store,
		},
		minGap: time.Second / defaultRequestsPerSecond,
	}
}

// ------------------------------------------------------------- response types
//
// Kroger nests deeply, and I only declare what I use. Same reasoning as the FDC
// client: I don't control this schema, so tolerating unknown fields is right.

// Location is one store.
type Location struct {
	LocationID string  `json:"locationId"`
	Name       string  `json:"name"`
	Chain      string  `json:"chain"`
	Address    Address `json:"address"`
}

type Address struct {
	AddressLine1 string `json:"addressLine1"`
	City         string `json:"city"`
	State        string `json:"state"`
	ZipCode      string `json:"zipCode"`
}

type locationsResponse struct {
	Data []Location `json:"data"`
}

// Product is one search result. Note the ITEMS array: Kroger models a product
// as a description plus one or more purchasable items, and the SIZE and PRICE
// live on the item, not the product. That surprised me and it's why the
// flattening below exists.
type Product struct {
	ProductID   string        `json:"productId"`
	UPC         string        `json:"upc"`
	Brand       string        `json:"brand"`
	Description string        `json:"description"`
	Items       []ProductItem `json:"items"`
}

type ProductItem struct {
	Size      string    `json:"size"`
	Price     Price     `json:"price"`
	Inventory Inventory `json:"inventory"`
}

// Price is in DOLLARS as a float, because that's what Kroger sends. It becomes
// integer cents the instant it crosses into my code — see dollarsToCents. This
// is the only place in the entire project where money is a float, and it exists
// solely because I don't control the wire format.
type Price struct {
	Regular float64 `json:"regular"`
	Promo   float64 `json:"promo"`
}

type Inventory struct {
	StockLevel string `json:"stockLevel"` // "HIGH" | "LOW" | "TEMPORARILY_OUT_OF_STOCK"
}

type productsResponse struct {
	Data []Product `json:"data"`
}

// ---------------------------------------------------------------- operations

// Locations finds stores near a zip code.
func (c *Client) Locations(ctx context.Context, zip string, limit int) ([]Location, error) {
	if limit <= 0 {
		limit = 10
	}
	params := url.Values{}
	// Kroger's filter parameters use dotted names. Not my choice.
	params.Set("filter.zipCode.near", zip)
	params.Set("filter.limit", strconv.Itoa(limit))

	var out locationsResponse
	if err := c.get(ctx, "/locations", params, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// SearchProducts finds products matching a term at one store.
//
// locationId is REQUIRED for prices. Without it Kroger returns products with no
// price data at all — which would silently produce a catalog of zero-cost foods
// and a nonsense basket. Worth stating loudly.
func (c *Client) SearchProducts(ctx context.Context, term, locationID string, limit int) ([]Product, error) {
	if locationID == "" {
		return nil, fmt.Errorf("locationId is required: without it Kroger returns no prices")
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	params := url.Values{}
	params.Set("filter.term", term)
	params.Set("filter.locationId", locationID)
	params.Set("filter.limit", strconv.Itoa(limit))

	var out productsResponse
	if err := c.get(ctx, "/products", params, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// get is the shared plumbing: rate limit, attach a token, send, check, decode.
func (c *Client) get(ctx context.Context, path string, params url.Values, dst any) error {
	c.waitForSlot(ctx)

	token, err := c.tokens.Token(ctx)
	if err != nil {
		return err
	}

	endpoint := c.BaseURL + path + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("requesting %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			// A 401 HERE (as opposed to at the token endpoint) means the token
			// expired mid-flight or the scope is wrong. Different cause,
			// different message.
			return fmt.Errorf("kroger returned 401 for %s: token rejected (scope may be missing product.compact)", path)
		case http.StatusForbidden:
			return fmt.Errorf("kroger returned 403 for %s: my app lacks permission for this endpoint", path)
		case http.StatusTooManyRequests:
			return fmt.Errorf("kroger returned 429: rate limited (daily cap is 10,000 requests)")
		}
		return fmt.Errorf("kroger returned %d for %s: %s", resp.StatusCode, path, body)
	}

	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("decoding %s response: %w", path, err)
	}
	return nil
}

// waitForSlot paces requests to at most defaultRequestsPerSecond.
//
// This is the simplest possible rate limiter: remember when I last called, and
// sleep until enough time has passed. It's not a token bucket — it doesn't
// allow bursts — and for ~43 sequential-ish requests that's exactly right.
// Reaching for golang.org/x/time/rate here would be adding a dependency to
// solve a problem I don't have.
//
// The mutex matters because the worker pool calls this from several goroutines,
// and without it two workers could both read the same lastCall and both decide
// they're clear to go.
func (c *Client) waitForSlot(ctx context.Context) {
	c.limiterMu.Lock()
	wait := time.Until(c.lastCall.Add(c.minGap))
	// Claim my slot BEFORE sleeping, so a second goroutine computes its wait
	// relative to my slot rather than the previous one. Claiming after sleeping
	// would let everyone stack up on the same instant.
	c.lastCall = time.Now().Add(max(0, wait))
	c.limiterMu.Unlock()

	if wait <= 0 {
		return
	}
	// select over a timer and ctx.Done so a cancelled run stops promptly
	// instead of finishing its nap first.
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

// DollarsToCents converts Kroger's float dollars to my integer cents.
//
// The +0.5 rounding is deliberate: 3.99 is stored as 3.9899999... in binary
// floating point, so int(3.99*100) truncates to 398 — a one-cent error on
// every single price. math.Round would work too; this is the same thing without
// the import.
//
// This function is the border checkpoint. Floats do not pass it.
func DollarsToCents(d float64) int64 {
	if d <= 0 {
		return 0
	}
	return int64(d*100 + 0.5)
}
