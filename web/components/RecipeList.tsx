import type { RecipePlan } from "@/lib/api";

// RecipeList — renders the meal plan from POST /v1/recipes.
//
// Deliberately the plainest component in the app. Everything above it (the
// bars, the basket table) is showing PROVEN output: the solver's answer is
// verifiable arithmetic. This is showing a language model's suggestion, and
// dressing it up in the same visual weight would imply the same confidence.
// The quiet styling is an honesty signal, not laziness.
//
// STRUCTURE FIRST: an <ol> for steps because the order is the meaning — step 3
// before step 2 is a different (and worse) recipe — and a <ul> for ingredients
// because their order isn't. Using the semantically right list means a screen
// reader announces "list, 5 items" and numbers the steps without any ARIA.

interface RecipeListProps {
  plan: RecipePlan;
}

export function RecipeList({ plan }: RecipeListProps) {
  return (
    <div className="flex flex-col gap-6">
      {plan.meals.map((meal, i) => (
        <article
          // The index is a safe key HERE, and it's worth saying why since
          // index-as-key is usually a bug: this list is never reordered,
          // filtered, or appended to — it's replaced wholesale by the next
          // response. The failure mode index keys cause (React reusing the
          // wrong DOM node after a reorder) cannot occur.
          key={i}
          className="rounded-lg border border-slate-200 p-4 dark:border-slate-800"
        >
          <header className="mb-2 flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
            {/* h3, not h2: the page's "Recipes for the week" heading is the h2,
                and skipping a level breaks the outline a screen-reader user
                navigates by. */}
            <h3 className="font-semibold">{meal.name}</h3>
            <p className="text-sm text-slate-600 dark:text-slate-400">
              {meal.servings} servings · {meal.prep_minutes} min
            </p>
          </header>

          <ul className="mb-3 flex flex-wrap gap-x-3 gap-y-1 text-sm text-slate-600 dark:text-slate-400">
            {meal.ingredients.map((ing, j) => (
              <li key={j} className="tabular-nums">
                {ing}
              </li>
            ))}
          </ul>

          <ol className="flex list-decimal flex-col gap-1 pl-5 text-sm">
            {meal.steps.map((step, j) => (
              <li key={j}>{step}</li>
            ))}
          </ol>
        </article>
      ))}

      {plan.notes.length > 0 && (
        <div className="text-sm text-slate-600 dark:text-slate-400">
          <h3 className="mb-1 font-semibold text-slate-800 dark:text-slate-200">
            Notes
          </h3>
          <ul className="flex list-disc flex-col gap-1 pl-5">
            {plan.notes.map((note, i) => (
              <li key={i}>{note}</li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
