import type { RecipePlan } from "@/lib/api";

interface RecipeListProps {
  plan: RecipePlan;
}

export function RecipeList({ plan }: RecipeListProps) {
  return (
    <div className="flex flex-col gap-6">
      {plan.meals.map((meal, i) => (
        <article
          key={i}
          className="border-t border-[var(--rule)] py-4"
        >
          <header className="mb-2 flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
            <h3 className="font-semibold">{meal.name}</h3>
            <p className="text-sm text-[var(--ink-soft)]">
              {meal.servings} servings · {meal.prep_minutes} min
            </p>
          </header>

          <ul className="mb-3 flex flex-wrap gap-x-3 gap-y-1 text-sm text-[var(--ink-soft)]">
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
        <div className="text-sm text-[var(--ink-soft)]">
          <h3 className="mb-1 font-semibold text-[var(--ink)]">
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
