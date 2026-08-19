"use client";

import { useEffect, useRef, useState } from "react";

import { BasketTable } from "@/components/BasketTable";
import { Callout } from "@/components/Callout";
import { MacroBar } from "@/components/MacroBar";
import { NumberField } from "@/components/NumberField";
import {
  ApiError,
  isInfeasible,
  isValidationError,
  listFoods,
  solveForTarget,
  type ApiErrorBody,
  type Basket,
  startKrogerCart,
} from "@/lib/api";
import { dollars, dollarsToCents, parseOptionalInt } from "@/lib/format";

const DIET_TAGS = [
  { value: "vegetarian", label: "Vegetarian" },
  { value: "vegan", label: "Vegan" },
  { value: "gluten_free", label: "Gluten-free" },
  { value: "dairy_free", label: "Dairy-free" },
];
const CART_ENABLED = process.env.NEXT_PUBLIC_KROGER_CART === "true";
const FORM_FIELD_ORDER = [
  "protein_g_daily",
  "carbs_g_daily",
  "fat_g_daily",
  "calories_max_daily",
  "budget_cents_weekly",
] as const;

interface FormState {
  protein_g_daily: string;
  carbs_g_daily: string;
  fat_g_daily: string;
  calories_max_daily: string;
  budget_dollars_weekly: string;
}

const INITIAL: FormState = {
  protein_g_daily: "180",
  carbs_g_daily: "200",
  fat_g_daily: "60",
  calories_max_daily: "",
  budget_dollars_weekly: "120",
};

const CART_ERRORS: Record<string, string> = {
  authorization_denied: "Kroger authorization was canceled.",
  invalid_callback: "Kroger returned an incomplete authorization response.",
  invalid_state: "The Kroger authorization could not be verified. Please try again.",
  expired_state: "The Kroger authorization took too long. Please try again.",
  browser_mismatch: "The authorization must finish in the browser where it started.",
  basket_not_found: "The selected basket is no longer available. Solve it again.",
  basket_load_failed: "Macro-Max could not reload the selected basket.",
  wrong_store: "This basket is not from the supported University Place store.",
  empty_basket: "The selected basket has no products to add.",
  invalid_product: "The basket contains an item Kroger cannot add.",
  missing_cart_scope: "Kroger did not grant permission to update the cart.",
  token_exchange_failed: "Kroger could not complete authorization.",
  cart_add_failed: "Kroger authorized the request but could not update the cart.",
  invalid_origin: "The cart request did not come from this application.",
  unreachable: "Macro-Max could not reach the cart service.",
};

function cartErrorMessage(code?: string): string {
  return CART_ERRORS[code ?? ""] ?? "The Kroger cart request could not be completed.";
}

export default function Home() {
  const [form, setForm] = useState<FormState>(INITIAL);
  const [diet, setDiet] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [basket, setBasket] = useState<Basket | null>(null);
  const [error, setError] = useState<ApiErrorBody | null>(null);
  const [solvedFor, setSolvedFor] = useState<{ p: number; c: number; f: number; kcal: number | null } | null>(null);
  const [proteinByFood, setProteinByFood] = useState<Record<string, number>>({});
	const [foodMetadataError, setFoodMetadataError] = useState(false);
	const [targetId, setTargetId] = useState<number | null>(null);
	const [capabilityToken, setCapabilityToken] = useState<string | null>(null);

  const [cartResult, setCartResult] = useState<{ success: boolean; code?: string } | null>(null);
  const [cartLoading, setCartLoading] = useState(false);

  const resultsRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    listFoods()
      .then((foods) => {
        const map: Record<string, number> = {};
        for (const f of foods) map[f.name] = f.protein_g_per_100g;
        setProteinByFood(map);
      })
      .catch(() => setFoodMetadataError(true));
  }, []);

  const set = (key: keyof FormState) => (value: string) =>
    setForm((f) => ({ ...f, [key]: value }));

  async function runSolve(budgetOverrideCents?: number) {
    setLoading(true);
    setError(null);

    setCartResult(null);

    const budgetCents =
      budgetOverrideCents ?? dollarsToCents(form.budget_dollars_weekly);

    try {
		const { target, basket, capabilityToken: newCapabilityToken } = await solveForTarget({
        label: "web",
        protein_g_daily: Number(form.protein_g_daily),
        carbs_g_daily: Number(form.carbs_g_daily),
        fat_g_daily: Number(form.fat_g_daily),
        calories_max_daily: parseOptionalInt(form.calories_max_daily),
        budget_cents_weekly: budgetCents,
        diet_tags: diet,
      });

      setBasket(basket);
		setTargetId(target.id);
		setCapabilityToken(newCapabilityToken);
      setSolvedFor({
        p: Number(form.protein_g_daily) * 7,
        c: Number(form.carbs_g_daily) * 7,
        f: Number(form.fat_g_daily) * 7,
        kcal: parseOptionalInt(form.calories_max_daily)
          ? parseOptionalInt(form.calories_max_daily)! * 7
          : null,
      });
      if (budgetOverrideCents) {
        setForm((f) => ({
          ...f,
          budget_dollars_weekly: (budgetOverrideCents / 100).toFixed(2),
        }));
      }
    } catch (e) {
      if (e instanceof ApiError) {
        setError(e.body);
        setBasket(null);
      } else {
        setError({
          code: "unreachable",
          message: "Something went wrong. Is the Go server running? (`make run`)",
        });
        setBasket(null);
      }
    } finally {
      setLoading(false);
    }
  }

  async function runAddToCart() {
	if (targetId === null || capabilityToken === null) return;
    // Open synchronously while this function is still handling the click.
    // Waiting for the API first makes browsers classify window.open as an
    // unsolicited popup and block it.
    const popup = window.open(
      "about:blank",
      "macro-max-kroger-cart",
      "popup,width=560,height=760,resizable=yes,scrollbars=yes",
    );
    if (popup) popup.opener = null;
    setCartLoading(true);
    setCartResult(null);
    try {
		const authorizeURL = await startKrogerCart(targetId, capabilityToken);
      if (popup) {
        popup.location.assign(authorizeURL);
        popup.focus();
      } else {
        // Popup blocking should not make the cart action unusable.
        window.location.assign(authorizeURL);
      }
    } catch (error) {
      popup?.close();
      setCartLoading(false);
      setCartResult({
        success: false,
        code: error instanceof ApiError ? error.body.code : "unreachable",
      });
    }
  }

  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const success = params.get("cart") === "success";
    const errorCode = params.get("cart_error");
    if (!success && !errorCode) return;
    setCartResult(success ? { success: true } : { success: false, code: errorCode ?? "unknown" });
    params.delete("cart");
    params.delete("cart_error");
    const query = params.toString();
    window.history.replaceState(
      {},
      "",
      window.location.pathname + (query ? `?${query}` : "") + window.location.hash,
    );
  }, []);

  useEffect(() => {
    if (basket && resultsRef.current) resultsRef.current.focus();
  }, [basket]);

  useEffect(() => {
    if (!error || !isValidationError(error)) return;
    const first = FORM_FIELD_ORDER.find((f) => error.fields[f]);
    if (first) document.getElementById(first)?.focus();
  }, [error]);

  const fieldErrors =
    error && isValidationError(error) ? error.fields : ({} as Record<string, string>);
  const hasHighlightedField = FORM_FIELD_ORDER.some((field) => fieldErrors[field]);

  return (
    <main id="main" className="page-shell">
      <header className="masthead">
        <h1 className="wordmark">Macro-Max</h1>
        <p className="promise">
          Build a practical week of groceries around your nutrition targets and
          budget. The optimizer compares real shelf prices, buys whole packs, and
          proves when no cheaper basket meets the same constraints.
        </p>
      </header>

      <aside className="store-banner" aria-label="Supported grocery store">
        <div>
          <strong>Harris Teeter · University Place</strong>
          <p>2110 S. Estes Drive, Chapel Hill, NC</p>
        </div>
        <span className="price-stamp">Prices from this location</span>
      </aside>

      <div className="workspace">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          runSolve();
        }}
        className="target-panel flex flex-col gap-6"
      >
        <div>
          <h2 className="section-title">Set the week.</h2>
          <p className="section-copy mt-3 text-sm">Enter daily nutrition goals and a weekly grocery ceiling.</p>
        </div>
        <fieldset className="flex flex-col gap-4">
          <legend className="mb-1 text-xl font-semibold">Daily targets</legend>

          <div className="grid grid-cols-3 gap-3">
            <NumberField
              name="protein_g_daily"
              label="Protein"
              suffix="g / day"
              value={form.protein_g_daily}
              onChange={set("protein_g_daily")}
              error={fieldErrors.protein_g_daily}
            />
            <NumberField
              name="carbs_g_daily"
              label="Carbs"
              suffix="g / day"
              value={form.carbs_g_daily}
              onChange={set("carbs_g_daily")}
              error={fieldErrors.carbs_g_daily}
            />
            <NumberField
              name="fat_g_daily"
              label="Fat"
              suffix="g / day"
              value={form.fat_g_daily}
              onChange={set("fat_g_daily")}
              error={fieldErrors.fat_g_daily}
            />
          </div>

          <NumberField
            name="calories_max_daily"
            label="Calorie ceiling"
            suffix="kcal / day"
            optional
            hint="Leave blank and the solver derives one from your macros."
            value={form.calories_max_daily}
            onChange={set("calories_max_daily")}
            error={fieldErrors.calories_max_daily}
          />
        </fieldset>

        <fieldset className="form-rule flex flex-col gap-4">
          <legend className="mb-1 text-xl font-semibold">Budget &amp; diet</legend>

          <NumberField
            name="budget_cents_weekly"
            label="Weekly budget"
            suffix="$ / week"
            hint="Macros are daily; the budget is weekly. The solver multiplies by seven."
            value={form.budget_dollars_weekly}
            onChange={set("budget_dollars_weekly")}
            error={fieldErrors.budget_cents_weekly}
          />

          <div>
            <span className="text-sm font-semibold text-[var(--ink)]">
              Dietary filters
            </span>
            <p className="mb-2 text-xs text-[var(--ink-soft)]">
              A food must carry every tag you check.
            </p>
            <div className="flex flex-wrap gap-x-2 gap-y-1 -mx-2">
              {DIET_TAGS.map((t) => (
                <label
                  key={t.value}
                  className="flex min-h-10 cursor-pointer items-center gap-2 px-2 py-2 text-sm hover:bg-[var(--green-wash)]"
                >
                  <input
                    type="checkbox"
                    checked={diet.includes(t.value)}
                    onChange={(e) =>
                      setDiet((d) =>
                        e.target.checked
                          ? [...d, t.value]
                          : d.filter((x) => x !== t.value),
                      )
                    }
                    className="h-4 w-4 accent-[var(--green)]"
                  />
                  {t.label}
                </label>
              ))}
            </div>
          </div>
        </fieldset>

        <div className="form-rule flex flex-col items-start gap-2">
          <button
            type="submit"
            disabled={loading}
            aria-busy={loading}
            className="primary-action w-full"
          >
            {loading ? "Solving…" : "Find the cheapest basket"}
          </button>
          <p aria-live="polite" className="text-sm text-[var(--ink-soft)]">
            {loading ? "Solving — this usually takes under a second." : ""}
          </p>
        </div>
      </form>

      <div ref={resultsRef} tabIndex={-1} className="results-panel focus:outline-none">
        {cartResult?.success && (
          <Callout tone="info" title="Basket added to your Kroger cart">
            <p>
              Review quantities and complete checkout on Kroger. Adding the same basket
              again will duplicate its items.
            </p>
          </Callout>
        )}
        {cartResult && !cartResult.success && (
          <Callout tone="error" title="Could not add the basket">
            <p>
              {cartErrorMessage(cartResult.code)} Your Macro-Max basket is unchanged.
            </p>
          </Callout>
        )}
        {error && isInfeasible(error) && (
          <Callout
            tone="warn"
            title={`No basket fits ${dollars(dollarsToCents(form.budget_dollars_weekly))} per week.`}
          >
            <p>
              These macros need at least{" "}
              <strong>{dollars(error.min_feasible_budget_cents)}</strong> per week at
              this store.
            </p>
            <button
              type="button"
              onClick={() => runSolve(error.min_feasible_budget_cents)}
              className="secondary-action mt-3 text-sm"
            >
              Use {dollars(error.min_feasible_budget_cents)} and solve again
            </button>
          </Callout>
        )}

        {error && isValidationError(error) && (
          <Callout
            tone="error"
            title={hasHighlightedField ? "Check the highlighted fields" : "Store catalog unavailable"}
          >
            <p>
              {hasHighlightedField
                ? error.message
                : "No purchasable products are available for this store right now. Please try again shortly."}
            </p>
          </Callout>
        )}

        {error && !isInfeasible(error) && !isValidationError(error) && (
          <Callout tone="error" title="Something went wrong">
            <p>{error.message}</p>
          </Callout>
        )}

        {!basket && !error && !cartResult && (
          <div className="results-empty">
            <p className="max-w-sm">Your optimized shopping list will appear here, with its cost and nutrition proof.</p>
          </div>
        )}

        {basket && solvedFor && (
          <div className="flex flex-col gap-8">
            <section className="result-summary">
              <div>
                <h2 className="section-title">Your least-cost basket</h2>
                <p className="result-price mt-4">{dollars(basket.total_cost_cents)}</p>
              </div>
              <p className="result-meta">
                of {dollars(dollarsToCents(form.budget_dollars_weekly))} budget<br />
                {basket.items.length} products · solved in{" "}
                {basket.solve_seconds < 0.01
                  ? "under 10ms"
                  : `${basket.solve_seconds.toFixed(2)}s`}{" "}
                · {basket.status === "optimal" ? "proven optimal" : "feasible"}
              </p>
            </section>

            <section className="flex flex-col gap-4">
              <h2 className="text-2xl font-semibold">Nutrition proof</h2>
              <MacroBar label="Protein" achieved={basket.achieved.protein_g} target={solvedFor.p} />
              <MacroBar label="Carbs" achieved={basket.achieved.carbs_g} target={solvedFor.c} />
              <MacroBar label="Fat" achieved={basket.achieved.fat_g} target={solvedFor.f} />
              {solvedFor.kcal !== null && (
                <MacroBar
                  label="Calories"
                  achieved={basket.achieved.calories}
                  target={solvedFor.kcal}
                  unit="kcal"
                  isCeiling
                />
              )}
            </section>

            <section className="result-section flex flex-col gap-3">
              <h2 className="text-2xl font-semibold">Shopping list</h2>
              {foodMetadataError && (
                <Callout tone="warn" title="Nutrition details unavailable">
                  <p>Cost per gram of protein could not be loaded. Basket quantities and prices are unaffected.</p>
                </Callout>
              )}
              <BasketTable items={basket.items} proteinByFood={proteinByFood} />
            </section>

            {CART_ENABLED && <section className="result-section flex flex-col gap-4">
              <h2 className="text-2xl font-semibold">Take it with you</h2>

              <div className="action-dock flex flex-wrap gap-3">
                {CART_ENABLED && <button
                  type="button"
                  onClick={runAddToCart}
                  disabled={cartLoading}
                  aria-busy={cartLoading}
                  className="primary-action text-sm"
                >
                  {cartLoading ? "Opening Kroger…" : "Add to my Kroger cart"}
                </button>}
              </div>

            </section>}
          </div>
        )}
      </div>
      </div>
    </main>
  );
}
