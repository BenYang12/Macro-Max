// Handwritten types mirror this small JSON API without a generation step.

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

// Validation and infeasibility have distinct payloads under HTTP 422.

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

export function isValidationError(b: ApiErrorBody): b is ValidationError {
  return b.code === "validation_failed";
}

export function isInfeasible(b: ApiErrorBody): b is InfeasibleError {
  return b.code === "infeasible";
}

// ---------------------------------------------------------------- the calls

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

async function post<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(`/api${path}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });

  if (!res.ok) {
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

/** Save targets and return their database identifier. */
export async function createTarget(input: TargetInput): Promise<UserTarget> {
  const { target } = await post<{ target: UserTarget }>("/targets", {
    label: input.label,
    protein_g_daily: input.protein_g_daily,
    carbs_g_daily: input.carbs_g_daily,
    fat_g_daily: input.fat_g_daily,
    calories_max_daily: input.calories_max_daily,
    budget_cents_weekly: input.budget_cents_weekly,
    diet_tags: input.diet_tags,
  });
  return target;
}

/** Run the whole-pack optimizer against a saved target. */
export async function solve(targetId: number): Promise<Basket> {
  const { basket } = await post<{ basket: Basket }>("/solve", {
    target_id: targetId,
    integer_packs: true,
  });
  return basket;
}

/** Save a target, then solve it. */
export async function solveForTarget(
  input: TargetInput,
): Promise<{ target: UserTarget; basket: Basket }> {
  const target = await createTarget(input);
  const basket = await solve(target.id);
  return { target, basket };
}

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

/** Generate optional meal suggestions for a solved target. */
export async function generateRecipes(targetId: number): Promise<RecipePlan> {
  const { plan } = await post<{ plan: RecipePlan }>("/recipes", {
    target_id: targetId,
  });
  return plan;
}

export async function startKrogerCart(targetId: number): Promise<string> {
  const { authorize_url } = await post<{ authorize_url: string }>("/kroger/authorize", {
    target_id: targetId,
  });
  return authorize_url;
}
