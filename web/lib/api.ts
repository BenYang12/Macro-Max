// api.ts — the whole boundary between my frontend and my Go server.
//
// Every type here is HAND-WRITTEN to mirror the JSON envelopes my Go handlers
// produce. I could have generated them, but the API surface is seven endpoints
// and has been stable since Phase 1, so a generator would be more machinery
// than the problem deserves. The tradeoff I'm accepting: if I change a Go json
// tag, TypeScript will not tell me — only a runtime failure will. Keeping every
// type in ONE file is what makes that survivable.

// ---------------------------------------------------------------- the models
//
// Field names are snake_case because they come off the wire that way. I'm
// deliberately NOT camelCasing them at the boundary: a translation layer is one
// more place for the two sides to disagree, and the names read fine as-is.

export interface BasketItem {
  product_id: number;
  product_name: string;
  food_name: string;
  packs: number;
  grams: number;
  cost_cents: number;
}

export interface MacroTotals {
  protein_g: number;
  carbs_g: number;
  fat_g: number;
  calories: number;
}

export interface Basket {
  status: "optimal" | "feasible";
  items: BasketItem[];
  total_cost_cents: number;
  achieved: MacroTotals;
  solve_seconds: number;
}

export interface UserTarget {
  id: number;
  label: string;
  protein_g_daily: number;
  carbs_g_daily: number;
  fat_g_daily: number;
  calories_max_daily: number | null;
  budget_cents_weekly: number;
  store_id: string;
  diet_tags: string[];
  exclude_food_ids: number[];
  created_at: string;
}

// ---------------------------------------------------------------- the errors
//
// My Go server returns TWO DIFFERENT SHAPES under HTTP 422, and telling them
// apart is the single most important thing this file does.
//
//   validation_failed -> has `fields`, a map of input name -> message
//   infeasible        -> has `min_feasible_budget_cents`, and NO `fields`
//
// The second one is not really an error at all: it's the solver's most
// valuable answer ("your macros need at least $X"), which just happens to
// arrive on an error status because there's no basket to return.
//
// A DISCRIMINATED UNION is exactly the tool for this. Once I narrow on
// `code`, TypeScript knows which extra fields exist, so I can't read
// `.fields` on an infeasible response by accident.

export interface ValidationError {
  code: "validation_failed";
  message: string;
  fields: Record<string, string>;
}

export interface InfeasibleError {
  code: "infeasible";
  message: string;
  min_feasible_budget_cents: number;
}

export interface GenericError {
  code: string;
  message: string;
}

export type ApiErrorBody = ValidationError | InfeasibleError | GenericError;

/** ApiError carries the parsed body so callers can narrow on `body.code`. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly body: ApiErrorBody,
  ) {
    super(body.message);
    this.name = "ApiError";
  }
}

// Type guards. Writing these as functions rather than inline `===` checks means
// the narrowing logic exists once and every call site gets it for free.
export function isValidationError(b: ApiErrorBody): b is ValidationError {
  return b.code === "validation_failed";
}

export function isInfeasible(b: ApiErrorBody): b is InfeasibleError {
  return b.code === "infeasible";
}

// ---------------------------------------------------------------- the calls

// The store I ingest prices from: Harris Teeter University Place, 2110 S Estes
// Dr. Hardcoded because it's the only store with live data, so a picker would
// mostly offer choices that produce an empty catalog.
export const STORE_ID = "09700117";

/** What the form collects. Note the units in the names — they are load-bearing. */
export interface TargetInput {
  label: string;
  protein_g_daily: number;
  carbs_g_daily: number;
  fat_g_daily: number;
  calories_max_daily: number | null;
  budget_cents_weekly: number;
  diet_tags: string[];
}

// post is the shared plumbing. Everything below funnels through it so error
// handling exists in exactly one place.
async function post<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(`/api${path}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });

  if (!res.ok) {
    // My server always returns { error: {...} } on failure. If parsing that
    // fails, something is wrong at a lower level than my API (a proxy 502, the
    // Go server not running), so I synthesize a body rather than throwing a
    // confusing JSON parse error at the user.
    let parsed: ApiErrorBody;
    try {
      const json = (await res.json()) as { error?: ApiErrorBody };
      parsed = json.error ?? { code: "unknown", message: res.statusText };
    } catch {
      parsed = {
        code: "unreachable",
        message:
          "Could not reach the API. Is the Go server running on :4000? (`make run`)",
      };
    }
    throw new ApiError(res.status, parsed);
  }

  return (await res.json()) as T;
}

/**
 * createTarget saves what the user asked for and returns the row, including
 * the database-assigned id that solve() needs.
 *
 * NOTE THE EXPLICIT OBJECT LITERAL. My Go handler sets DisallowUnknownFields,
 * so ANY extra key is a 400 — spreading form state in here would break the
 * moment the form grows a field the API doesn't know about. Listing the fields
 * by hand is the safeguard.
 */
export async function createTarget(input: TargetInput): Promise<UserTarget> {
  const { target } = await post<{ target: UserTarget }>("/targets", {
    label: input.label,
    protein_g_daily: input.protein_g_daily,
    carbs_g_daily: input.carbs_g_daily,
    fat_g_daily: input.fat_g_daily,
    calories_max_daily: input.calories_max_daily,
    budget_cents_weekly: input.budget_cents_weekly,
    store_id: STORE_ID,
    diet_tags: input.diet_tags,
  });
  return target;
}

/**
 * solve runs the optimizer against a saved target.
 *
 * integer_packs is always true: that's the Phase 4 MILP, which returns whole
 * packs and enforces variety. The Phase 3 LP is still reachable by sending
 * false, but its answer buys 0.22 of a tub of whey — correct arithmetic that
 * would read as a bug to anyone looking at the UI.
 */
export async function solve(targetId: number): Promise<Basket> {
  const { basket } = await post<{ basket: Basket }>("/solve", {
    target_id: targetId,
    integer_packs: true,
  });
  return basket;
}

/**
 * solveForTarget is the whole user action: save, then solve.
 *
 * Two requests because /v1/solve only accepts a target_id — it has no inline
 * form of the target. That means every click writes a user_targets row, which
 * I've accepted: the rows are a genuine log of what was asked, and baskets
 * already reference them.
 */
export async function solveForTarget(
  input: TargetInput,
): Promise<{ target: UserTarget; basket: Basket }> {
  const target = await createTarget(input);
  const basket = await solve(target.id);
  return { target, basket };
}

// ------------------------------------------------------------------ Phase 7
//
// Two endpoints that both act on the LATEST BASKET FOR A TARGET, which is why
// they take a target id rather than a basket id: the solve response carries the
// basket's contents, not its row (a cache hit has no row to name), so the
// target id is the only identifier the client actually holds.

/** One recipe from POST /v1/recipes. Mirrors recipes.Meal in Go. */
export interface Meal {
  name: string;
  servings: number;
  ingredients: string[];
  steps: string[];
  prep_minutes: number;
}

export interface RecipePlan {
  meals: Meal[];
  notes: string[];
}

/**
 * generateRecipes turns the solved basket into a week of meals.
 *
 * WORTH KNOWING WHEN THIS 404s: the route isn't registered unless the server
 * has an ANTHROPIC_API_KEY. That's deliberate — the solver is the product and
 * the LLM is a finishing layer — so a 404 here means "not configured", not
 * "broken", and the UI says so rather than showing a generic failure.
 */
export async function generateRecipes(targetId: number): Promise<RecipePlan> {
  const { plan } = await post<{ plan: RecipePlan }>("/recipes", {
    target_id: targetId,
  });
  return plan;
}

export interface CartResult {
  items_added: number;
  note: string;
  skipped?: string[];
}

/**
 * addToKrogerCart pushes the basket into the user's real Kroger cart.
 *
 * NOT IDEMPOTENT, and the UI must not pretend otherwise. Kroger's cart API is
 * additive with no way to read the cart back and no way to remove what was
 * added, so calling this twice doubles the quantities. The success message
 * carries that warning, and the button disables itself after a success.
 *
 * A 401 means no Kroger account is connected yet — the user needs to visit
 * /v1/kroger/authorize in a browser first, which is a full-page navigation
 * rather than a fetch because it ends at Kroger's login screen.
 */
export async function addToKrogerCart(targetId: number): Promise<CartResult> {
  const { cart } = await post<{ cart: CartResult }>("/kroger/cart", {
    target_id: targetId,
  });
  return cart;
}
