"use client";

import { useEffect, useRef, useState } from "react";

import { BasketTable } from "@/components/BasketTable";
import { Callout } from "@/components/Callout";
import { MacroBar } from "@/components/MacroBar";
import { NumberField } from "@/components/NumberField";
import { RecipeList } from "@/components/RecipeList";
import {
  addToKrogerCart,
  ApiError,
  generateRecipes,
  isInfeasible,
  isValidationError,
  solveForTarget,
  type ApiErrorBody,
  type Basket,
  type CartResult,
  type RecipePlan,
} from "@/lib/api";
import { dollars, dollarsToCents, parseOptionalInt } from "@/lib/format";

// The whole app. One client component holding all state, because there are
// exactly two API calls and nothing to cache, revalidate, or share across
// routes. React Query or server actions here would be machinery in search of a
// problem.

// The four dietary tags my seeder writes. A food must carry EVERY checked tag
// to be considered, so checking both vegan and gluten-free narrows hard.
const DIET_TAGS = [
  { value: "vegetarian", label: "Vegetarian" },
  { value: "vegan", label: "Vegan" },
  { value: "gluten_free", label: "Gluten-free" },
  { value: "dairy_free", label: "Dairy-free" },
];

// Form state as STRINGS, not numbers.
//
// An input's value is a string, and storing numbers would mean clearing a field
// gives me NaN or silently snaps to 0. Keeping strings lets "" mean "empty",
// which is what the user actually did, and the conversion happens once at
// submit. It also means the server's validation — not mine — decides what's
// valid, which is the whole point of not duplicating rules.
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

export default function Home() {
  const [form, setForm] = useState<FormState>(INITIAL);
  const [diet, setDiet] = useState<string[]>([]);

  const [loading, setLoading] = useState(false);
  const [basket, setBasket] = useState<Basket | null>(null);
  const [error, setError] = useState<ApiErrorBody | null>(null);
  // The targets that produced the current basket, so the bars can compare
  // achieved against what was actually asked for (weekly = daily x 7).
  const [solvedFor, setSolvedFor] = useState<{ p: number; c: number; f: number; kcal: number | null } | null>(null);

  // food name -> protein per 100g, for the cost-per-gram-protein column.
  const [proteinByFood, setProteinByFood] = useState<Record<string, number>>({});

  // PHASE 7 STATE.
  //
  // targetId is kept because both new endpoints act on "the latest basket for
  // a target" — the solve response carries the basket's CONTENTS but not its
  // row (a cache hit has no row to name), so the target id is the only handle
  // the client actually holds.
  const [targetId, setTargetId] = useState<number | null>(null);

  const [plan, setPlan] = useState<RecipePlan | null>(null);
  const [planLoading, setPlanLoading] = useState(false);
  const [planError, setPlanError] = useState<string | null>(null);

  const [cart, setCart] = useState<CartResult | null>(null);
  const [cartLoading, setCartLoading] = useState(false);
  const [cartError, setCartError] = useState<ApiErrorBody | null>(null);

  const resultsRef = useRef<HTMLDivElement>(null);

  // Load the food catalog once. The solve response says what to buy but not
  // what's in it, so this side lookup is what makes the ¢/g protein column
  // possible without a backend change.
  useEffect(() => {
    fetch("/api/foods")
      .then((r) => (r.ok ? r.json() : null))
      .then((data: { foods?: { name: string; protein_g_per_100g: number }[] } | null) => {
        if (!data?.foods) return;
        const map: Record<string, number> = {};
        for (const f of data.foods) map[f.name] = f.protein_g_per_100g;
        setProteinByFood(map);
      })
      .catch(() => {
        // Non-fatal: without this the ¢/g column shows dashes, and every other
        // part of the page still works. Not worth an error banner.
      });
  }, []);

  const set = (key: keyof FormState) => (value: string) =>
    setForm((f) => ({ ...f, [key]: value }));

  async function runSolve(budgetOverrideCents?: number) {
    setLoading(true);
    setError(null);

    // Clear the Phase 7 results. A new solve produces a new basket, so a meal
    // plan or cart confirmation from the PREVIOUS basket left on screen would
    // be actively wrong — recipes for groceries the user is no longer buying.
    setPlan(null);
    setPlanError(null);
    setCart(null);
    setCartError(null);

    const budgetCents =
      budgetOverrideCents ?? dollarsToCents(form.budget_dollars_weekly);

    try {
      const { target, basket } = await solveForTarget({
        label: "web",
        // Number("") is 0, which the server would accept as a real target of
        // zero. That's fine: the server's own validation is the authority, and
        // a zero protein target is a legal (if odd) request.
        protein_g_daily: Number(form.protein_g_daily),
        carbs_g_daily: Number(form.carbs_g_daily),
        fat_g_daily: Number(form.fat_g_daily),
        calories_max_daily: parseOptionalInt(form.calories_max_daily),
        budget_cents_weekly: budgetCents,
        diet_tags: diet,
      });

      setBasket(basket);
      setTargetId(target.id);
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

  // ------------------------------------------------------------- Phase 7

  async function runRecipes() {
    if (targetId === null) return;
    setPlanLoading(true);
    setPlanError(null);
    try {
      setPlan(await generateRecipes(targetId));
    } catch (e) {
      // A 404 here is NOT a broken endpoint — it means the server has no
      // ANTHROPIC_API_KEY, so the route was never registered. That's a
      // deliberate design choice (the solver is the product; the LLM is a
      // removable finishing layer), and saying "not configured" is honest
      // where a generic error would send someone debugging the wrong thing.
      if (e instanceof ApiError && e.status === 404) {
        setPlanError(
          "Recipe generation isn't configured on this server (no Anthropic API key). Everything else still works — that's the point: the solver doesn't depend on an LLM.",
        );
      } else if (e instanceof ApiError) {
        setPlanError(e.body.message);
      } else {
        setPlanError("Could not reach the API.");
      }
    } finally {
      setPlanLoading(false);
    }
  }

  async function runAddToCart() {
    if (targetId === null) return;
    setCartLoading(true);
    setCartError(null);
    try {
      setCart(await addToKrogerCart(targetId));
    } catch (e) {
      if (e instanceof ApiError) {
        setCartError(e.body);
      } else {
        setCartError({ code: "unreachable", message: "Could not reach the API." });
      }
    } finally {
      setCartLoading(false);
    }
  }

  // Move focus to the results after a successful solve, so a keyboard or
  // screen-reader user isn't left at the submit button wondering what happened.
  useEffect(() => {
    if (basket && resultsRef.current) resultsRef.current.focus();
  }, [basket]);

  // ...and on a VALIDATION failure, move focus to the first field the server
  // rejected instead.
  //
  // This is the half I'd left out. role="alert" on the message means a screen
  // reader ANNOUNCES the problem, but announcing isn't the same as arriving:
  // focus is still sitting on the submit button, and the user now has to
  // shift-tab backwards through the form hunting for which input was wrong.
  // Moving focus puts the cursor in the field they have to fix.
  //
  // FIELD_ORDER exists because objects don't guarantee useful key order and
  // "first" has to mean first ON SCREEN, not first in whatever order the Go
  // server happened to serialize its map. The ids match because NumberField
  // sets id={name} and I named every input after its server field.
  useEffect(() => {
    if (!error || !isValidationError(error)) return;
    const FIELD_ORDER = [
      "protein_g_daily",
      "carbs_g_daily",
      "fat_g_daily",
      "calories_max_daily",
      "budget_cents_weekly",
    ];
    const first = FIELD_ORDER.find((f) => error.fields[f]);
    if (first) document.getElementById(first)?.focus();
  }, [error]);

  // Validation errors come back keyed by the SERVER's field names, which is
  // exactly what I named the inputs — so this is a direct lookup, not a
  // translation table.
  const fieldErrors =
    error && isValidationError(error) ? error.fields : ({} as Record<string, string>);

  return (
    <main id="main" className="mx-auto max-w-3xl px-4 py-10 sm:py-16">
      <header className="mb-8">
        <h1 className="text-3xl font-bold tracking-tight">MacroCart</h1>
        <p className="mt-2 text-slate-600 dark:text-slate-400">
          The <strong>provably cheapest</strong> real grocery basket that hits your
          macro targets within a weekly budget. A mixed-integer program over live
          Harris Teeter prices — whole packs, at least three protein sources, two
          vegetables, and a fruit. Not a guess from a language model.
        </p>
      </header>

      <form
        onSubmit={(e) => {
          e.preventDefault();
          runSolve();
        }}
        className="flex flex-col gap-6"
      >
        {/*
          A fieldset+legend groups the three macros as one concept. A screen
          reader announces "Daily macro targets, protein, grams per day" rather
          than three unrelated inputs.
        */}
        <fieldset className="flex flex-col gap-4">
          <legend className="mb-1 text-lg font-semibold">Daily macro targets</legend>

          <div className="grid gap-4 sm:grid-cols-3">
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

        <fieldset className="flex flex-col gap-4">
          <legend className="mb-1 text-lg font-semibold">Budget &amp; diet</legend>

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
            <span className="text-sm font-medium text-slate-800 dark:text-slate-200">
              Dietary filters
            </span>
            <p className="mb-2 text-xs text-slate-500 dark:text-slate-400">
              A food must carry every tag you check.
            </p>
            {/*
              gap-x-2 not gap-x-6, because the padding below now supplies the
              spacing. The old 16px box with no padding gave each checkbox a
              ~20px-tall hit area — under WCAG 2.5.8 Target Size (Minimum),
              which wants 24x24, and miserable on a phone. The label already
              wraps the input, so growing the LABEL grows the tap target for
              free: py-2 takes it to 40px tall, -mx-2 keeps the row visually
              flush with the text above it despite the new padding.
            */}
            <div className="flex flex-wrap gap-x-2 gap-y-1 -mx-2">
              {DIET_TAGS.map((t) => (
                <label
                  key={t.value}
                  className="flex cursor-pointer items-center gap-2 rounded px-2 py-2 text-sm hover:bg-slate-100 dark:hover:bg-slate-800"
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
                    className="h-4 w-4 rounded border-slate-400"
                  />
                  {t.label}
                </label>
              ))}
            </div>
          </div>
        </fieldset>

        <div className="flex items-center gap-4">
          <button
            type="submit"
            disabled={loading}
            // aria-busy is the machine-readable half of the "Solving…" label.
            // disabled alone says "you can't press this"; aria-busy says WHY.
            aria-busy={loading}
            className="rounded-md bg-sky-700 px-5 py-2.5 font-semibold text-white hover:bg-sky-800 disabled:cursor-not-allowed disabled:opacity-60"
          >
            {loading ? "Solving…" : "Find the cheapest basket"}
          </button>
          {/*
            aria-live="polite" announces the in-flight state without interrupting
            whatever the user is currently hearing. A spinner alone conveys
            nothing to a screen reader.
          */}
          <p aria-live="polite" className="text-sm text-slate-600 dark:text-slate-400">
            {loading ? "Solving — this usually takes under a second." : ""}
          </p>
        </div>
      </form>

      {/* ------------------------------------------------------ results ---- */}

      <div ref={resultsRef} tabIndex={-1} className="mt-10 focus:outline-none">
        {error && isInfeasible(error) && (
          // THE INFEASIBLE STATE — and it is not an error banner.
          //
          // "No basket exists" is a dead end. "Your macros need at least $31.75
          // at this store" is something a person can act on, and it's the one
          // answer a solver can give that a language model cannot: it required
          // proving that no cheaper basket exists.
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
              className="mt-3 rounded-md border border-amber-700 px-3 py-1.5 text-sm font-medium hover:bg-amber-100 dark:hover:bg-amber-900/40"
            >
              Use {dollars(error.min_feasible_budget_cents)} and solve again
            </button>
          </Callout>
        )}

        {error && isValidationError(error) && (
          <Callout tone="error" title="Check the highlighted fields">
            <p>{error.message}</p>
          </Callout>
        )}

        {error && !isInfeasible(error) && !isValidationError(error) && (
          <Callout tone="error" title="Something went wrong">
            <p>{error.message}</p>
          </Callout>
        )}

        {basket && solvedFor && (
          <div className="flex flex-col gap-8">
            <Callout
              tone="info"
              title={`${dollars(basket.total_cost_cents)} of your ${dollars(dollarsToCents(form.budget_dollars_weekly))} weekly budget`}
            >
              <p>
                {basket.items.length} products · solved in{" "}
                {basket.solve_seconds < 0.01
                  ? "under 10ms"
                  : `${basket.solve_seconds.toFixed(2)}s`}{" "}
                · {basket.status === "optimal" ? "proven optimal" : "feasible"}
              </p>
            </Callout>

            <section className="flex flex-col gap-4">
              <h2 className="text-lg font-semibold">Weekly macros achieved</h2>
              <MacroBar label="Protein" achieved={basket.achieved.protein_g} target={solvedFor.p} />
              <MacroBar label="Carbs" achieved={basket.achieved.carbs_g} target={solvedFor.c} />
              <MacroBar label="Fat" achieved={basket.achieved.fat_g} target={solvedFor.f} />
              {/*
                `!== null`, not a bare truthiness check. A calorie ceiling of 0
                is nonsense as a target, but the bug this guards is real and
                generic: `{n && <X/>}` renders the literal 0 on screen when n is
                0, because 0 is falsy but JSX still prints it. Explicit null
                checks are the fix, and the habit is worth keeping everywhere.
              */}
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

            <section className="flex flex-col gap-3">
              <h2 className="text-lg font-semibold">Your basket</h2>
              <BasketTable items={basket.items} proteinByFood={proteinByFood} />
            </section>

            {/* ------------------------------------------------- Phase 7 --- */}

            <section className="flex flex-col gap-4 border-t border-slate-200 pt-8 dark:border-slate-800">
              <h2 className="text-lg font-semibold">What now?</h2>

              <div className="flex flex-wrap gap-3">
                <button
                  type="button"
                  onClick={runRecipes}
                  disabled={planLoading}
                  aria-busy={planLoading}
                  className="rounded-md border border-slate-400 px-4 py-2.5 text-sm font-medium hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-600 dark:hover:bg-slate-800"
                >
                  {planLoading ? "Writing recipes…" : "Turn this into a week of meals"}
                </button>

                <button
                  type="button"
                  onClick={runAddToCart}
                  // Disabled after a SUCCESS, not just while in flight. Kroger's
                  // cart API is additive with no read-back and no undo, so a
                  // second click silently doubles the order. Removing the
                  // affordance is more honest than a confirmation dialog that
                  // trains people to click through it.
                  disabled={cartLoading || cart !== null}
                  aria-busy={cartLoading}
                  className="rounded-md border border-slate-400 px-4 py-2.5 text-sm font-medium hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-600 dark:hover:bg-slate-800"
                >
                  {cartLoading
                    ? "Adding…"
                    : cart
                      ? "Added to your Kroger cart"
                      : "Add to my Kroger cart"}
                </button>
              </div>

              {/* aria-live so a screen-reader user hears the outcome of a
                  button press that changes content far from the button. */}
              <div aria-live="polite" className="flex flex-col gap-4">
                {planError && (
                  <Callout tone="warn" title="No recipes">
                    <p>{planError}</p>
                  </Callout>
                )}

                {cartError && (
                  <Callout
                    tone={cartError.code === "kroger_not_connected" ? "warn" : "error"}
                    title={
                      cartError.code === "kroger_not_connected"
                        ? "Connect your Kroger account first"
                        : "Could not add to cart"
                    }
                  >
                    <p>{cartError.message}</p>
                    {cartError.code === "kroger_not_connected" && (
                      // A real link, not a fetch. This flow ends at Kroger's
                      // own login page, so it has to be a full-page navigation
                      // — an XHR would just fetch their HTML and discard it.
                      <a
                        href="/api/kroger/authorize"
                        className="mt-3 inline-block rounded-md border border-amber-700 px-3 py-1.5 text-sm font-medium hover:bg-amber-100 dark:hover:bg-amber-900/40"
                      >
                        Sign in to Kroger
                      </a>
                    )}
                  </Callout>
                )}

                {cart && (
                  <Callout tone="info" title={`${cart.items_added} items added to your cart`}>
                    <p>{cart.note}</p>
                    {cart.skipped && cart.skipped.length > 0 && (
                      <p className="mt-2">
                        Skipped (no Kroger product id): {cart.skipped.join(", ")}
                      </p>
                    )}
                  </Callout>
                )}

                {plan && (
                  <div className="flex flex-col gap-3">
                    <h3 className="font-semibold">Your week of meals</h3>
                    {/* The honesty line. Everything above this point is proven
                        — the MILP showed no cheaper basket hits these macros.
                        This part is a language model's suggestion, and the UI
                        should not let the two borrow each other's authority. */}
                    <p className="text-sm text-slate-600 dark:text-slate-400">
                      Written by Claude from the basket above. The basket is proven
                      optimal; these recipes are a suggestion.
                    </p>
                    <RecipeList plan={plan} />
                  </div>
                )}
              </div>
            </section>
          </div>
        )}
      </div>
    </main>
  );
}
