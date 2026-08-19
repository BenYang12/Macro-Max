package fdc

// client_test.go — tests the client against a FAKE FDC server.
//
// httptest.NewServer starts a real HTTP server on a random localhost port and
// hands back its URL. Pointing Client.BaseURL at it means the client does
// genuine network I/O — real sockets, real headers, real JSON decoding — with
// zero dependency on USDA being up, on having an API key, or on their data
// staying the same.
//
// Why not hit the real API? A test that needs the internet is slow, fails on a
// plane, breaks in CI without secrets, and starts failing the day USDA edits a
// value. The fake server tests OUR code, which is the only code we can fix.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestClient spins up a fake FDC server whose handler is supplied by the
// test, and returns a Client aimed at it.
//
// The handler parameter is an http.HandlerFunc — the same type our own
// handlers satisfy. Writing a fake SERVER is just writing a handler.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()

	srv := httptest.NewServer(handler)
	// Shut the server down when the test ends, releasing the port.
	t.Cleanup(srv.Close)

	return &Client{
		BaseURL: srv.URL, // the fake, not api.nal.usda.gov
		APIKey:  "test-key",
		HTTP:    srv.Client(), // a client preconfigured for this server
	}
}

func TestDetail_DecodesFoundationFood(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// Assert on the REQUEST the client built. This is half the value of a
		// fake server: it verifies we call the right URL with the right params,
		// not just that we can parse a response.
		if r.URL.Path != "/food/171077" {
			t.Errorf("path = %q; want /food/171077", r.URL.Path)
		}
		if got := r.URL.Query().Get("api_key"); got != "test-key" {
			t.Errorf("api_key = %q; want test-key", got)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"fdcId": 171077,
			"description": "Chicken, broilers or fryers, breast, meat only, raw",
			"dataType": "SR Legacy",
			"foodNutrients": [
				{"nutrient": {"id": 1003, "name": "Protein", "unitName": "G"}, "amount": 22.5},
				{"nutrient": {"id": 1004, "name": "Total lipid (fat)", "unitName": "G"}, "amount": 2.62},
				{"nutrient": {"id": 1005, "name": "Carbohydrate", "unitName": "G"}, "amount": 0.0},
				{"nutrient": {"id": 1008, "name": "Energy", "unitName": "KCAL"}, "amount": 120}
			]
		}`))
	})

	got, err := c.Detail(context.Background(), 171077)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}

	if got.FdcID != 171077 {
		t.Errorf("FdcID = %d; want 171077", got.FdcID)
	}
	if got.DataType != DataTypeSRLegacy {
		t.Errorf("DataType = %q; want %q", got.DataType, DataTypeSRLegacy)
	}
	if len(got.FoodNutrients) != 4 {
		t.Fatalf("got %d nutrients; want 4", len(got.FoodNutrients))
	}
	// The nested decode worked: nutrient.id came through the inner struct.
	if got.FoodNutrients[0].Nutrient.ID != NutrientProtein {
		t.Errorf("first nutrient id = %d; want %d",
			got.FoodNutrients[0].Nutrient.ID, NutrientProtein)
	}
	if got.FoodNutrients[0].Amount != 22.5 {
		t.Errorf("protein = %v; want 22.5", got.FoodNutrients[0].Amount)
	}
}

// The full pipeline in one test: fetch, then normalize. This is the seam that
// matters — the client's types feeding the pure functions.
func TestDetail_ThenNormalize(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{
			"fdcId": 171077, "dataType": "SR Legacy",
			"description": "Chicken breast",
			"foodNutrients": [
				{"nutrient": {"id": 1003, "unitName": "G"}, "amount": 22.5},
				{"nutrient": {"id": 1004, "unitName": "G"}, "amount": 2.62},
				{"nutrient": {"id": 1005, "unitName": "G"}, "amount": 0},
				{"nutrient": {"id": 1008, "unitName": "KCAL"}, "amount": 120}
			]
		}`))
	})

	detail, err := c.Detail(context.Background(), 171077)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}

	p, err := Normalize(detail)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if err := Validate(p, "protein"); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if p.ProteinG != 22.5 {
		t.Errorf("ProteinG = %v; want 22.5", p.ProteinG)
	}
}

func TestSearch_SendsQueryAndDataTypeParams(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/foods/search" {
			t.Errorf("path = %q; want /foods/search", r.URL.Path)
		}
		// url.Values decoded the escaping back to the original string, which
		// proves the client encoded it correctly on the way out.
		if got := r.URL.Query().Get("query"); got != "chicken breast" {
			t.Errorf("query = %q; want %q", got, "chicken breast")
		}
		if got := r.URL.Query().Get("dataType"); got != "Foundation,SR Legacy" {
			t.Errorf("dataType = %q; want %q", got, "Foundation,SR Legacy")
		}

		w.Write([]byte(`{"foods": [
			{"fdcId": 1, "description": "Chicken breast", "dataType": "Foundation"}
		]}`))
	})

	foods, err := c.Search(context.Background(), "chicken breast",
		[]string{DataTypeFoundation, DataTypeSRLegacy})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(foods) != 1 || foods[0].FdcID != 1 {
		t.Fatalf("unexpected results: %+v", foods)
	}
}

// A query with characters that MUST be percent-encoded. Hand-built query
// strings break here; url.Values does not.
func TestSearch_EncodesSpecialCharacters(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("query"); got != "salt & pepper 100%" {
			t.Errorf("query = %q; want %q", got, "salt & pepper 100%")
		}
		w.Write([]byte(`{"foods": []}`))
	})

	if _, err := c.Search(context.Background(), "salt & pepper 100%", nil); err != nil {
		t.Fatalf("Search: %v", err)
	}
}

// An empty result set must decode to an empty slice and no error — "nothing
// matched" is a valid answer, not a failure.
func TestSearch_EmptyResultIsNotAnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"foods": []}`))
	})

	foods, err := c.Search(context.Background(), "nonexistent", nil)
	if err != nil {
		t.Fatalf("empty results should not error: %v", err)
	}
	if len(foods) != 0 {
		t.Errorf("got %d foods; want 0", len(foods))
	}
}

// Unknown fields must be IGNORED, not rejected — the opposite of our own API's
// readJSON. We don't control FDC's schema, so tolerating additions is correct.
func TestDetail_IgnoresUnknownFields(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{
			"fdcId": 42,
			"description": "Test",
			"someFutureFieldUSDAAdded": {"nested": true},
			"foodNutrients": []
		}`))
	})

	got, err := c.Detail(context.Background(), 42)
	if err != nil {
		t.Fatalf("unknown fields should be ignored, got error: %v", err)
	}
	if got.FdcID != 42 {
		t.Errorf("FdcID = %d; want 42", got.FdcID)
	}
}

// A 403 is the "bad API key" case, and its message should say so — the error
// you're most likely to hit on a first real run.
func TestDetail_403MentionsAPIKey(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error": "invalid key"}`))
	})

	_, err := c.Detail(context.Background(), 1)
	if err == nil {
		t.Fatal("expected an error for 403; got nil")
	}
	if !strings.Contains(err.Error(), "FDC_API_KEY") {
		t.Errorf("error %q should mention FDC_API_KEY", err)
	}
}

func TestDetail_429MentionsRateLimit(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})

	_, err := c.Detail(context.Background(), 1)
	if err == nil {
		t.Fatal("expected an error for 429; got nil")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error %q should mention rate limiting", err)
	}
}

// A non-200 must be an error, not a silently empty result. This is the bug that
// bites people who forget Do() only errors on TRANSPORT failures.
func TestDetail_404IsAnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	if _, err := c.Detail(context.Background(), 999); err == nil {
		t.Fatal("expected an error for 404; got nil")
	}
}

// Malformed JSON from a 200 response must surface as a decode error.
func TestDetail_MalformedJSONIsAnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"fdcId": `))
	})

	_, err := c.Detail(context.Background(), 1)
	if err == nil {
		t.Fatal("expected a decode error; got nil")
	}
	if !strings.Contains(err.Error(), "decoding") {
		t.Errorf("error %q should mention decoding", err)
	}
}

// A cancelled context must abort the request. This is what makes Ctrl-C work
// during a long import.
func TestDetail_RespectsContextCancellation(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"fdcId": 1}`))
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel BEFORE calling, so the request never goes out

	if _, err := c.Detail(ctx, 1); err == nil {
		t.Fatal("expected an error from a cancelled context; got nil")
	}
}

// FDC's detail endpoint intermittently answers 404 for ids that exist, so the
// client retries. Without this the importer turned transient failures into
// permanent skips: three consecutive runs of the same curated mapping imported
// 20, 26, and 24 of 41 foods, each with a different failure set.
func TestGet_RetriesTransientFailures(t *testing.T) {
	for _, tc := range []struct {
		name         string
		failStatus   int
		failTimes    int
		wantAttempts int
		wantErr      bool
	}{
		{"transient 404 succeeds on retry", http.StatusNotFound, 1, 2, false},
		{"transient 500 succeeds on retry", http.StatusInternalServerError, 1, 2, false},
		{"429 is retried", http.StatusTooManyRequests, 1, 2, false},
		{"persistent 404 gives up after maxAttempts", http.StatusNotFound, 99, maxAttempts, true},
		// A bad key is not transient: retrying wastes quota and delays a clear
		// diagnosis, so 403 must fail on the first attempt.
		{"403 is not retried", http.StatusForbidden, 99, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts++
				if attempts <= tc.failTimes {
					w.WriteHeader(tc.failStatus)
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"fdcId":123,"description":"ok","dataType":"Foundation"}`))
			}))
			defer srv.Close()

			c := New("key")
			c.BaseURL = srv.URL

			got, err := c.Detail(context.Background(), 123)
			if tc.wantErr && err == nil {
				t.Fatal("expected an error")
			}
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("Detail: %v", err)
				}
				if got.FdcID != 123 {
					t.Errorf("FdcID = %d; want 123", got.FdcID)
				}
			}
			if attempts != tc.wantAttempts {
				t.Errorf("made %d attempt(s); want %d", attempts, tc.wantAttempts)
			}
		})
	}
}

// A cancelled context must stop the retry loop rather than sleeping through
// its full backoff schedule.
func TestGet_CancellationStopsRetrying(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New("key")
	c.BaseURL = srv.URL

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel after the first attempt fails, while the backoff timer is running.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	if _, err := c.Detail(ctx, 123); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want context.Canceled", err)
	}
	if attempts >= maxAttempts {
		t.Errorf("made %d attempts; cancellation should have cut the loop short", attempts)
	}
}
