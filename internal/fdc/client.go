// Package fdc talks to the USDA FoodData Central API
// CLIENT package: knows how to fetch and decode FDC's JSON, and nothing else.
// Does not know about Postgres, HTTP handlers, or this project's Food type
// API docs: https://fdc.nal.usda.gov/api-guide.html

package fdc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// defaultBaseURL is real API
// Tests override to point at local httptest server -> testable is important
const defaultBaseURL = "https://api.nal.usda.gov/fdc/v1"

// Configured FDC API Client
// holds own *http.Client instead of calling http.Get (which uses http.DefaultClient -> NO TIMEOUT)
// Owning client = owning timeout
// ONE client shared across many requests is both correct and faster than creating one per call
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

func New(apiKey string) *Client {
	return &Client{
		BaseURL: defaultBaseURL,
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 10 * time.Second},
	}

}

// ---------------------------------------------------------------- API types
//
// These structs mirror FDC's JSON, NOT our database. They are a faithful
// description of what USDA sends — including field names we would never
// choose. Translating them into our own types is normalize.go's job.
//
// Only the fields we use are declared. encoding/json IGNORES unknown fields
// when DECODING, which is the opposite of what our own readJSON does — and
// correct here. We do not control FDC's schema; if they add a field tomorrow,
// silently ignoring it is right, whereas erroring would break the importer
// for no reason.

// FoodSummary is one result from a search.
type FoodSummary struct {
	FdcID       int64  `json:"fdcId"`
	Description string `json:"description"`
	DataType    string `json:"dataType"` // "Foundation" | "SR Legacy" | "Branded"
	BrandOwner  string `json:"brandOwner"`
}

// searchResponse is the envelope FDC wraps search results in. Unexported
// because callers only ever want the foods inside it.
type searchResponse struct {
	Foods []FoodSummary `json:"foods"`
}

// FoodDetail is the full record for one food.
type FoodDetail struct {
	FdcID       int64  `json:"fdcId"`
	Description string `json:"description"`
	DataType    string `json:"dataType"`

	// Foundation and SR Legacy foods report nutrients here, already per-100g.
	FoodNutrients []FoodNutrient `json:"foodNutrients"`

	// Branded foods carry label nutrients, which are PER SERVING, not
	// per-100g. Normalize has to divide by ServingSize — the most error-prone
	// part of this phase, and the reason Branded is the least preferred type.
	LabelNutrients  *LabelNutrients `json:"labelNutrients"`
	ServingSize     float64         `json:"servingSize"`
	ServingSizeUnit string          `json:"servingSizeUnit"`
}

// FoodNutrient is one measured nutrient. Note the NESTED struct: FDC wraps the
// nutrient's identity in its own object, so the JSON is
//
//	{"nutrient": {"id": 1003, "name": "Protein", "unitName": "G"}, "amount": 22.5}
//
// and the Go types have to nest the same way for the decoder to follow it.
type FoodNutrient struct {
	Nutrient Nutrient `json:"nutrient"`
	Amount   float64  `json:"amount"`
}

type Nutrient struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	UnitName string `json:"unitName"` // "G", "KCAL", "kJ" — casing varies
}

// LabelNutrients mirrors the Branded per-serving block. Every field is a
// pointer because ANY of them can be absent, and 0 is a real value ("0 g fat"
// is a true claim). Same nil-vs-zero distinction as our nullable DB columns.
type LabelNutrients struct {
	Protein       *LabelValue `json:"protein"`
	Carbohydrates *LabelValue `json:"carbohydrates"`
	Fat           *LabelValue `json:"fat"`
	Calories      *LabelValue `json:"calories"`
}

type LabelValue struct {
	Value float64 `json:"value"`
}

// Nutrient IDs, the stable identifiers FDC uses. NAMES are not stable across
// data types ("Protein" vs "Protein, total"), so ALWAYS match on ID.
const (
	NutrientProtein  = 1003
	NutrientFat      = 1004
	NutrientCarbs    = 1005
	NutrientEnergyKC = 1008 // energy in kcal
	NutrientEnergyKJ = 1062 // energy in kJ — needs conversion

	// Current Foundation records frequently omit 1008 entirely and report
	// energy only under these two, both already in kcal. 2048 applies
	// food-group-specific Atwater coefficients; 2047 applies the general
	// 4/4/9 factors. Without them the importer rejects most Foundation
	// records with "no energy nutrient" — which is how a mapping of
	// Foundation ids can fail wholesale while SR Legacy ids succeed.
	NutrientEnergyAtwaterSpecific = 2048
	NutrientEnergyAtwaterGeneral  = 2047
)

// Data type names, used for the Foundation -> SR Legacy -> Branded preference
// order. Exported so cmd/fdcimport can pass them to Search.
const (
	DataTypeFoundation = "Foundation"
	DataTypeSRLegacy   = "SR Legacy"
	DataTypeBranded    = "Branded"
)

// PreferredDataTypes is the search order the plan calls for: authoritative
// lab-measured data first, Branded label data only as a last resort.
var PreferredDataTypes = []string{DataTypeFoundation, DataTypeSRLegacy, DataTypeBranded}

// ------------------------------------------------------------------ requests

// Search finds foods by name, optionally restricted to certain data types.
func (c *Client) Search(ctx context.Context, query string, dataTypes []string) ([]FoodSummary, error) {
	// url.Values builds a query string with correct PERCENT-ENCODING. Never
	// concatenate query params by hand: a food named "salt & pepper" would
	// silently truncate at the &, and a space would break the URL outright.
	params := url.Values{}
	params.Set("query", query)
	params.Set("api_key", c.APIKey)
	if len(dataTypes) > 0 {
		// FDC accepts a comma-joined list in one param.
		params.Set("dataType", strings.Join(dataTypes, ","))
	}

	var out searchResponse
	if err := c.get(ctx, "/foods/search", params, &out); err != nil {
		return nil, err
	}
	return out.Foods, nil
}

// Detail fetches one food by its FDC id.
func (c *Client) Detail(ctx context.Context, fdcID int64) (FoodDetail, error) {
	params := url.Values{}
	params.Set("api_key", c.APIKey)

	// The id is part of the PATH here, not the query string. FormatInt is the
	// explicit int64->string conversion (strconv.Itoa only takes int).
	path := "/food/" + strconv.FormatInt(fdcID, 10)

	var out FoodDetail
	if err := c.get(ctx, path, params, &out); err != nil {
		return FoodDetail{}, err
	}
	return out, nil
}

// maxAttempts bounds the retry loop in get. Three is enough to ride out the
// intermittent failures observed against FDC without turning a genuinely
// missing record into a nine-second wait.
const maxAttempts = 3

// retryBackoff is the pause before attempt n+1. Short on purpose: this is
// smoothing over a flaky endpoint, not backing off from a rate limit, and the
// importer already sleeps 200ms between foods.
func retryBackoff(attempt int) time.Duration {
	return time.Duration(attempt) * 250 * time.Millisecond
}

// isRetryable reports whether a status is worth another attempt.
//
// 404 IS on this list, which is unusual enough to justify. FDC's detail
// endpoint intermittently answers 404 for ids that exist: the same id can 404,
// then return 200 seconds later with rate-limit headers showing ~3550 of 3600
// requests remaining, so it is neither quota nor a missing record. Running the
// importer three times in a row produced 20, 26, and 24 of 41 foods, with a
// different failure set each time. Without a retry those transient 404s become
// permanent skips, and the operator sees a curated mapping that looks broken.
// A record that is genuinely absent still fails, just after three tries.
func isRetryable(status int) bool {
	return status == http.StatusNotFound ||
		status == http.StatusTooManyRequests ||
		status >= 500
}

// get is the shared plumbing: build the request, send it, check the status,
// decode the body, and retry the failures worth retrying. Both public methods
// funnel through here so timeout handling, error wrapping, status checks, and
// the retry policy exist in exactly one place.
//
// dst is `any` for the same reason readJSON's was: it receives a POINTER to
// whatever struct the caller wants filled.
func (c *Client) get(ctx context.Context, path string, params url.Values, dst any) error {
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			timer := time.NewTimer(retryBackoff(attempt - 1))
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
		}

		var retryable bool
		retryable, err = c.getOnce(ctx, path, params, dst)
		if err == nil || !retryable {
			return err
		}
	}
	return fmt.Errorf("after %d attempts: %w", maxAttempts, err)
}

// getOnce performs a single attempt, reporting whether a failure is worth
// retrying. A nil error always means "done".
func (c *Client) getOnce(ctx context.Context, path string, params url.Values, dst any) (retryable bool, err error) {
	endpoint := c.BaseURL + path + "?" + params.Encode()

	// http.NewRequestWithContext, never http.NewRequest. The context is what
	// lets a caller CANCEL this request — on Ctrl-C, on a parent timeout, or
	// when a user disconnects. Without it the request runs to completion no
	// matter what, ignoring every cancellation signal in the program.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		// A transport-level failure: DNS, connection refused, timeout. This is
		// NOT how a 404 or 500 arrives — those are successful HTTP exchanges
		// that happen to carry an error status, and they come back through
		// resp below. Conflating the two is a very common bug.
		// Transport failures are worth another attempt for the same reason a
		// 5xx is: nothing about the request itself is wrong.
		return true, fmt.Errorf("requesting %s: %w", path, err)
	}
	// MANDATORY: an unclosed response body leaks the underlying TCP
	// connection, and the pool eventually starves. defer it immediately after
	// the error check — never before (resp is nil on error) and never later
	// (an early return would skip it).
	// The _ = is for errcheck: closing a response body can technically fail,
	// and there is nothing useful to do about it inside a defer. Being explicit
	// beats an exclusion rule that would also hide closes I DO care about.
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// Read a bounded amount of the error body for the message.
		// io.LimitReader caps it: a misconfigured endpoint could return a
		// 50 MB HTML error page, and we will not load that to log it.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))

		// 403 deserves its own message because it has exactly one likely
		// cause, and a good error saves ten minutes of confusion.
		if resp.StatusCode == http.StatusForbidden {
			return false, fmt.Errorf("FDC returned 403: check FDC_API_KEY")
		}
		// 429 is the documented rate limit (1000 requests/hour by default).
		if resp.StatusCode == http.StatusTooManyRequests {
			return true, fmt.Errorf("FDC returned 429: rate limited, slow down")
		}
		return isRetryable(resp.StatusCode), fmt.Errorf("FDC returned %d for %s: %s", resp.StatusCode, path, body)
	}

	// Decode straight from the response body — a STREAM. json.Unmarshal would
	// require loading the whole body into a []byte first; the decoder reads
	// incrementally.
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		// A truncated or malformed body is usually a blip, not a contract
		// change, so one more attempt is worth it.
		return true, fmt.Errorf("decoding %s response: %w", path, err)
	}

	return false, nil
}
