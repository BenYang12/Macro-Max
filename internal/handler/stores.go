package handler

// stores.go — GET /v1/stores?zip=, the store picker's data source.
//
// This is my first endpoint that PROXIES a third-party API rather than reading
// my own database. Worth being deliberate about why I'm proxying at all instead
// of letting the frontend call Kroger directly:
//
//   1. My Kroger credentials would have to ship to the browser. That's
//      disqualifying on its own — a client secret in JavaScript is a public
//      secret.
//   2. I control the response shape, so the frontend depends on MY contract,
//      not Kroger's. When they rename a field, one Go file changes.
//   3. It's the natural place to add caching later (store lists change roughly
//      never, so this is an obvious candidate).

import (
	"context"
	"net/http"
	"regexp"

	"github.com/BenYang12/Macro-Max/internal/kroger"
)

// StoreFinder is the slice of the Kroger client this handler needs — one
// method, same consumer-side interface habit as everywhere else.
type StoreFinder interface {
	Locations(ctx context.Context, zip string, limit int) ([]kroger.Location, error)
}

type StoresHandler struct {
	Kroger StoreFinder
}

func NewStoresHandler(k StoreFinder) *StoresHandler {
	return &StoresHandler{Kroger: k}
}

// zipPattern is exactly five digits. I validate BEFORE calling Kroger rather
// than forwarding whatever arrived, for two reasons: a bad zip wastes a request
// against my daily quota, and a clear 422 is a better answer than whatever
// Kroger returns for garbage input.
var zipPattern = regexp.MustCompile(`^[0-9]{5}$`)

// List handles GET /v1/stores?zip=45202
func (h *StoresHandler) List(w http.ResponseWriter, r *http.Request) {
	zip := r.URL.Query().Get("zip")

	if zip == "" {
		failedValidationResponse(w, map[string]string{"zip": "must be provided"})
		return
	}
	if !zipPattern.MatchString(zip) {
		failedValidationResponse(w, map[string]string{"zip": "must be a 5-digit US zip code"})
		return
	}

	locations, err := h.Kroger.Locations(r.Context(), zip, 10)
	if err != nil {
		// A Kroger failure is MY problem from the client's point of view: they
		// asked a well-formed question and I couldn't answer it.
		serverErrorResponse(w, err)
		return
	}

	// Reshape into my own JSON rather than passing Kroger's through. The
	// frontend should never learn Kroger's field names.
	stores := make([]map[string]any, 0, len(locations))
	for _, l := range locations {
		stores = append(stores, map[string]any{
			"location_id": l.LocationID,
			"name":        l.Name,
			"chain":       l.Chain,
			"address":     l.Address.AddressLine1,
			"city":        l.Address.City,
			"state":       l.Address.State,
			"zip":         l.Address.ZipCode,
		})
	}

	if err := writeJSON(w, http.StatusOK, envelope{"stores": stores}); err != nil {
		serverErrorResponse(w, err)
	}
}
