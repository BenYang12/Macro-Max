import type { BasketItem } from "@/lib/api";
import { centsPerGram, dollars, grams, proteinCostPerGram } from "@/lib/format";

// BasketTable — the results grid.
//
// A REAL <table>, not a grid of divs. This is genuinely tabular data (rows are
// products, columns are attributes), and a real table gives screen-reader users
// navigation that no amount of ARIA bolted onto divs matches: jump by cell,
// hear the column header re-announced with each value. <caption> names the
// table, <th scope="col"> ties headers to columns, and <th scope="row"> on the
// food name makes it the row's identity rather than just its first cell.
//
// NO SORTING. It's the one thing in my accessibility spec that needs
// aria-sort and buttons inside <th>, and on a basket of ~8 rows it buys
// almost nothing. Dropped deliberately rather than forgotten.

interface BasketTableProps {
  items: BasketItem[];
  /**
   * food name -> protein per 100g, from GET /v1/foods.
   *
   * The solve response doesn't carry nutrition — it returns what to buy, not
   * what's in it. So the cost-per-gram-protein column needs this side lookup.
   * Passing it in rather than fetching here keeps this component pure and
   * render-only.
   */
  proteinByFood: Record<string, number>;
}

export function BasketTable({ items, proteinByFood }: BasketTableProps) {
  return (
    // overflow-x-auto so a narrow screen scrolls THIS element rather than the
    // whole page — a horizontally scrolling body is the classic mobile bug.
    <div className="overflow-x-auto">
      <table className="w-full border-collapse text-sm">
        <caption className="sr-only">
          Your basket: {items.length} products, with quantity, weight, cost, and
          the cost per gram of protein each one delivers.
        </caption>

        <thead>
          <tr className="border-b border-slate-300 text-left dark:border-slate-700">
            <th scope="col" className="py-2 pr-3 font-semibold">
              Food
            </th>
            <th scope="col" className="py-2 pr-3 font-semibold">
              Product
            </th>
            <th scope="col" className="py-2 pr-3 text-right font-semibold">
              Packs
            </th>
            <th scope="col" className="py-2 pr-3 text-right font-semibold">
              Eat
            </th>
            <th scope="col" className="py-2 pr-3 text-right font-semibold">
              Cost
            </th>
            {/*
              The column that explains the basket. Everything else says WHAT to
              buy; this says why the optimizer chose it over the alternatives.
            */}
            <th scope="col" className="py-2 text-right font-semibold">
              <span className="whitespace-nowrap">¢ / g protein</span>
            </th>
          </tr>
        </thead>

        <tbody>
          {items.map((it) => {
            const per = proteinCostPerGram(
              it.cost_cents,
              it.grams,
              proteinByFood[it.food_name] ?? 0,
            );

            return (
              <tr
                key={it.product_id}
                className="border-b border-slate-200 last:border-0 dark:border-slate-800"
              >
                <th
                  scope="row"
                  className="py-2 pr-3 text-left font-medium text-slate-900 dark:text-slate-100"
                >
                  {it.food_name}
                </th>
                <td className="py-2 pr-3 text-slate-600 dark:text-slate-400">
                  {it.product_name}
                </td>
                {/* tabular-nums + right alignment so the digits form columns
                    that can be compared down the page. */}
                <td className="whitespace-nowrap py-2 pr-3 text-right tabular-nums">{it.packs}</td>
                <td className="whitespace-nowrap py-2 pr-3 text-right tabular-nums">{grams(it.grams)}</td>
                <td className="whitespace-nowrap py-2 pr-3 text-right tabular-nums">
                  {dollars(it.cost_cents)}
                </td>
                <td className="whitespace-nowrap py-2 text-right tabular-nums text-slate-600 dark:text-slate-400">
                  {/* An em dash, not "0" or "Infinity": a fat or produce line
                      genuinely has no protein cost, and a dash says that
                      without implying it's free. */}
                  {per === null ? "—" : centsPerGram(per)}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
