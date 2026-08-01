package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenYang12/Macro-Max/internal/kroger"
)

type fakeStoreFinder struct {
	locations []kroger.Location
	err       error
	gotZip    string
	calls     int
}

func (f *fakeStoreFinder) Locations(ctx context.Context, zip string, limit int) ([]kroger.Location, error) {
	f.calls++
	f.gotZip = zip
	return f.locations, f.err
}

func getStores(t *testing.T, h *StoresHandler, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/stores"+query, nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	return rr
}

func TestStores_ReturnsReshapedLocations(t *testing.T) {
	fake := &fakeStoreFinder{locations: []kroger.Location{{
		LocationID: "01400376", Name: "Kroger Clifton", Chain: "KROGER",
		Address: kroger.Address{AddressLine1: "123 Main St", City: "Cincinnati", State: "OH", ZipCode: "45202"},
	}}}
	h := NewStoresHandler(fake)

	rr := getStores(t, h, "?zip=45202")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rr.Code)
	}
	if fake.gotZip != "45202" {
		t.Errorf("zip = %q; want 45202", fake.gotZip)
	}

	var body struct {
		Stores []struct {
			LocationID string `json:"location_id"`
			Name       string `json:"name"`
			City       string `json:"city"`
		} `json:"stores"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(body.Stores) != 1 {
		t.Fatalf("got %d stores; want 1", len(body.Stores))
	}
	// snake_case, my names — not Kroger's locationId.
	if body.Stores[0].LocationID != "01400376" {
		t.Errorf("location_id = %q", body.Stores[0].LocationID)
	}
	if body.Stores[0].City != "Cincinnati" {
		t.Errorf("city = %q", body.Stores[0].City)
	}
}

// A malformed zip must be rejected BEFORE calling Kroger — a wasted request is
// a wasted slice of my daily quota.
func TestStores_BadZipNeverCallsKroger(t *testing.T) {
	for _, zip := range []string{"", "1234", "abcde", "123456", "45202-1234"} {
		t.Run("zip="+zip, func(t *testing.T) {
			fake := &fakeStoreFinder{}
			h := NewStoresHandler(fake)

			rr := getStores(t, h, "?zip="+zip)

			if rr.Code != http.StatusUnprocessableEntity {
				t.Errorf("status = %d; want 422", rr.Code)
			}
			if fake.calls != 0 {
				t.Error("Kroger was called with an invalid zip")
			}
		})
	}
}

func TestStores_EmptyResultIsArrayNotNull(t *testing.T) {
	h := NewStoresHandler(&fakeStoreFinder{locations: nil})

	rr := getStores(t, h, "?zip=99999")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 — no stores nearby is a valid answer", rr.Code)
	}
	var body struct {
		Stores []any `json:"stores"`
	}
	json.NewDecoder(rr.Body).Decode(&body)
	if body.Stores == nil {
		t.Error("stores decoded as nil; want an empty array")
	}
}

func TestStores_KrogerFailureIs500(t *testing.T) {
	h := NewStoresHandler(&fakeStoreFinder{err: errors.New("kroger down")})

	if rr := getStores(t, h, "?zip=45202"); rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500", rr.Code)
	}
}
