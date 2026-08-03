package handler

// targets.go - POST /v1/targets and GET /v1/targets/{id}
// the first endpoint that ACCEPTS data rather than just returning it,
// first one where the client can be wrong in interesting ways.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/BenYang12/Macro-Max/internal/store"
)

type TargetStore interface {
	CreateTarget(ctx context.Context, t *store.UserTarget) error
	GetTarget(ctx context.Context, id int64) (store.UserTarget, error)
}

type TargetsHandler struct {
	Store TargetStore
}

func NewTargetsHandler(s TargetStore) *TargetsHandler {
	return &TargetsHandler{Store: s}
}

// shape of an acceptable POST body
type createTargetRequest struct {
	Label             *string `json:"label"`
	ProteinGDaily     *int    `json:"protein_g_daily"`
	CarbsGDaily       *int    `json:"carbs_g_daily"`
	FatGDaily         *int    `json:"fat_g_daily"`
	CaloriesMaxDaily  *int    `json:"calories_max_daily"` // genuinely optional
	BudgetCentsWeekly *int    `json:"budget_cents_weekly"`

	DietTags       []string `json:"diet_tags"`        // slices are already nil-able
	ExcludeFoodIDs []int64  `json:"exclude_food_ids"` // so no pointer needed
}

// Create handles POST /v1/targets
func (h *TargetsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createTargetRequest

	// readJSON takes JSON body a client sent and fills Go struct with it
	if err := readJSON(w, r, &req); err != nil {
		badRequestResponse(w, err)
		return
	}

	// Validation
	//   400 Bad Request          — "I can't parse this." Broken syntax,
	//                              unknown field, wrong JSON type.
	//   422 Unprocessable Entity — "I parsed it fine; the VALUES are wrong."
	//                              Negative protein, zero budget, empty label.

	// 400 -> bug in client's code
	// 422 -> show the user next to the offending output

	// COLLECT ALL ERRORS, DON'T RETURN ON THE FIRST. A form with four bad
	// fields should report four problems in one response, not force four
	// round trips.
	fields := map[string]string{}

	if req.Label == nil {
		fields["label"] = "must be provided"
	} else if *req.Label == "" {
		// Present but empty is a DIFFERENT mistake than absent, and saying so
		// helps whoever is debugging the client.
		fields["label"] = "must not be empty"
	}

	// The three macros share identical rules, so a tiny local closure keeps
	// this honest instead of copy-pasting the same six lines three times.
	requireNonNegative := func(name string, v *int) {
		if v == nil {
			fields[name] = "must be provided"
		} else if *v < 0 {
			fields[name] = "must not be negative"
		}
	}
	requireNonNegative("protein_g_daily", req.ProteinGDaily)
	requireNonNegative("carbs_g_daily", req.CarbsGDaily)
	requireNonNegative("fat_g_daily", req.FatGDaily)

	// Budget must be strictly positive: a zero budget makes every solve
	// trivially infeasible, so accepting it only defers a confusing failure.
	if req.BudgetCentsWeekly == nil {
		fields["budget_cents_weekly"] = "must be provided"
	} else if *req.BudgetCentsWeekly <= 0 {
		fields["budget_cents_weekly"] = "must be greater than zero"
	}

	// Optional field: nil is fine (no ceiling). Only validate if PRESENT.
	if req.CaloriesMaxDaily != nil && *req.CaloriesMaxDaily <= 0 {
		fields["calories_max_daily"] = "must be greater than zero when provided"
	}

	// A cross-field check — the kind of rule a database CHECK constraint
	// can't express well, which is exactly why an application validation
	// layer exists at all. If a calorie ceiling is set, it has to leave room
	// for the macros the user asked for (Atwater: 4/4/9 kcal per gram).
	if req.CaloriesMaxDaily != nil &&
		req.ProteinGDaily != nil && req.CarbsGDaily != nil && req.FatGDaily != nil {
		macroKcal := 4**req.ProteinGDaily + 4**req.CarbsGDaily + 9**req.FatGDaily
		if *req.CaloriesMaxDaily < macroKcal {
			fields["calories_max_daily"] = fmt.Sprintf(
				"is below the %d kcal implied by the macro targets", macroKcal)
		}
	}

	// One response carrying every problem found.
	if len(fields) > 0 {
		failedValidationResponse(w, fields)
		return
	}

	// Past this point every pointer is known non-nil, so dereferencing is
	// safe. This is the moment the request shape becomes the STORE shape —
	// and note what is NOT copied: id and created_at, which the database owns.
	target := store.UserTarget{
		Label:             *req.Label,
		ProteinGDaily:     *req.ProteinGDaily,
		CarbsGDaily:       *req.CarbsGDaily,
		FatGDaily:         *req.FatGDaily,
		CaloriesMaxDaily:  req.CaloriesMaxDaily, // already a pointer; nil is meaningful
		BudgetCentsWeekly: *req.BudgetCentsWeekly,
		StoreID:           store.UniversityPlaceStoreID,
		DietTags:          req.DietTags,
		ExcludeFoodIDs:    req.ExcludeFoodIDs,
	}

	// Postgres columns are NOT NULL DEFAULT '{}', and a nil Go slice would be
	// sent as SQL NULL, violating that. Normalize nil to empty so an omitted
	// array behaves like an empty one.
	if target.DietTags == nil {
		target.DietTags = []string{}
	}
	if target.ExcludeFoodIDs == nil {
		target.ExcludeFoodIDs = []int64{}
	}

	// &target passes the address, so CreateTarget can write id and created_at
	// back into this very struct — which is why the response below already
	// has them.
	if err := h.Store.CreateTarget(r.Context(), &target); err != nil {
		serverErrorResponse(w, err)
		return
	}

	// 201 Created, not 200 OK — the precise status for "a new resource now
	// exists". The Location header tells the client WHERE it lives, so it can
	// follow up without string-building a URL itself. Both are small
	// correctness details that make an API feel professional.
	w.Header().Set("Location", fmt.Sprintf("/v1/targets/%d", target.ID))

	if err := writeJSON(w, http.StatusCreated, envelope{"target": target}); err != nil {
		serverErrorResponse(w, err)
	}

}

// Get handles GET /v1/targets/{id} — same shape as foods and products.
func (h *TargetsHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		notFoundResponse(w)
		return
	}

	target, err := h.Store.GetTarget(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFoundResponse(w)
			return
		}
		serverErrorResponse(w, err)
		return
	}

	if err := writeJSON(w, http.StatusOK, envelope{"target": target}); err != nil {
		serverErrorResponse(w, err)
	}
}
