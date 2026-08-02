// Package recipes turns a solved basket into a week of meals using Claude.
//
// THE ARCHITECTURAL POINT OF THIS PACKAGE, and the thing I'd want a reader to
// take away: the LLM is a FINISHING LAYER, not the product. The MILP already
// decided what to buy and proved nothing cheaper hits the macros. That answer
// is verifiable — you can check the arithmetic. What a solver genuinely cannot
// do is tell you that 2.4kg of lentils and 900g of frozen broccoli is a curry,
// a soup, and three lunches. That's the job here, and it's the ONLY job here.
//
// So the boundary is strict, in both directions:
//
//   - Claude never chooses foods, quantities, or prices. It receives a finished
//     basket and writes cooking instructions for it.
//   - Nothing in this package can fail the solve. If the API key is missing the
//     route isn't registered; if the call fails the user still has their basket.
//
// The moment an LLM is allowed to pick the groceries, the "provably cheapest"
// claim in my README becomes a lie, because nothing about a language model's
// output is provable. Keeping that line bright is the whole design.
package recipes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// model is pinned rather than tracking an alias.
//
// Aliases like "claude-opus-latest" quietly change what my code talks to. For a
// portfolio project that someone might clone in a year, a pinned model means
// the output they get is the output I tested against. Bumping it is then a
// deliberate one-line commit with a diff, which is what a version change
// deserves to be.
const model = anthropic.ModelClaudeOpus5

// maxTokens caps the response. A week of meals is a page or two of text; this
// is roughly four times that, which is headroom rather than a real limit.
//
// It is ALSO a cost ceiling. Output tokens are the expensive half of an LLM
// bill, and an unbounded max_tokens on a public endpoint is how a demo becomes
// a surprise invoice.
const maxTokens = 8192

// requestTimeout bounds the whole call including retries.
//
// Deliberately generous compared to the 10s I gave the FDC client: extended
// thinking plus a long structured response is genuinely slower than fetching a
// JSON document, and a timeout that fires mid-generation wastes the tokens
// already spent without producing anything.
const requestTimeout = 120 * time.Second

// ErrRefused is returned when Claude declines to answer.
//
// This is a real, documented outcome (stop_reason: "refusal"), not an error
// condition — the model completed successfully and chose not to produce the
// content. Treating it as a 500 would be wrong twice over: it isn't my server
// failing, and retrying would just burn tokens reaching the same conclusion.
// A sentinel error lets the handler map it to its own status and message.
//
// In practice a grocery basket is about as benign as prompts get, so this is
// defensive. But an unhandled refusal surfaces to the user as an empty recipe
// with no explanation, which is a worse bug than the one it prevents.
var ErrRefused = errors.New("the model declined to generate recipes for this basket")

// ErrNoBasket guards the case that would otherwise produce confident nonsense:
// asking for recipes when there's nothing to cook. Without this the model would
// cheerfully invent a meal plan from an empty ingredient list, which is exactly
// the hallucination failure mode this package's design is meant to avoid.
var ErrNoBasket = errors.New("basket has no items to cook")

// Client wraps the Anthropic SDK.
//
// Same shape as internal/fdc and internal/kroger: a struct holding a configured
// transport, so timeouts and retries are owned in one place and tests can swap
// the base URL for an httptest server.
type Client struct {
	api anthropic.Client
}

// New builds a client. The SDK's option.WithAPIKey is the only required piece;
// everything else has sensible defaults.
//
// baseURL is normally "" (the real API) and is set only by tests, which point
// it at a local httptest server. That's the same testability escape hatch as
// fdc.Client.BaseURL, and it's why none of my tests need a network or a key.
func New(apiKey, baseURL string) *Client {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &Client{api: anthropic.NewClient(opts...)}
}

// Ingredient is one line of the basket, in the terms a cook cares about.
//
// Note what ISN'T here: price, product id, store id, brand. Those are real
// fields on store.BasketLine, and every one of them is noise to a recipe
// writer. Passing the whole struct would cost tokens on every request and give
// the model information it has no business acting on — I don't want "the salmon
// was expensive, so use less of it" as an emergent behavior. Narrow the input
// to exactly what the task needs.
type Ingredient struct {
	Food  string  // "Lentils, dry" — the food, not the SKU
	Grams float64 // total for the WEEK, already summed across packs
}

// Request is everything the recipe generator needs.
type Request struct {
	Ingredients []Ingredient

	// Daily macro targets, so the model can portion meals toward them rather
	// than inventing arbitrary serving sizes.
	ProteinGDaily int
	CarbsGDaily   int
	FatGDaily     int

	// Dietary tags the solver already enforced. Passing them is belt-and-braces:
	// a vegan basket physically contains no chicken, so the model cannot add it
	// to a recipe. But it CAN suggest "serve with a fried egg" as a garnish, and
	// naming the constraint prevents that.
	DietTags []string
}

// Meal is one recipe.
type Meal struct {
	Name        string   `json:"name"`
	Servings    int      `json:"servings"`
	Ingredients []string `json:"ingredients"`
	Steps       []string `json:"steps"`
	// PrepMinutes is a rough total. Not load-bearing — it's the kind of detail
	// that makes a plan feel usable rather than theoretical.
	PrepMinutes int `json:"prep_minutes"`
}

// Plan is the structured answer.
type Plan struct {
	Meals []Meal `json:"meals"`
	// Notes covers what doesn't fit a per-meal field: storage advice, what to
	// batch-cook on Sunday, what to freeze.
	Notes []string `json:"notes"`
}

// systemPrompt sets the role and the hard rules.
//
// WHY A SYSTEM PROMPT rather than putting this in the user turn: the system
// prompt is the stable, per-application instruction, and the user turn is the
// per-request data. Keeping them separate is what makes the whole system prompt
// cacheable across requests and keeps the variable part small — and it draws a
// clean line between "how this assistant behaves" and "what it was asked".
const systemPrompt = `You are a practical meal-prep cook writing a week of recipes from a grocery basket that has already been purchased.

Hard rules:
- Use ONLY the ingredients listed. Do not add any food that is not in the list, not even small amounts.
- You may assume salt, pepper, water, and cooking oil are already in the kitchen. Nothing else.
- The gram amounts are the TOTAL for the whole week. Do not exceed them across all recipes combined.
- Prefer batch cooking: fewer, larger recipes that reheat well beat seven distinct dinners.
- Be honest about repetition. A cheap optimized basket produces a repetitive week, and pretending otherwise is not helpful.

Respond with a single JSON object and nothing else — no preamble, no markdown fences.

Schema:
{
  "meals": [
    {
      "name": "string",
      "servings": integer,
      "ingredients": ["string, with the amount, e.g. '400g lentils'"],
      "steps": ["string"],
      "prep_minutes": integer
    }
  ],
  "notes": ["string"]
}`

// Generate asks Claude for a week of meals.
func (c *Client) Generate(ctx context.Context, req Request) (Plan, error) {
	if len(req.Ingredients) == 0 {
		return Plan{}, ErrNoBasket
	}

	// The timeout is applied HERE rather than being baked into the client, so
	// a caller with its own deadline (an HTTP request context, say) still wins
	// if theirs is shorter. context.WithTimeout takes the minimum of the two.
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	msg, err := c.api.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     model,
		MaxTokens: maxTokens,
		System: []anthropic.TextBlockParam{
			{Text: systemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(buildPrompt(req))),
		},
	})
	if err != nil {
		// Wrapping keeps the SDK's error (which carries the HTTP status and
		// the API's own message) inspectable by errors.As at the call site,
		// while the prefix says which of my subsystems failed.
		return Plan{}, fmt.Errorf("calling Claude: %w", err)
	}

	// THE REFUSAL CHECK, before touching the content.
	//
	// A refusal still returns 200 with a well-formed message — the failure is
	// in stop_reason, not the status code. Checking content first would mean
	// trying to parse an apology as JSON and reporting "invalid JSON", which
	// hides the real reason completely.
	if msg.StopReason == anthropic.StopReasonRefusal {
		return Plan{}, ErrRefused
	}

	// Concatenate every text block. Today there's normally one, but that's an
	// observation about current behavior rather than a guarantee, and assuming
	// Content[0] is the whole answer is how you silently truncate a response.
	var sb strings.Builder
	for _, block := range msg.Content {
		if t := block.AsText(); t.Text != "" {
			sb.WriteString(t.Text)
		}
	}

	plan, err := parsePlan(sb.String())
	if err != nil {
		return Plan{}, err
	}
	return plan, nil
}

// buildPrompt renders the request as the user turn.
//
// Plain readable text, not JSON. Models handle both, but text is what I can
// eyeball in a log to see exactly what was asked — and a prompt I can't read
// is a prompt I can't debug.
func buildPrompt(req Request) string {
	var sb strings.Builder

	sb.WriteString("Here is the basket, with total grams for the week:\n\n")
	for _, ing := range req.Ingredients {
		// %.0f — gram precision. "412.7g of carrots" implies a kitchen scale
		// accuracy that neither the solver's rounding nor a real shopper has.
		fmt.Fprintf(&sb, "- %s: %.0fg\n", ing.Food, ing.Grams)
	}

	fmt.Fprintf(&sb, "\nDaily macro targets: %dg protein, %dg carbs, %dg fat.\n",
		req.ProteinGDaily, req.CarbsGDaily, req.FatGDaily)

	if len(req.DietTags) > 0 {
		// Underscores to spaces: my database says "gluten_free" because it's a
		// column value; a prompt should read like English.
		tags := strings.ReplaceAll(strings.Join(req.DietTags, ", "), "_", " ")
		fmt.Fprintf(&sb, "Dietary requirements already satisfied by this basket: %s.\n", tags)
	}

	sb.WriteString("\nWrite the week's meals as JSON matching the schema.")
	return sb.String()
}

// parsePlan decodes the model's JSON.
//
// The system prompt asks for bare JSON, and in practice that's what comes back.
// But "in practice" is not "always" — a model can wrap output in ```json
// fences out of habit, and a strict parser that dies on that is brittle for no
// good reason. So: trim fences if present, then parse strictly. Forgiving about
// the wrapper, strict about the contents.
func parsePlan(raw string) (Plan, error) {
	s := strings.TrimSpace(raw)

	if strings.HasPrefix(s, "```") {
		// Drop the opening fence line (which may be ``` or ```json) and the
		// closing one.
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
		s = strings.TrimSpace(s)
	}

	var plan Plan
	if err := json.Unmarshal([]byte(s), &plan); err != nil {
		return Plan{}, fmt.Errorf("model returned unparseable JSON: %w", err)
	}

	// An empty meals array parses fine and is useless. Catching it here means
	// the handler never returns a 200 with nothing in it — a "successful"
	// response the user can't do anything with is a failure wearing a success's
	// status code.
	if len(plan.Meals) == 0 {
		return Plan{}, errors.New("model returned no meals")
	}

	return plan, nil
}
