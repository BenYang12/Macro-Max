package recipes

// client_test.go — tests for the parts of this package that are MY logic
// rather than Claude's: parsing the model's output and building the prompt.
//
// There is deliberately no test that calls the real API. It would cost money,
// need a key, fail offline, and — worst of all — assert on the one thing that
// is legitimately non-deterministic. What I can and should pin down is that
// unusual-but-valid output doesn't break my parser, and that malformed output
// fails loudly instead of producing an empty plan.

import (
	"errors"
	"strings"
	"testing"
)

func TestParsePlan_BareJSON(t *testing.T) {
	plan, err := parsePlan(`{"meals":[{"name":"Dal","servings":4,"prep_minutes":30,
		"ingredients":["400g lentils"],"steps":["Simmer"]}],"notes":["Freezes."]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Meals) != 1 || plan.Meals[0].Name != "Dal" {
		t.Fatalf("meals not parsed: %+v", plan.Meals)
	}
	if plan.Meals[0].Servings != 4 || plan.Meals[0].PrepMinutes != 30 {
		t.Errorf("numeric fields wrong: %+v", plan.Meals[0])
	}
	if len(plan.Notes) != 1 {
		t.Errorf("notes = %v, want 1", plan.Notes)
	}
}

func TestParsePlan_StripsMarkdownFences(t *testing.T) {
	// The system prompt says "no markdown fences", and models mostly comply.
	// "Mostly" is the operative word: fencing JSON is a deeply ingrained habit,
	// and a parser that dies on it turns a cosmetic deviation into a 500.
	//
	// Both fence styles, because ``` and ```json are equally common.
	for _, raw := range []string{
		"```json\n{\"meals\":[{\"name\":\"Dal\"}]}\n```",
		"```\n{\"meals\":[{\"name\":\"Dal\"}]}\n```",
		"  ```json\n{\"meals\":[{\"name\":\"Dal\"}]}\n```  ",
	} {
		plan, err := parsePlan(raw)
		if err != nil {
			t.Fatalf("parsing %q: %v", raw, err)
		}
		if len(plan.Meals) != 1 || plan.Meals[0].Name != "Dal" {
			t.Errorf("parsing %q gave %+v", raw, plan.Meals)
		}
	}
}

func TestParsePlan_RejectsGarbage(t *testing.T) {
	if _, err := parsePlan("I'd be happy to help you plan meals!"); err == nil {
		t.Fatal("expected an error for non-JSON output")
	}
}

func TestParsePlan_RejectsEmptyMeals(t *testing.T) {
	// Valid JSON, zero meals. This parses perfectly and is completely useless,
	// which is precisely why it needs an explicit check — without it the
	// handler would return 200 with an empty plan and call that success.
	_, err := parsePlan(`{"meals":[],"notes":["sorry"]}`)
	if err == nil {
		t.Fatal("expected an error for a plan with no meals")
	}
	if !strings.Contains(err.Error(), "no meals") {
		t.Errorf("error should say what was wrong; got: %v", err)
	}
}

func TestGenerate_EmptyBasketFailsWithoutCallingTheAPI(t *testing.T) {
	// New with a junk key and no base URL override: if this ever reached the
	// network it would fail with an auth error rather than ErrNoBasket, so the
	// assertion doubles as proof that the guard short-circuits FIRST.
	c := New("not-a-real-key", "")

	_, err := c.Generate(t.Context(), Request{Ingredients: nil})
	if !errors.Is(err, ErrNoBasket) {
		t.Fatalf("err = %v, want ErrNoBasket", err)
	}
}

func TestBuildPrompt_IncludesEverythingTheModelNeeds(t *testing.T) {
	got := buildPrompt(Request{
		Ingredients: []Ingredient{
			{Food: "Lentils, dry", Grams: 1815.4},
			{Food: "Broccoli, frozen", Grams: 680},
		},
		ProteinGDaily: 180, CarbsGDaily: 200, FatGDaily: 60,
		DietTags: []string{"vegan", "gluten_free"},
	})

	for _, want := range []string{
		"Lentils, dry: 1815g", // %.0f — no false precision from the solver's rounding
		"Broccoli, frozen: 680g",
		"180g protein",
		"vegan, gluten free", // underscores become spaces; a prompt reads as English
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, got)
		}
	}
}

func TestBuildPrompt_OmitsDietLineWhenNoTags(t *testing.T) {
	got := buildPrompt(Request{
		Ingredients:   []Ingredient{{Food: "Oats", Grams: 500}},
		ProteinGDaily: 100,
	})
	// An empty "Dietary requirements: ." line is noise that costs tokens and
	// reads as a bug to anyone inspecting the prompt in a log.
	if strings.Contains(got, "Dietary requirements") {
		t.Errorf("expected no diet line for an untagged target:\n%s", got)
	}
}
