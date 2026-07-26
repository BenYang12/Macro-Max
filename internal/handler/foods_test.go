package handler

// foods_test.go — UNIT tests for the food handlers. No database: a fake
// FoodStore feeds canned answers, so these test routing, status codes, and
// JSON shape in microseconds. The real store gets exercised separately by the
// integration test in the store package.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenYang12/Macro-Max/internal/store"
)

// fakeFoodStore implements the handler's FoodStore interface by hand. Each
// field lets a test PRECONFIGURE what the method returns — including errors,
// so I can drive the 404 and 500 paths without a real failure.
//
// This is the LGWT-preferred style: a hand-written fake I fully control, not
// a mocking framework. It's just a struct.
type fakeFoodStore struct {
	foods     []store.Food     // returned by ListFoods
	food      store.Food       // returned by GetFood on success
	listErr   error            // if set, ListFoods returns it
	getErr    error            // if set, GetFood returns it
	gotID     int64            // records the id GetFood was called with (spying)
	gotFilter store.FoodFilter // records the filter ListFoods was called with
}

// These two methods are ALL it takes for *fakeFoodStore to satisfy FoodStore.
// Same method names, same signatures as the interface -> Go accepts it.
func (f *fakeFoodStore) ListFoods(ctx context.Context, filter store.FoodFilter) ([]store.Food, error) {
	f.gotFilter = filter // record what the handler passed, to assert on later
	return f.foods, f.listErr
}

func (f *fakeFoodStore) GetFood(ctx context.Context, id int64) (store.Food, error) {
	f.gotID = id
	return f.food, f.getErr
}

func TestFoodsList_ReturnsFoodsEnvelope(t *testing.T) {
	// Arrange: a fake preloaded with one food.
	fake := &fakeFoodStore{
		foods: []store.Food{{ID: 1, Name: "Chicken Breast", Category: "protein"}},
	}
	h := NewFoodsHandler(fake)

	// httptest.NewRequest builds a request without a network. The query
	// string exercises filter parsing. httptest.NewRecorder is a fake
	// ResponseWriter that captures whatever the handler writes.
	req := httptest.NewRequest(http.MethodGet, "/v1/foods?category=protein", nil)
	rr := httptest.NewRecorder()

	// Act: call the handler directly — no server, no port.
	h.List(rr, req)

	// Assert status.
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d", rr.Code, http.StatusOK)
	}

	// Assert the handler forwarded the query param into the filter.
	if fake.gotFilter.Category != "protein" {
		t.Errorf("store got category %q; want %q", fake.gotFilter.Category, "protein")
	}

	// Assert the body is the {"foods": [...]} envelope. Decoding into a typed
	// struct proves the KEY is "foods" and the value is a food array.
	var body struct {
		Foods []store.Food `json:"foods"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response body: %v", err)
	}
	if len(body.Foods) != 1 || body.Foods[0].Name != "Chicken Breast" {
		t.Errorf("unexpected body: %+v", body.Foods)
	}
}

func TestFoodsGet_UnknownIDReturns404(t *testing.T) {
	// A fake wired to return the store's not-found sentinel.
	fake := &fakeFoodStore{getErr: store.ErrNotFound}
	h := NewFoodsHandler(fake)

	// A request WITH a path value. Because I call the method directly (no
	// router to fill {id}), I set the path value myself with SetPathValue —
	// that's the seam that makes r.PathValue("id") return "42" inside Get.
	req := httptest.NewRequest(http.MethodGet, "/v1/foods/42", nil)
	req.SetPathValue("id", "42")
	rr := httptest.NewRecorder()

	h.Get(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want %d", rr.Code, http.StatusNotFound)
	}
	// The handler should have parsed "42" and passed 42 to the store.
	if fake.gotID != 42 {
		t.Errorf("store got id %d; want 42", fake.gotID)
	}
}

func TestFoodsGet_NonNumericIDReturns404(t *testing.T) {
	// No store error configured — the handler must reject "banana" BEFORE
	// ever calling the store, purely from the failed ParseInt.
	fake := &fakeFoodStore{}
	h := NewFoodsHandler(fake)

	req := httptest.NewRequest(http.MethodGet, "/v1/foods/banana", nil)
	req.SetPathValue("id", "banana")
	rr := httptest.NewRecorder()

	h.Get(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want %d", rr.Code, http.StatusNotFound)
	}
}
