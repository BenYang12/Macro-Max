package solver

// Unit tests for the conversion layer. No gRPC, no network — BuildRequest is a
// pure function, and it's where every unit rule in the project gets enforced,
// so it's the most valuable thing in this package to test.

import (
	"math"
	"strings"
	"testing"

	"github.com/BenYang12/Macro-Max/internal/store"
)

// Float equality is a trap I walked straight into writing these tests:
// 2.6/100 is 0.026000000000000002, not 0.026, because neither value is exactly
// representable in binary floating point. Division is EXACTLY where that shows
// up, and this conversion layer is nothing but division. So every float
// assertion here goes through a tolerance.
func closeTo(t *testing.T, got, want float64, label string) {
	t.Helper()
	const tol = 1e-9
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %v; want %v", label, got, want)
	}
}

func ptrF(v float64) *float64 { return &v }
func ptrI(v int) *int         { return &v }

// A minimal fixture: one food, one product, one target.
func fixture() SolveInput {
	return SolveInput{
		Target: store.UserTarget{
			ProteinGDaily:     180,
			CarbsGDaily:       200,
			FatGDaily:         60,
			BudgetCentsWeekly: 7500,
			StoreID:           store.UniversityPlaceStoreID,
		},
		Foods: map[int64]store.Food{
			1: {
				ID: 1, Name: "Chicken Breast, raw", Category: "protein",
				KcalPer100g: 120, ProteinGPer100g: 22.5, CarbsGPer100g: 0, FatGPer100g: 2.6,
			},
		},
		Products: []store.Product{
			{
				ID: 10, FoodID: 1, Name: "Chicken 3lb",
				NetWeightG: 1360.8, PriceCents: 1047, EffectivePriceCents: 1047,
				Available: true,
			},
		},
	}
}

// The per-100g -> per-gram conversion. If this is wrong every macro is off by
// 100x, and the solver would confidently return a basket of three grams of
// chicken.
func TestBuildRequest_ConvertsNutritionToPerGram(t *testing.T) {
	req, err := BuildRequest(fixture())
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if len(req.Foods) != 1 {
		t.Fatalf("got %d foods; want 1", len(req.Foods))
	}

	f := req.Foods[0]
	closeTo(t, f.ProteinPerG, 0.225, "ProteinPerG (22.5g per 100g)")
	closeTo(t, f.KcalPerG, 1.2, "KcalPerG (120 kcal per 100g)")
	closeTo(t, f.FatPerG, 0.026, "FatPerG (2.6g per 100g)")
}

// The daily -> weekly conversion. Getting this wrong by a factor of 7 would
// produce a basket that looks plausible and starves the user.
func TestBuildRequest_ConvertsTargetsToWeekly(t *testing.T) {
	req, err := BuildRequest(fixture())
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}

	if req.Targets.ProteinG != 1260 {
		t.Errorf("ProteinG = %v; want 1260 (180/day x 7)", req.Targets.ProteinG)
	}
	if req.Targets.CarbsG != 1400 {
		t.Errorf("CarbsG = %v; want 1400", req.Targets.CarbsG)
	}
	if req.Targets.FatG != 420 {
		t.Errorf("FatG = %v; want 420", req.Targets.FatG)
	}
	// The budget is ALREADY weekly in my schema (hence the column name), so it
	// must NOT be multiplied. This assertion is here because multiplying it
	// would be an easy and very expensive mistake.
	if req.BudgetCents != 7500 {
		t.Errorf("BudgetCents = %v; want 7500 unchanged (already weekly)", req.BudgetCents)
	}
}

func TestBuildRequest_CaloriesMaxIsWeeklyWhenSet(t *testing.T) {
	in := fixture()
	in.Target.CaloriesMaxDaily = ptrI(2400)

	req, err := BuildRequest(in)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if req.Targets.CaloriesMax != 16800 {
		t.Errorf("CaloriesMax = %v; want 16800 (2400 x 7)", req.Targets.CaloriesMax)
	}
}

// nil calories must become 0, which the CONTRACT defines as "derive a ceiling",
// not "unlimited". Documenting that distinction with a test because it's the
// kind of sentinel that gets misread later.
func TestBuildRequest_NilCaloriesBecomesZeroSentinel(t *testing.T) {
	req, err := BuildRequest(fixture())
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if req.Targets.CaloriesMax != 0 {
		t.Errorf("CaloriesMax = %v; want 0 (the derive-it sentinel)", req.Targets.CaloriesMax)
	}
}

// The promo price must win, because that's what I'd actually pay.
func TestBuildRequest_UsesEffectivePrice(t *testing.T) {
	in := fixture()
	in.Products[0].PriceCents = 1047
	in.Products[0].EffectivePriceCents = 799 // on sale

	req, err := BuildRequest(in)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if req.Foods[0].PackPriceCents != 799 {
		t.Errorf("PackPriceCents = %d; want 799 (the promo price)", req.Foods[0].PackPriceCents)
	}
}

func TestBuildRequest_NilMaxGramsBecomesZero(t *testing.T) {
	req, err := BuildRequest(fixture())
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if req.Foods[0].MaxGramsWeek != 0 {
		t.Errorf("MaxGramsWeek = %v; want 0 (uncapped)", req.Foods[0].MaxGramsWeek)
	}
}

func TestBuildRequest_PassesThroughMaxGramsWhenSet(t *testing.T) {
	in := fixture()
	f := in.Foods[1]
	f.MaxGramsPerWeek = ptrF(1400)
	in.Foods[1] = f

	req, err := BuildRequest(in)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if req.Foods[0].MaxGramsWeek != 1400 {
		t.Errorf("MaxGramsWeek = %v; want 1400", req.Foods[0].MaxGramsWeek)
	}
}

// Unavailable and mispriced products get filtered out rather than sent to the
// solver, which keeps its input clean and its errors meaningful.
func TestBuildRequest_FiltersUnusableProducts(t *testing.T) {
	in := fixture()
	in.Products = append(in.Products,
		store.Product{ID: 11, FoodID: 1, NetWeightG: 500, EffectivePriceCents: 100, Available: false},
		store.Product{ID: 12, FoodID: 1, NetWeightG: 500, EffectivePriceCents: 0, Available: true},
		store.Product{ID: 13, FoodID: 1, NetWeightG: 0, EffectivePriceCents: 100, Available: true},
	)

	req, err := BuildRequest(in)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if len(req.Foods) != 1 {
		t.Errorf("got %d foods; want 1 (three products should be filtered)", len(req.Foods))
	}
}

// Distinctness counts foods, not products, so food_id has to survive
// the conversion intact. Two pack sizes of one rice must share a food_id.
func TestBuildRequest_PreservesFoodIDForDistinctness(t *testing.T) {
	in := fixture()
	in.Foods[2] = store.Food{
		ID: 2, Name: "White Rice", Category: "carb",
		KcalPer100g: 365, ProteinGPer100g: 7.1, CarbsGPer100g: 80, FatGPer100g: 0.7,
	}
	in.Products = append(in.Products,
		store.Product{ID: 20, FoodID: 2, Name: "Rice 2lb", NetWeightG: 907.2, EffectivePriceCents: 249, Available: true},
		store.Product{ID: 21, FoodID: 2, Name: "Rice 10lb", NetWeightG: 4536, EffectivePriceCents: 899, Available: true},
	)

	req, err := BuildRequest(in)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}

	byProduct := map[int64]int64{}
	for _, f := range req.Foods {
		byProduct[f.ProductId] = f.FoodId
	}
	if byProduct[20] != byProduct[21] {
		t.Error("the two rice pack sizes must share a food_id, or variety counting breaks")
	}
	if byProduct[20] == byProduct[10] {
		t.Error("rice and chicken must have different food_ids")
	}
}

func TestBuildRequest_ErrorsWhenNoProducts(t *testing.T) {
	in := fixture()
	in.Products = nil

	_, err := BuildRequest(in)
	if err == nil {
		t.Fatal("expected an error for an empty product list")
	}
	if !strings.Contains(err.Error(), "no products") {
		t.Errorf("error = %q; want it to mention no products", err)
	}
}

// A product referencing a food I never loaded means the caller's query is
// wrong. Failing loudly beats quietly solving over a partial catalog.
func TestBuildRequest_ErrorsOnMissingFood(t *testing.T) {
	in := fixture()
	in.Products = append(in.Products,
		store.Product{ID: 99, FoodID: 404, NetWeightG: 500, EffectivePriceCents: 100, Available: true})

	_, err := BuildRequest(in)
	if err == nil {
		t.Fatal("expected an error for a product with an unloaded food")
	}
	if !strings.Contains(err.Error(), "not loaded") {
		t.Errorf("error = %q; want it to mention the unloaded food", err)
	}
}

func TestBuildRequest_ErrorsWhenEverythingFiltered(t *testing.T) {
	in := fixture()
	in.Products[0].Available = false

	_, err := BuildRequest(in)
	if err == nil {
		t.Fatal("expected an error when every product is filtered out")
	}
	if !strings.Contains(err.Error(), "after filtering") {
		t.Errorf("error = %q; want it to mention filtering", err)
	}
}

func TestBuildRequest_SetsIntegerPacksFlag(t *testing.T) {
	in := fixture()

	req, err := BuildRequest(in)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if !req.Options.IntegerPacks {
		t.Error("whole-pack optimization must always be enabled")
	}
	if req.Options.MinProteinSources != defaultMinProteinSources ||
		req.Options.MinVegetables != defaultMinVegetables ||
		req.Options.MinFruits != defaultMinFruits {
		t.Error("product variety defaults were not applied")
	}
}
