package fdc

// normalize_test.go — table-driven tests for the pure functions.
//
// Because Normalize and Validate touch no I/O, every case here is a struct
// literal in, a value out. That is what makes it cheap to test the awkward
// cases exhaustively: kJ-only energy, per-serving division, volume serving
// units, missing nutrients. Those are precisely the cases that would be
// painful to trigger against the live API.

import (
	"math"
	"strings"
	"testing"
)

// nutrient is a tiny constructor to keep the test tables readable.
func nutrient(id int, unit string, amount float64) FoodNutrient {
	return FoodNutrient{
		Nutrient: Nutrient{ID: id, UnitName: unit},
		Amount:   amount,
	}
}

// closeTo compares floats with a tolerance. Never compare floats with == :
// 0.1+0.2 != 0.3 in binary floating point, and normalization does division,
// so exact equality would fail on correct answers.
func closeTo(got, want, tol float64) bool {
	return math.Abs(got-want) <= tol
}

func TestNormalize_FoundationAndSRLegacy(t *testing.T) {
	tests := []struct {
		name    string
		detail  FoodDetail
		want    Per100g
		wantErr string // substring the error must contain; "" = expect success
	}{
		{
			name: "SR Legacy chicken breast, kcal reported directly",
			detail: FoodDetail{
				FdcID: 171077, DataType: DataTypeSRLegacy,
				Description: "Chicken, breast, raw",
				FoodNutrients: []FoodNutrient{
					nutrient(NutrientProtein, "G", 22.5),
					nutrient(NutrientFat, "G", 2.62),
					nutrient(NutrientCarbs, "G", 0),
					nutrient(NutrientEnergyKC, "KCAL", 120),
				},
			},
			want: Per100g{Kcal: 120, ProteinG: 22.5, CarbsG: 0, FatG: 2.62},
		},
		{
			// The kJ path: FDC reports only nutrient 1062, so Normalize must
			// convert. 502 kJ / 4.184 = 119.98 kcal.
			name: "energy reported only in kJ gets converted",
			detail: FoodDetail{
				FdcID: 1, DataType: DataTypeFoundation,
				FoodNutrients: []FoodNutrient{
					nutrient(NutrientProtein, "G", 22.5),
					nutrient(NutrientFat, "G", 2.62),
					nutrient(NutrientCarbs, "G", 0),
					nutrient(NutrientEnergyKJ, "kJ", 502),
				},
			},
			want: Per100g{Kcal: 119.98, ProteinG: 22.5, CarbsG: 0, FatG: 2.62},
		},
		{
			// When BOTH are present, kcal wins — no need to compound a
			// conversion when the value is reported directly.
			name: "kcal preferred over kJ when both present",
			detail: FoodDetail{
				FdcID: 2, DataType: DataTypeFoundation,
				FoodNutrients: []FoodNutrient{
					nutrient(NutrientProtein, "G", 20),
					nutrient(NutrientFat, "G", 10),
					nutrient(NutrientCarbs, "G", 0),
					nutrient(NutrientEnergyKC, "KCAL", 170),
					nutrient(NutrientEnergyKJ, "kJ", 9999), // absurd, must be ignored
				},
			},
			want: Per100g{Kcal: 170, ProteinG: 20, CarbsG: 0, FatG: 10},
		},
		{
			// Zero is a REAL value, not "missing" — oil genuinely has zero
			// protein. This must succeed, which is why found-flags exist
			// instead of checking for non-zero.
			name: "zero macros are valid values, not missing data",
			detail: FoodDetail{
				FdcID: 3, DataType: DataTypeSRLegacy,
				FoodNutrients: []FoodNutrient{
					nutrient(NutrientProtein, "G", 0),
					nutrient(NutrientFat, "G", 100),
					nutrient(NutrientCarbs, "G", 0),
					nutrient(NutrientEnergyKC, "KCAL", 884),
				},
			},
			want: Per100g{Kcal: 884, ProteinG: 0, CarbsG: 0, FatG: 100},
		},
		{
			name: "missing protein is fatal",
			detail: FoodDetail{
				FdcID: 4, DataType: DataTypeSRLegacy,
				FoodNutrients: []FoodNutrient{
					nutrient(NutrientFat, "G", 2),
					nutrient(NutrientCarbs, "G", 0),
					nutrient(NutrientEnergyKC, "KCAL", 100),
				},
			},
			wantErr: "missing nutrient(s): protein",
		},
		{
			name: "missing energy entirely is fatal",
			detail: FoodDetail{
				FdcID: 5, DataType: DataTypeSRLegacy,
				FoodNutrients: []FoodNutrient{
					nutrient(NutrientProtein, "G", 20),
					nutrient(NutrientFat, "G", 2),
					nutrient(NutrientCarbs, "G", 0),
				},
			},
			wantErr: "no energy nutrient",
		},
		{
			name:    "empty nutrient array is fatal",
			detail:  FoodDetail{FdcID: 6, DataType: DataTypeSRLegacy},
			wantErr: "no foodNutrients array",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Normalize(tc.detail)

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error containing %q; got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q; want it to contain %q", err, tc.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			const tol = 0.01
			if !closeTo(got.Kcal, tc.want.Kcal, tol) {
				t.Errorf("Kcal = %v; want %v", got.Kcal, tc.want.Kcal)
			}
			if !closeTo(got.ProteinG, tc.want.ProteinG, tol) {
				t.Errorf("ProteinG = %v; want %v", got.ProteinG, tc.want.ProteinG)
			}
			if !closeTo(got.CarbsG, tc.want.CarbsG, tol) {
				t.Errorf("CarbsG = %v; want %v", got.CarbsG, tc.want.CarbsG)
			}
			if !closeTo(got.FatG, tc.want.FatG, tol) {
				t.Errorf("FatG = %v; want %v", got.FatG, tc.want.FatG)
			}
			// Provenance must survive normalization.
			if got.FdcID != tc.detail.FdcID {
				t.Errorf("FdcID = %d; want %d", got.FdcID, tc.detail.FdcID)
			}
		})
	}
}

// label builds a Branded labelNutrients block from per-serving values.
func label(protein, carbs, fat, calories float64) *LabelNutrients {
	return &LabelNutrients{
		Protein:       &LabelValue{Value: protein},
		Carbohydrates: &LabelValue{Value: carbs},
		Fat:           &LabelValue{Value: fat},
		Calories:      &LabelValue{Value: calories},
	}
}

func TestNormalize_Branded(t *testing.T) {
	tests := []struct {
		name    string
		detail  FoodDetail
		want    Per100g
		wantErr string
	}{
		{
			// The core per-serving division. A 30 g scoop with 24 g protein is
			// 80 g protein per 100 g. Serving is deliberately FAR from 100 g so
			// that a multiply-instead-of-divide bug can't accidentally pass.
			name: "30g serving scales up to per-100g",
			detail: FoodDetail{
				FdcID: 100, DataType: DataTypeBranded,
				ServingSize: 30, ServingSizeUnit: "g",
				LabelNutrients: label(24, 2.4, 0.9, 111),
			},
			want: Per100g{ProteinG: 80, CarbsG: 8, FatG: 3, Kcal: 370},
		},
		{
			// A serving LARGER than 100 g must scale DOWN — the other
			// direction of the same bug.
			name: "200g serving scales down to per-100g",
			detail: FoodDetail{
				FdcID: 101, DataType: DataTypeBranded,
				ServingSize: 200, ServingSizeUnit: "g",
				LabelNutrients: label(40, 20, 10, 330),
			},
			want: Per100g{ProteinG: 20, CarbsG: 10, FatG: 5, Kcal: 165},
		},
		{
			// Ounces convert to grams first: 2 oz = 56.699 g.
			name: "ounce serving unit converts to grams",
			detail: FoodDetail{
				FdcID: 102, DataType: DataTypeBranded,
				ServingSize: 2, ServingSizeUnit: "oz",
				LabelNutrients: label(11.34, 0, 1.13, 56.7),
			},
			want: Per100g{ProteinG: 20, CarbsG: 0, FatG: 2, Kcal: 100},
		},
		{
			// Casing and whitespace are inconsistent in FDC data.
			name: "serving unit casing is tolerated",
			detail: FoodDetail{
				FdcID: 103, DataType: DataTypeBranded,
				ServingSize: 100, ServingSizeUnit: " G ",
				LabelNutrients: label(10, 10, 10, 170),
			},
			want: Per100g{ProteinG: 10, CarbsG: 10, FatG: 10, Kcal: 170},
		},
		{
			// THE REJECTION RULE: volume units cannot become mass without
			// density, so we refuse rather than guess.
			name: "millilitre serving is rejected, not guessed",
			detail: FoodDetail{
				FdcID: 104, DataType: DataTypeBranded,
				ServingSize: 240, ServingSizeUnit: "ml",
				LabelNutrients: label(8, 12, 5, 150),
			},
			wantErr: "cannot convert serving unit",
		},
		{
			name: "fluid ounce serving is rejected",
			detail: FoodDetail{
				FdcID: 105, DataType: DataTypeBranded,
				ServingSize: 8, ServingSizeUnit: "fl oz",
				LabelNutrients: label(8, 12, 5, 150),
			},
			wantErr: "cannot convert serving unit",
		},
		{
			name: "zero serving size is rejected (would divide by zero)",
			detail: FoodDetail{
				FdcID: 106, DataType: DataTypeBranded,
				ServingSize: 0, ServingSizeUnit: "g",
				LabelNutrients: label(8, 12, 5, 150),
			},
			wantErr: "non-positive servingSize",
		},
		{
			name: "sub-gram serving is rejected as implausible",
			detail: FoodDetail{
				FdcID: 107, DataType: DataTypeBranded,
				ServingSize: 500, ServingSizeUnit: "mg", // 0.5 g
				LabelNutrients: label(0.1, 0.1, 0.1, 2),
			},
			wantErr: "implausibly small",
		},
		{
			name: "missing label macro is reported",
			detail: FoodDetail{
				FdcID: 108, DataType: DataTypeBranded,
				ServingSize: 30, ServingSizeUnit: "g",
				LabelNutrients: &LabelNutrients{
					Protein:  &LabelValue{Value: 24},
					Fat:      &LabelValue{Value: 1},
					Calories: &LabelValue{Value: 110},
					// Carbohydrates omitted
				},
			},
			wantErr: "missing carbohydrates",
		},
		{
			// A Branded record with no label block but a nutrients array should
			// fall back rather than fail — real FDC data has both shapes.
			name: "branded with no label falls back to foodNutrients",
			detail: FoodDetail{
				FdcID: 109, DataType: DataTypeBranded,
				FoodNutrients: []FoodNutrient{
					nutrient(NutrientProtein, "G", 25),
					nutrient(NutrientFat, "G", 1),
					nutrient(NutrientCarbs, "G", 0),
					nutrient(NutrientEnergyKC, "KCAL", 110),
				},
			},
			want: Per100g{ProteinG: 25, CarbsG: 0, FatG: 1, Kcal: 110},
		},
		{
			name: "branded with neither label nor nutrients is fatal",
			detail: FoodDetail{
				FdcID: 110, DataType: DataTypeBranded,
				ServingSize: 30, ServingSizeUnit: "g",
			},
			wantErr: "no labelNutrients",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Normalize(tc.detail)

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error containing %q; got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q; want it to contain %q", err, tc.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			const tol = 0.05 // looser: ounce conversion compounds rounding
			if !closeTo(got.Kcal, tc.want.Kcal, tol) {
				t.Errorf("Kcal = %v; want %v", got.Kcal, tc.want.Kcal)
			}
			if !closeTo(got.ProteinG, tc.want.ProteinG, tol) {
				t.Errorf("ProteinG = %v; want %v", got.ProteinG, tc.want.ProteinG)
			}
			if !closeTo(got.CarbsG, tc.want.CarbsG, tol) {
				t.Errorf("CarbsG = %v; want %v", got.CarbsG, tc.want.CarbsG)
			}
			if !closeTo(got.FatG, tc.want.FatG, tol) {
				t.Errorf("FatG = %v; want %v", got.FatG, tc.want.FatG)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name     string
		p        Per100g
		category string
		wantErr  string
	}{
		{
			name: "realistic chicken breast passes",
			p:    Per100g{Kcal: 120, ProteinG: 22.5, CarbsG: 0, FatG: 2.6},
			// Atwater = 90+0+23.4 = 113.4; 120 is 5.8% off. Fine.
			category: "protein",
		},
		{
			// Nuts are the classic legitimate Atwater outlier, but still well
			// inside 25%: Atwater = 84.8+86.4+449 = 620, measured 579 = 6.6%.
			name: "almonds pass despite specific-factor energy",
			p:    Per100g{Kcal: 579, ProteinG: 21.2, CarbsG: 21.6, FatG: 49.9},
		},
		{
			name: "pure oil passes (zero protein, not a protein food)",
			p:    Per100g{Kcal: 884, ProteinG: 0, CarbsG: 0, FatG: 100},
			// Atwater = 900 vs 884 = 1.8% off.
			category: "fat",
		},
		{
			name: "near-zero-calorie vegetable skips the Atwater check",
			p:    Per100g{Kcal: 15, ProteinG: 1.4, CarbsG: 2.9, FatG: 0.2},
			// Atwater = 5.6+11.6+1.8 = 19, a 21% gap that would be noise at
			// this scale. Below the 20 kcal floor, so not checked.
			category: "vegetable",
		},
		{
			name:    "macro over 105g per 100g is rejected",
			p:       Per100g{Kcal: 400, ProteinG: 120, CarbsG: 0, FatG: 0},
			wantErr: "implausible macro",
		},
		{
			name:    "negative macro is rejected",
			p:       Per100g{Kcal: 100, ProteinG: -5, CarbsG: 10, FatG: 2},
			wantErr: "negative value",
		},
		{
			// The per-serving bug signature: every macro inflated together, so
			// each is individually under 105 but the SUM is impossible.
			name:    "macros summing over 105g is rejected",
			p:       Per100g{Kcal: 700, ProteinG: 60, CarbsG: 50, FatG: 20},
			wantErr: "macros sum to",
		},
		{
			name:    "energy far below macros is rejected",
			p:       Per100g{Kcal: 100, ProteinG: 20, CarbsG: 20, FatG: 20},
			wantErr: "deviates",
		},
		{
			name:    "energy far above macros is rejected",
			p:       Per100g{Kcal: 900, ProteinG: 10, CarbsG: 10, FatG: 10},
			wantErr: "deviates",
		},
		{
			name:    "calories with no macros is rejected",
			p:       Per100g{Kcal: 300, ProteinG: 0, CarbsG: 0, FatG: 0},
			wantErr: "essentially no macros",
		},
		{
			name:     "protein category with no protein is rejected",
			p:        Per100g{Kcal: 380, ProteinG: 1, CarbsG: 90, FatG: 1},
			category: "protein",
			wantErr:  "category is 'protein'",
		},
		{
			name:     "same food passes when category is not protein",
			p:        Per100g{Kcal: 380, ProteinG: 1, CarbsG: 90, FatG: 1},
			category: "carb",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.p, tc.category)

			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error; got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q; got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q; want it to contain %q", err, tc.wantErr)
			}
		})
	}
}
