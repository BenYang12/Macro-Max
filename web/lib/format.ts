// format.ts — the display layer for numbers.
//
// THIS FILE IS THE ONLY PLACE MONEY BECOMES A DECIMAL. My whole stack keeps
// money as integer cents — Go int64, Postgres INT, protobuf int64 — precisely
// so that 0.1 + 0.2 never enters the picture. Converting at render time, in one
// function, means the float exists for exactly as long as it takes to draw a
// string and never gets stored, summed, or compared.

/** 3175 -> "$31.75" */
export function dollars(cents: number): string {
  return new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
  }).format(cents / 100);
}

/** 1284.37 -> "1,284 g" — grocery weights don't deserve decimals. */
export function grams(g: number): string {
  return `${Math.round(g).toLocaleString("en-US")} g`;
}

/** 0.6997 -> "0.70¢" — cost per gram of protein, the "why this item" column. */
export function centsPerGram(value: number): string {
  return `${value.toFixed(2)}¢`;
}

/**
 * percentOf clamps to a sane display range.
 *
 * A basket can overshoot a macro target substantially (buying enough protein
 * often drags carbs along with it), and a 400%-wide bar would blow out the
 * layout. I clamp the BAR at 100 but report the true number in the text, so the
 * visual stays readable while the actual value is never hidden — the same
 * never-lie-with-the-chart rule as not using color alone.
 */
export function percentOf(achieved: number, target: number): number {
  if (target <= 0) return 100;
  return (achieved / target) * 100;
}

/**
 * proteinCostPerGram answers "what did a gram of protein cost me in this line?"
 *
 * This is the column that turns the results table from a shopping list into an
 * explanation: it shows WHY the optimizer picked tuna over chicken. Computed
 * here on the client from data already in the solve response, so it costs no
 * extra request and no backend change.
 *
 * Returns null when the item supplies no protein — a fat or produce line — so
 * the table can render a dash instead of Infinity.
 */
export function proteinCostPerGram(
  costCents: number,
  grams: number,
  proteinPer100g: number,
): number | null {
  const proteinGrams = (grams * proteinPer100g) / 100;
  if (proteinGrams <= 0) return null;
  return costCents / proteinGrams;
}

/** "180" -> 180, "" -> null. Used for the optional calorie ceiling. */
export function parseOptionalInt(raw: string): number | null {
  const trimmed = raw.trim();
  if (trimmed === "") return null;
  const n = Number(trimmed);
  return Number.isFinite(n) ? Math.round(n) : null;
}

/**
 * dollarsToCents converts what the user typed into the integer my API wants.
 *
 * Math.round, not truncation: "120.99" * 100 is 12098.999... in binary floating
 * point, and truncating would quietly charge a cent less. Same reasoning as
 * DollarsToCents in the Kroger client — this is the mirror image of that
 * boundary, on the way in instead of out.
 */
export function dollarsToCents(raw: string): number {
  const n = Number(raw.trim());
  if (!Number.isFinite(n)) return 0;
  return Math.round(n * 100);
}
