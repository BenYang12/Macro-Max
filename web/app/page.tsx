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
  solveForTarget,
  type ApiErrorBody,
  type Basket,
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

    const budgetCents =
      budgetOverrideCents ?? dollarsToCents(form.budget_dollars_weekly);

    try {
      const { basket } = await solveForTarget({
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

  // Move focus to the results after a successful solve, so a keyboard or
  // screen-reader user isn't left at the submit button wondering what happened.
  useEffect(() => {
    if (basket && resultsRef.current) resultsRef.current.focus();
  }, [basket]);

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
            <div className="flex flex-wrap gap-x-6 gap-y-2">
              {DIET_TAGS.map((t) => (
                <label key={t.value} className="flex items-center gap-2 text-sm">
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
            className="rounded-md bg-sky-700 px-5 py-2.5 font-semibold text-white hover:bg-sky-800 disabled:opacity-60"
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
              {solvedFor.kcal && (
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
          </div>
        )}
      </div>
    </main>
  );
}
