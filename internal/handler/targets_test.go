package handler

// targets_test.go — unit tests for the first WRITE endpoint. No database:
// a fake TargetStore stands in, so these test the thing that actually needs
// testing here — the validation layer and its status codes.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenYang12/Macro-Max/internal/store"
)

type fakeTargetStore struct {
	created   *store.UserTarget
	createErr error
	target    store.UserTarget
	getErr    error
}

func (f *fakeTargetStore) CreateTarget(ctx context.Context, t *store.UserTarget) error {
	if f.createErr != nil {
		return f.createErr
	}
	// Imitate what Postgres does via RETURNING: stamp the generated id.
	// Without this the Location header assertion below couldn't mean anything.
	t.ID = 7
	f.created = t
	return nil
}

func (f *fakeTargetStore) GetTarget(ctx context.Context, id int64) (store.UserTarget, error) {
	return f.target, f.getErr
}

// postTarget is a helper: builds a POST with the given JSON body and runs it.
// strings.NewReader turns a string into the io.Reader that NewRequest wants
// for a body — the same interface a real network body satisfies, which is why
// readJSON can't tell the difference.
func postTarget(t *testing.T, h *TargetsHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/targets", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	return rr
}

func TestTargetsCreate_ValidReturns201AndLocation(t *testing.T) {
	fake := &fakeTargetStore{}
	h := NewTargetsHandler(fake)

	rr := postTarget(t, h, `{
		"label": "cutting",
		"protein_g_daily": 180,
		"carbs_g_daily": 200,
		"fat_g_daily": 60,
		"budget_cents_weekly": 7500
	}`)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201. body: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Location"); got != "/v1/targets/7" {
		t.Errorf("Location = %q; want %q", got, "/v1/targets/7")
	}
	if fake.created == nil {
		t.Fatal("store never received a target")
	}
	if fake.created.Label != "cutting" {
		t.Errorf("label = %q; want %q", fake.created.Label, "cutting")
	}
	if fake.created.StoreID != store.UniversityPlaceStoreID {
		t.Errorf("store_id = %q; want %q", fake.created.StoreID, store.UniversityPlaceStoreID)
	}
	// Omitted arrays must reach the store as EMPTY, never nil — the NOT NULL
	// DEFAULT '{}' columns depend on it, and a nil slice would be sent as SQL
	// NULL and rejected.
	if fake.created.DietTags == nil {
		t.Error("diet_tags reached the store as nil; want an empty slice")
	}
	if fake.created.ExcludeFoodIDs == nil {
		t.Error("exclude_food_ids reached the store as nil; want an empty slice")
	}
	// An omitted OPTIONAL field must stay nil — that's "no calorie ceiling",
	// which is meaningfully different from a ceiling of zero.
	if fake.created.CaloriesMaxDaily != nil {
		t.Errorf("calories_max_daily = %d; want nil when omitted", *fake.created.CaloriesMaxDaily)
	}
}

// The headline test for this step: MISSING fields produce 422 with one entry
// per problem, all in a single response.
func TestTargetsCreate_MissingFieldsReturn422WithAllFields(t *testing.T) {
	h := NewTargetsHandler(&fakeTargetStore{})

	rr := postTarget(t, h, `{"label": "cutting"}`)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 422", rr.Code)
	}

	var body struct {
		Error struct {
			Code   string            `json:"code"`
			Fields map[string]string `json:"fields"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}

	if body.Error.Code != "validation_failed" {
		t.Errorf("error code = %q; want %q", body.Error.Code, "validation_failed")
	}

	// Four required fields were omitted; all four must be reported at once.
	// This is the assertion that would fail if validation returned early on
	// the first problem.
	for _, want := range []string{
		"protein_g_daily", "carbs_g_daily", "fat_g_daily",
		"budget_cents_weekly",
	} {
		if _, ok := body.Error.Fields[want]; !ok {
			t.Errorf("missing %q in validation fields: %v", want, body.Error.Fields)
		}
	}
	// label WAS supplied, so it must not be reported.
	if _, ok := body.Error.Fields["label"]; ok {
		t.Error("label was provided but still reported as invalid")
	}
}

func TestTargetsCreate_NegativeMacroReturns422(t *testing.T) {
	h := NewTargetsHandler(&fakeTargetStore{})

	rr := postTarget(t, h, `{
		"label": "bad", "protein_g_daily": -5, "carbs_g_daily": 200,
		"fat_g_daily": 60, "budget_cents_weekly": 7500
	}`)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 422", rr.Code)
	}
}

func TestTargetsCreate_ZeroBudgetReturns422(t *testing.T) {
	h := NewTargetsHandler(&fakeTargetStore{})

	// Zero is the interesting case: it's a value the client really sent, so
	// the pointer is non-nil. Only the explicit <= 0 check catches it.
	rr := postTarget(t, h, `{
		"label": "broke", "protein_g_daily": 180, "carbs_g_daily": 200,
		"fat_g_daily": 60, "budget_cents_weekly": 0
	}`)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 422", rr.Code)
	}
}

// The cross-field rule: a calorie ceiling below the macros it must contain.
// 180*4 + 200*4 + 60*9 = 2060 kcal, so a 1000 kcal cap is self-contradictory.
func TestTargetsCreate_CalorieCapBelowMacrosReturns422(t *testing.T) {
	h := NewTargetsHandler(&fakeTargetStore{})

	rr := postTarget(t, h, `{
		"label": "impossible", "protein_g_daily": 180, "carbs_g_daily": 200,
		"fat_g_daily": 60, "calories_max_daily": 1000,
		"budget_cents_weekly": 7500
	}`)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 422", rr.Code)
	}
}

// The mirror of the test above: a ceiling ABOVE the implied macros is fine,
// which proves the cross-field check isn't just rejecting everything.
func TestTargetsCreate_CalorieCapAboveMacrosIsAccepted(t *testing.T) {
	fake := &fakeTargetStore{}
	h := NewTargetsHandler(fake)

	rr := postTarget(t, h, `{
		"label": "sensible", "protein_g_daily": 180, "carbs_g_daily": 200,
		"fat_g_daily": 60, "calories_max_daily": 2400,
		"budget_cents_weekly": 7500
	}`)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201. body: %s", rr.Code, rr.Body.String())
	}
	if fake.created.CaloriesMaxDaily == nil || *fake.created.CaloriesMaxDaily != 2400 {
		t.Error("calories_max_daily did not reach the store as 2400")
	}
}

// The 400/422 boundary, from the 400 side. Malformed JSON is UNREADABLE, so
// it must be rejected before validation ever runs.
func TestTargetsCreate_MalformedJSONReturns400(t *testing.T) {
	h := NewTargetsHandler(&fakeTargetStore{})

	rr := postTarget(t, h, `{"label": "cutting"`) // truncated on purpose

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rr.Code)
	}
}

// DisallowUnknownFields from step 5, proven end to end. This is the guard
// against silently ignoring a client's typo'd field name.
func TestTargetsCreate_UnknownFieldReturns400(t *testing.T) {
	h := NewTargetsHandler(&fakeTargetStore{})

	rr := postTarget(t, h, `{
		"label": "cutting", "protein_g_daily": 180, "carbs_g_daily": 200,
		"fat_g_daily": 60, "budget_cents_weekly": 7500,
		"sneaky": true
	}`)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rr.Code)
	}
}

func TestTargetsCreate_RejectsClientStoreID(t *testing.T) {
	h := NewTargetsHandler(&fakeTargetStore{})
	rr := postTarget(t, h, `{
		"label": "cutting", "protein_g_daily": 180, "carbs_g_daily": 200,
		"fat_g_daily": 60, "budget_cents_weekly": 7500,
		"store_id": "another-store"
	}`)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rr.Code)
	}
}

// Wrong JSON TYPE is also unreadable (400), not merely invalid (422) — the
// decoder fails before any validation rule gets a chance to look at it.
func TestTargetsCreate_WrongTypeReturns400(t *testing.T) {
	h := NewTargetsHandler(&fakeTargetStore{})

	rr := postTarget(t, h, `{"label": "cutting", "protein_g_daily": "lots"}`)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rr.Code)
	}
}

func TestTargetsCreate_EmptyBodyReturns400(t *testing.T) {
	h := NewTargetsHandler(&fakeTargetStore{})

	rr := postTarget(t, h, ``)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rr.Code)
	}
}

func TestTargetsGet_UnknownIDReturns404(t *testing.T) {
	h := NewTargetsHandler(&fakeTargetStore{getErr: store.ErrNotFound})

	req := httptest.NewRequest(http.MethodGet, "/v1/targets/42", nil)
	req.SetPathValue("id", "42")
	rr := httptest.NewRecorder()

	h.Get(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404", rr.Code)
	}
}

func TestTargetsGet_ReturnsTargetEnvelope(t *testing.T) {
	h := NewTargetsHandler(&fakeTargetStore{
		target: store.UserTarget{ID: 3, Label: "bulk", BudgetCentsWeekly: 9000},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/targets/3", nil)
	req.SetPathValue("id", "3")
	rr := httptest.NewRecorder()

	h.Get(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rr.Code)
	}

	var body struct {
		Target store.UserTarget `json:"target"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Target.Label != "bulk" {
		t.Errorf("label = %q; want %q", body.Target.Label, "bulk")
	}
}
