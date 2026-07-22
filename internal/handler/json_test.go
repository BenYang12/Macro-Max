package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWriteJSON checks the happy path: correct status, header, and body.
func TestWriteJSON(t *testing.T) {
	rr := httptest.NewRecorder()

	err := writeJSON(rr, http.StatusOK, envelope{"food": "oats"})
	if err != nil {
		t.Fatalf("writeJSON returned an error: %v", err)
	}

	if rr.Code != http.StatusOK {
		t.Errorf("status: want %d, got %d", http.StatusOK, rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type: want application/json, got %q", got)
	}

	var body map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}
	if body["food"] != "oats" {
		t.Errorf(`body["food"]: want "oats", got %q`, body["food"])
	}
}

// TestReadJSONValid: a well-formed body decodes into the target struct.
func TestReadJSONValid(t *testing.T) {
	// A struct defined INSIDE a function — legal in Go, and handy for
	// test-local shapes. The json tags map JSON keys to fields.
	var dst struct {
		Label   string `json:"label"`
		Protein int    `json:"protein_g_daily"`
	}

	body := `{"label":"cutting","protein_g_daily":150}`
	req := httptest.NewRequest(http.MethodPost, "/v1/targets", strings.NewReader(body))
	rr := httptest.NewRecorder()

	if err := readJSON(rr, req, &dst); err != nil {
		t.Fatalf("readJSON returned an unexpected error: %v", err)
	}

	if dst.Label != "cutting" {
		t.Errorf("Label: want %q, got %q", "cutting", dst.Label)
	}
	if dst.Protein != 150 {
		t.Errorf("Protein: want 150, got %d", dst.Protein)
	}
}

// TestReadJSONErrors is a table-driven test over every failure mode
// readJSON is designed to catch. `wantErrContains` keeps assertions loose:
// we check the message MENTIONS the right problem rather than pinning exact
// wording, so improving an error message doesn't break the test.
func TestReadJSONErrors(t *testing.T) {
	tests := []struct {
		name            string
		body            string
		wantErrContains string
	}{
		{"empty body", "", "must not be empty"},
		{"malformed json", `{"label":`, "badly-formed"},
		{"wrong type", `{"protein_g_daily":"lots"}`, "incorrect JSON type"},
		{"unknown field", `{"protien_g_daily":150}`, "unknown key"},
		{"two json values", `{"label":"a"}{"label":"b"}`, "single JSON value"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dst struct {
				Label   string `json:"label"`
				Protein int    `json:"protein_g_daily"`
			}

			req := httptest.NewRequest(http.MethodPost, "/v1/targets", strings.NewReader(tt.body))
			rr := httptest.NewRecorder()

			err := readJSON(rr, req, &dst)
			if err == nil {
				t.Fatalf("expected an error for %s, got nil", tt.name)
			}
			if !strings.Contains(err.Error(), tt.wantErrContains) {
				t.Errorf("error %q should contain %q", err.Error(), tt.wantErrContains)
			}
		})
	}
}

// TestErrorResponses checks that each helper produces the documented
// envelope shape: {"error":{"code":..., "message":...}}.
func TestErrorResponses(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		rr := httptest.NewRecorder()
		notFoundResponse(rr)

		if rr.Code != http.StatusNotFound {
			t.Errorf("status: want 404, got %d", rr.Code)
		}

		// Nested map because the value under "error" is itself an object.
		var body map[string]map[string]any
		if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
			t.Fatalf("body is not valid JSON: %v", err)
		}
		if body["error"]["code"] != "not_found" {
			t.Errorf(`code: want "not_found", got %v`, body["error"]["code"])
		}
		if body["error"]["message"] == "" {
			t.Error("message should not be empty")
		}
	})

	t.Run("failed validation", func(t *testing.T) {
		rr := httptest.NewRecorder()
		failedValidationResponse(rr, map[string]string{
			"protein_g_daily": "must be zero or greater",
		})

		if rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("status: want 422, got %d", rr.Code)
		}

		var body map[string]map[string]any
		if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
			t.Fatalf("body is not valid JSON: %v", err)
		}
		if body["error"]["code"] != "validation_failed" {
			t.Errorf(`code: want "validation_failed", got %v`, body["error"]["code"])
		}

		// The nested "fields" object decodes as map[string]any.
		fields, ok := body["error"]["fields"].(map[string]any)
		if !ok {
			t.Fatalf("fields should be a JSON object, got %T", body["error"]["fields"])
		}
		if fields["protein_g_daily"] == nil {
			t.Error("fields should mention protein_g_daily")
		}
	})
}
