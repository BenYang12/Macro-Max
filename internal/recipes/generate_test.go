package recipes

// generate_test.go — exercises the REAL Generate path against a fake Anthropic
// server, with no API key and no network.
//
// client_test.go covers parsePlan and buildPrompt in isolation, which leaves
// the most failure-prone part untested: the SDK call itself. Everything between
// my Request and my Plan — the auth header, the request body shape, block
// concatenation, the stop_reason check — lives in Generate and would otherwise
// be verified for the first time by a paid call in production.
//
// httptest.NewServer plus the baseURL override is what makes this possible, and
// it's the exact reason New takes that parameter. Same trick as fdc.Client's
// BaseURL from Phase 2.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAnthropic returns a server that replies with one text block containing
// `text`, plus whatever stop_reason is asked for.
func fakeAnthropic(t *testing.T, stopReason, text string, capture *map[string]any) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			body, _ := io.ReadAll(r.Body)
			var parsed map[string]any
			_ = json.Unmarshal(body, &parsed)
			// Record the headers I care about alongside the body, so one
			// capture map can carry everything the assertions need.
			parsed["__auth"] = r.Header.Get("x-api-key")
			parsed["__path"] = r.URL.Path
			*capture = parsed
		}

		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"id":            "msg_test",
			"type":          "message",
			"role":          "assistant",
			"model":         "claude-opus-5",
			"stop_reason":   stopReason,
			"stop_sequence": nil,
			"usage":         map[string]any{"input_tokens": 10, "output_tokens": 20},
			"content":       []any{map[string]any{"type": "text", "text": text}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sampleRequest() Request {
	return Request{
		Ingredients: []Ingredient{
			{Food: "Lentils, dry", Grams: 1815},
			{Food: "Broccoli, frozen", Grams: 680},
		},
		ProteinGDaily: 180, CarbsGDaily: 200, FatGDaily: 60,
		DietTags: []string{"vegan"},
	}
}

const samplePlanJSON = `{"meals":[{"name":"Lentil curry","servings":6,
	"ingredients":["900g lentils","340g broccoli"],"steps":["Simmer","Serve"],
	"prep_minutes":40}],"notes":["Freezes well."]}`

func TestGenerate_HappyPath(t *testing.T) {
	var captured map[string]any
	srv := fakeAnthropic(t, "end_turn", samplePlanJSON, &captured)

	c := New("test-key-not-real", srv.URL)

	plan, err := c.Generate(t.Context(), sampleRequest())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if len(plan.Meals) != 1 || plan.Meals[0].Name != "Lentil curry" {
		t.Fatalf("plan not parsed: %+v", plan)
	}
	if plan.Meals[0].Servings != 6 || plan.Meals[0].PrepMinutes != 40 {
		t.Errorf("meal fields wrong: %+v", plan.Meals[0])
	}
	if len(plan.Notes) != 1 {
		t.Errorf("notes = %v, want 1", plan.Notes)
	}
}

func TestGenerate_SendsTheRightRequest(t *testing.T) {
	var captured map[string]any
	srv := fakeAnthropic(t, "end_turn", samplePlanJSON, &captured)

	if _, err := New("test-key-not-real", srv.URL).Generate(t.Context(), sampleRequest()); err != nil {
		t.Fatal(err)
	}

	if captured["__auth"] != "test-key-not-real" {
		t.Errorf("x-api-key = %v, want the configured key", captured["__auth"])
	}
	if got := captured["model"]; got != "claude-opus-5" {
		t.Errorf("model = %v, want claude-opus-5", got)
	}
	// max_tokens must be present and bounded. An unbounded request on a public
	// endpoint is how a demo becomes a surprise invoice.
	if captured["max_tokens"] == nil {
		t.Error("max_tokens was not sent")
	}

	// The system prompt must carry the hard rules. If it silently stopped being
	// sent, the model would start inventing ingredients that aren't in the
	// basket — which is precisely the hallucination this package's design
	// exists to prevent, and it would look like a model quality problem rather
	// than a missing field.
	system, _ := json.Marshal(captured["system"])
	if !strings.Contains(string(system), "Use ONLY the ingredients listed") {
		t.Errorf("system prompt did not carry the ingredient rule: %s", system)
	}

	// And the user turn must carry the actual basket.
	messages, _ := json.Marshal(captured["messages"])
	for _, want := range []string{"Lentils, dry: 1815g", "180g protein", "vegan"} {
		if !strings.Contains(string(messages), want) {
			t.Errorf("user message missing %q: %s", want, messages)
		}
	}
}

func TestGenerate_RefusalBecomesErrRefused(t *testing.T) {
	// A refusal is a 200 with a well-formed body — the failure is in
	// stop_reason, not the status code. Checking content first would mean
	// trying to parse an apology as JSON and reporting "invalid JSON", which
	// hides the real reason completely.
	srv := fakeAnthropic(t, "refusal", "I can't help with that.", nil)

	_, err := New("k", srv.URL).Generate(t.Context(), sampleRequest())
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v, want ErrRefused", err)
	}
}

func TestGenerate_NonJSONOutputIsAnError(t *testing.T) {
	srv := fakeAnthropic(t, "end_turn", "Sure! Here are some meal ideas...", nil)

	_, err := New("k", srv.URL).Generate(t.Context(), sampleRequest())
	if err == nil {
		t.Fatal("expected an error when the model returns prose instead of JSON")
	}
	if errors.Is(err, ErrRefused) {
		t.Error("prose output is a parse failure, not a refusal")
	}
}

func TestGenerate_ServerErrorIsWrapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"boom"}}`))
	}))
	t.Cleanup(srv.Close)

	// The SDK retries 5xx, so this also proves the retry path terminates
	// rather than hanging — a test that hung here would be its own signal.
	_, err := New("k", srv.URL).Generate(t.Context(), sampleRequest())
	if err == nil {
		t.Fatal("expected an error for a 500 from the API")
	}
	if !strings.Contains(err.Error(), "calling Claude") {
		t.Errorf("error should name the failing subsystem; got: %v", err)
	}
}
