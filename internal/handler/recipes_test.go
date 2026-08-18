package handler

// recipes_test.go — unit tests for POST /v1/recipes. No database and, more
// importantly, NO CLAUDE: both dependencies are interfaces, so a fake stands in
// for each. A test suite that charges per run is a test suite that quietly
// stops being run, and one that needs a network is one that fails on a plane.
//
// What's actually worth testing here isn't "does Claude write good recipes" —
// that isn't a property a unit test can assert. It's the translation layer I
// wrote: the aggregation, the status codes, and the error mapping.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenYang12/Macro-Max/internal/recipes"
	"github.com/BenYang12/Macro-Max/internal/store"
)

type fakeRecipeStore struct {
	target    store.UserTarget
	targetErr error
	lines     []store.BasketLine
	basketErr error
	gotDigest []byte
}

func (f *fakeRecipeStore) GetTarget(ctx context.Context, id int64, digest []byte) (store.UserTarget, error) {
	f.gotDigest = append([]byte(nil), digest...)
	return f.target, f.targetErr
}

func (f *fakeRecipeStore) LatestBasketForTarget(ctx context.Context, targetID int64) (store.Basket, []store.BasketLine, error) {
	if f.basketErr != nil {
		return store.Basket{}, nil, f.basketErr
	}
	return store.Basket{ID: 1, TargetID: targetID}, f.lines, nil
}

type fakeGenerator struct {
	// gotReq captures what the handler actually sent, which is the only way to
	// assert on the aggregation — the plan coming back says nothing about it.
	gotReq recipes.Request
	plan   recipes.Plan
	err    error
}

func (f *fakeGenerator) Generate(ctx context.Context, req recipes.Request) (recipes.Plan, error) {
	f.gotReq = req
	return f.plan, f.err
}

func postRecipes(t *testing.T, h *RecipesHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/recipes", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testCapabilityToken)
	req.Header.Set("X-Recipe-Key", "test-recipe-key")
	rr := httptest.NewRecorder()
	h.Generate(rr, req)
	return rr
}

func TestRecipes_RequiresDeploymentAccessKey(t *testing.T) {
	h := NewRecipesHandler(recipeStoreFake(), &fakeGenerator{plan: recipePlanFake()}, "correct-key")
	for _, tc := range []struct {
		name, key string
		want      int
	}{
		{"missing", "", http.StatusUnauthorized},
		{"wrong", "wrong-key", http.StatusUnauthorized},
		{"correct", "correct-key", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/recipes", strings.NewReader(`{"target_id":7}`))
			req.Header.Set("Authorization", "Bearer "+testCapabilityToken)
			if tc.key != "" {
				req.Header.Set("X-Recipe-Key", tc.key)
			}
			rr := httptest.NewRecorder()
			h.Generate(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("status = %d; want %d", rr.Code, tc.want)
			}
		})
	}
}

func TestRecipes_RejectsMissingMalformedAndWrongCapabilities(t *testing.T) {
	for _, tc := range []struct{ name, token string }{
		{"missing", ""}, {"malformed", "not-base64!"}, {"wrong", testWrongCapabilityToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := recipeStoreFake()
			if tc.name == "wrong" {
				st.targetErr = store.ErrNotFound
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/recipes", strings.NewReader(`{"target_id":7}`))
			req.Header.Set("X-Recipe-Key", "test-recipe-key")
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rr := httptest.NewRecorder()
			NewRecipesHandler(st, &fakeGenerator{}, "test-recipe-key").Generate(rr, req)
			if rr.Code != http.StatusNotFound {
				t.Fatalf("status = %d; want 404", rr.Code)
			}
		})
	}
}

// recipeStoreFake builds a store fake whose basket contains the SAME FOOD TWICE, under
// two different products. That's the interesting case, not an edge case: the
// MILP chooses pack sizes independently, so a basket routinely holds two bag
// sizes of one food.
func recipeStoreFake() *fakeRecipeStore {
	return &fakeRecipeStore{
		target: store.UserTarget{
			ID: 7, ProteinGDaily: 180, CarbsGDaily: 200, FatGDaily: 60,
			DietTags: []string{"vegan"},
		},
		lines: []store.BasketLine{
			{ProductID: 1, FoodName: "Lentils, dry", ProductName: "Store Brand Lentils 454g", Grams: 908, CostCents: 400},
			{ProductID: 2, FoodName: "Broccoli, frozen", ProductName: "Frozen Broccoli 340g", Grams: 680, CostCents: 300},
			{ProductID: 3, FoodName: "Lentils, dry", ProductName: "Bulk Lentils 907g", Grams: 907, CostCents: 250},
		},
	}
}

func recipePlanFake() recipes.Plan {
	return recipes.Plan{
		Meals: []recipes.Meal{{
			Name: "Lentil curry", Servings: 6,
			Ingredients: []string{"900g lentils"},
			Steps:       []string{"Simmer."},
			PrepMinutes: 40,
		}},
		Notes: []string{"Freezes well."},
	}
}

func TestRecipes_AggregatesGramsByFood(t *testing.T) {
	gen := &fakeGenerator{plan: recipePlanFake()}
	h := NewRecipesHandler(recipeStoreFake(), gen, "test-recipe-key")

	rr := postRecipes(t, h, `{"target_id": 7}`)
	if string(h.Store.(*fakeRecipeStore).gotDigest) != string(testCapabilityDigest()) {
		t.Fatal("recipes did not propagate the bearer token's exact digest")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// Three basket lines, two distinct foods. If this is 3, the aggregation
	// didn't happen and the model would see lentils as two ingredients.
	if len(gen.gotReq.Ingredients) != 2 {
		t.Fatalf("ingredients = %d, want 2 (three lines, two foods): %+v",
			len(gen.gotReq.Ingredients), gen.gotReq.Ingredients)
	}

	// FIRST-APPEARANCE ORDER, not map order. This assertion is the whole reason
	// the handler tracks `order` separately: ranging a Go map is randomized, so
	// without it this test would pass and fail at random — and worse, the
	// prompt would differ between two identical requests in production.
	if got := gen.gotReq.Ingredients[0].Food; got != "Lentils, dry" {
		t.Errorf("first ingredient = %q, want %q", got, "Lentils, dry")
	}

	// 908 + 907, summed across the two lentil products.
	if got := gen.gotReq.Ingredients[0].Grams; got != 1815 {
		t.Errorf("lentil grams = %v, want 1815", got)
	}
	if got := gen.gotReq.Ingredients[1].Grams; got != 680 {
		t.Errorf("broccoli grams = %v, want 680", got)
	}
}

func TestRecipes_PassesTargetsAndDietTags(t *testing.T) {
	gen := &fakeGenerator{plan: recipePlanFake()}
	h := NewRecipesHandler(recipeStoreFake(), gen, "test-recipe-key")

	postRecipes(t, h, `{"target_id": 7}`)

	if gen.gotReq.ProteinGDaily != 180 || gen.gotReq.CarbsGDaily != 200 || gen.gotReq.FatGDaily != 60 {
		t.Errorf("macros not forwarded: %+v", gen.gotReq)
	}
	if len(gen.gotReq.DietTags) != 1 || gen.gotReq.DietTags[0] != "vegan" {
		t.Errorf("diet tags = %v, want [vegan]", gen.gotReq.DietTags)
	}
}

func TestRecipes_ReturnsThePlan(t *testing.T) {
	h := NewRecipesHandler(recipeStoreFake(), &fakeGenerator{plan: recipePlanFake()}, "test-recipe-key")

	rr := postRecipes(t, h, `{"target_id": 7}`)

	var body struct {
		Plan recipes.Plan `json:"plan"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(body.Plan.Meals) != 1 || body.Plan.Meals[0].Name != "Lentil curry" {
		t.Errorf("plan not returned intact: %+v", body.Plan)
	}
}

func TestRecipes_MissingTargetIDIs422(t *testing.T) {
	h := NewRecipesHandler(recipeStoreFake(), &fakeGenerator{plan: recipePlanFake()}, "test-recipe-key")

	rr := postRecipes(t, h, `{}`)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rr.Code)
	}
}

func TestRecipes_UnknownFieldIs400(t *testing.T) {
	h := NewRecipesHandler(recipeStoreFake(), &fakeGenerator{plan: recipePlanFake()}, "test-recipe-key")

	// DisallowUnknownFields is on globally, and this asserts the recipes route
	// inherits it. 400, not 422: the body itself is unreadable, not merely wrong.
	rr := postRecipes(t, h, `{"target_id": 7, "spice_level": "hot"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestRecipes_UnknownTargetIs404(t *testing.T) {
	st := recipeStoreFake()
	st.targetErr = store.ErrNotFound
	h := NewRecipesHandler(st, &fakeGenerator{plan: recipePlanFake()}, "test-recipe-key")

	rr := postRecipes(t, h, `{"target_id": 999}`)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestRecipes_UnsolvedTargetIs422NotFoundish(t *testing.T) {
	st := recipeStoreFake()
	st.basketErr = store.ErrNotFound
	h := NewRecipesHandler(st, &fakeGenerator{plan: recipePlanFake()}, "test-recipe-key")

	rr := postRecipes(t, h, `{"target_id": 7}`)

	// The distinction this test exists to protect: the TARGET was found, the
	// BASKET wasn't. Collapsing both into 404 sends the user hunting for a bad
	// id when the real fix is "solve first".
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "/v1/solve") {
		t.Errorf("message should point at the next action; got: %s", rr.Body.String())
	}
}

func TestRecipes_RefusalIs422NotServerError(t *testing.T) {
	h := NewRecipesHandler(recipeStoreFake(), &fakeGenerator{err: recipes.ErrRefused}, "test-recipe-key")

	rr := postRecipes(t, h, `{"target_id": 7}`)

	// A refusal is a SUCCESSFUL model call with a declined outcome. Reporting
	// it as 500 would blame my server for something that didn't break, and
	// would invite a client retry that can only reach the same answer.
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "model_refused") {
		t.Errorf("expected model_refused code; got: %s", rr.Body.String())
	}
}

func TestRecipes_GeneratorFailureIs500(t *testing.T) {
	h := NewRecipesHandler(recipeStoreFake(), &fakeGenerator{err: errors.New("connection reset")}, "test-recipe-key")

	rr := postRecipes(t, h, `{"target_id": 7}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	// The 500 body must stay generic. A transport error can carry a URL with a
	// key in it, and error bodies get pasted into bug reports.
	if strings.Contains(rr.Body.String(), "connection reset") {
		t.Errorf("internal error text leaked to the client: %s", rr.Body.String())
	}
}
