package store

// targets_integration_test.go — the round trip that matters: write a row,
// read it back, prove nothing was lost or mangled in between. Arrays and
// nullable columns are the parts most likely to break, so both are covered.

import (
	"context"
	"errors"
	"testing"
)

func TestCreateAndGetTarget_RoundTrip(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	calories := 2400
	target := UserTarget{
		Label:             "__test_cutting__",
		ProteinGDaily:     180,
		CarbsGDaily:       200,
		FatGDaily:         60,
		CaloriesMaxDaily:  &calories,
		BudgetCentsWeekly: 7500,
		StoreID:           UniversityPlaceStoreID,
		DietTags:          []string{"gluten_free"},
		ExcludeFoodIDs:    []int64{1, 2, 3},
	}

	if err := st.CreateTarget(ctx, &target); err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}

	// The database wrote these back through the pointer.
	if target.ID == 0 {
		t.Fatal("CreateTarget did not populate ID")
	}
	if target.CreatedAt.IsZero() {
		t.Error("CreateTarget did not populate CreatedAt")
	}

	t.Cleanup(func() {
		st.Pool.Exec(context.Background(), `DELETE FROM user_targets WHERE id = $1`, target.ID)
	})

	got, err := st.GetTarget(ctx, target.ID)
	if err != nil {
		t.Fatalf("GetTarget: %v", err)
	}

	if got.Label != target.Label {
		t.Errorf("label = %q; want %q", got.Label, target.Label)
	}
	// The nullable column survived as a real value, not nil.
	if got.CaloriesMaxDaily == nil {
		t.Fatal("calories_max_daily came back nil; want 2400")
	}
	if *got.CaloriesMaxDaily != 2400 {
		t.Errorf("calories_max_daily = %d; want 2400", *got.CaloriesMaxDaily)
	}
	// TEXT[] round-tripped.
	if len(got.DietTags) != 1 || got.DietTags[0] != "gluten_free" {
		t.Errorf("diet_tags = %v; want [gluten_free]", got.DietTags)
	}
	// BIGINT[] round-tripped, in order.
	if len(got.ExcludeFoodIDs) != 3 || got.ExcludeFoodIDs[2] != 3 {
		t.Errorf("exclude_food_ids = %v; want [1 2 3]", got.ExcludeFoodIDs)
	}
}

// The other half of the nullable column: omitting it must yield nil, not 0.
func TestCreateTarget_NilCaloriesStaysNil(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	target := UserTarget{
		Label: "__test_no_cap__", ProteinGDaily: 150, CarbsGDaily: 150,
		FatGDaily: 50, BudgetCentsWeekly: 6000, StoreID: UniversityPlaceStoreID,
		DietTags: []string{}, ExcludeFoodIDs: []int64{},
	}

	if err := st.CreateTarget(ctx, &target); err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	t.Cleanup(func() {
		st.Pool.Exec(context.Background(), `DELETE FROM user_targets WHERE id = $1`, target.ID)
	})

	got, err := st.GetTarget(ctx, target.ID)
	if err != nil {
		t.Fatalf("GetTarget: %v", err)
	}
	if got.CaloriesMaxDaily != nil {
		t.Errorf("calories_max_daily = %d; want nil", *got.CaloriesMaxDaily)
	}
}

func TestGetTarget_UnknownIDIsNotFound(t *testing.T) {
	st := newTestStore(t)

	_, err := st.GetTarget(context.Background(), 999_999_999)

	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound; got %v", err)
	}
}
