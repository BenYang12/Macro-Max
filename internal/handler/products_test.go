package handler

// products_test.go — unit tests with a fake ProductStore. No database.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenYang12/Macro-Max/internal/store"
)

type fakeProductStore struct {
	products  []store.Product
	product   store.Product
	listErr   error
	getErr    error
	gotID     int64
	gotFilter store.ProductFilter
}

func (f *fakeProductStore) ListProducts(ctx context.Context, filter store.ProductFilter) ([]store.Product, error) {
	f.gotFilter = filter
	return f.products, f.listErr
}

func (f *fakeProductStore) GetProduct(ctx context.Context, id int64) (store.Product, error) {
	f.gotID = id
	return f.product, f.getErr
}

func TestProductsList_UsesFixedStoreAndParsesFoodFilter(t *testing.T) {
	fake := &fakeProductStore{
		products: []store.Product{{ID: 1, Name: "Test Chicken", FoodName: "Chicken Breast"}},
	}
	h := NewProductsHandler(fake)

	req := httptest.NewRequest(http.MethodGet, "/v1/products?food_id=7", nil)
	rr := httptest.NewRecorder()

	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d", rr.Code, http.StatusOK)
	}
	if fake.gotFilter.StoreID != store.UniversityPlaceStoreID {
		t.Errorf("store_id = %q; want %q", fake.gotFilter.StoreID, store.UniversityPlaceStoreID)
	}
	// FoodID is a POINTER, so check for nil BEFORE dereferencing — a nil
	// dereference is a panic, which would crash the whole test binary rather
	// than failing one test.
	if fake.gotFilter.FoodID == nil {
		t.Fatal("food_id filter was nil; want a pointer to 7")
	}
	if *fake.gotFilter.FoodID != 7 { // * reads THROUGH the pointer
		t.Errorf("food_id = %d; want 7", *fake.gotFilter.FoodID)
	}
}

func TestProductsList_RejectsStoreID(t *testing.T) {
	fake := &fakeProductStore{}
	h := NewProductsHandler(fake)
	req := httptest.NewRequest(http.MethodGet, "/v1/products?store_id=other", nil)
	rr := httptest.NewRecorder()

	h.List(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d", rr.Code, http.StatusBadRequest)
	}
}

// The absent-filter case: no food_id in the query string means the store must
// receive nil, i.e. "don't filter by food" — NOT a zero.
func TestProductsList_OmittedFoodIDIsNil(t *testing.T) {
	fake := &fakeProductStore{}
	h := NewProductsHandler(fake)

	req := httptest.NewRequest(http.MethodGet, "/v1/products", nil)
	rr := httptest.NewRecorder()

	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d", rr.Code, http.StatusOK)
	}
	if fake.gotFilter.FoodID != nil {
		t.Errorf("food_id = %v; want nil when the param is absent", *fake.gotFilter.FoodID)
	}
}

// The garbage-filter case: 400, not 404 — and the store is never called.
func TestProductsList_BadFoodIDReturns400(t *testing.T) {
	fake := &fakeProductStore{}
	h := NewProductsHandler(fake)

	req := httptest.NewRequest(http.MethodGet, "/v1/products?food_id=banana", nil)
	rr := httptest.NewRecorder()

	h.List(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d", rr.Code, http.StatusBadRequest)
	}
}

// Empty results must serialize as [] so a frontend can map over it safely.
func TestProductsList_EmptyIsArrayNotNull(t *testing.T) {
	// The fake's zero value has a nil products slice — the exact situation
	// that would produce "products": null if the store forgot []Product{}.
	// Since the handler passes the slice straight through, this test documents
	// the contract at the API boundary.
	fake := &fakeProductStore{products: []store.Product{}}
	h := NewProductsHandler(fake)

	req := httptest.NewRequest(http.MethodGet, "/v1/products", nil)
	rr := httptest.NewRecorder()

	h.List(rr, req)

	var body struct {
		Products []store.Product `json:"products"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response body: %v", err)
	}
	if body.Products == nil {
		t.Error("products decoded as nil; want an empty array")
	}
}

func TestProductsGet_UnknownIDReturns404(t *testing.T) {
	fake := &fakeProductStore{getErr: store.ErrNotFound}
	h := NewProductsHandler(fake)

	req := httptest.NewRequest(http.MethodGet, "/v1/products/42", nil)
	req.SetPathValue("id", "42")
	rr := httptest.NewRecorder()

	h.Get(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want %d", rr.Code, http.StatusNotFound)
	}
	if fake.gotID != 42 {
		t.Errorf("store got id %d; want 42", fake.gotID)
	}
}

func TestProductsGet_OtherStoreReturns404(t *testing.T) {
	fake := &fakeProductStore{product: store.Product{ID: 42, StoreID: "other-store"}}
	h := NewProductsHandler(fake)
	req := httptest.NewRequest(http.MethodGet, "/v1/products/42", nil)
	req.SetPathValue("id", "42")
	rr := httptest.NewRecorder()

	h.Get(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want %d", rr.Code, http.StatusNotFound)
	}
}
