import type { BasketItem } from "@/lib/api";
import { centsPerGram, dollars, grams, proteinCostPerGram } from "@/lib/format";

interface BasketTableProps {
  items: BasketItem[];
  proteinByFood: Record<string, number>;
}

export function BasketTable({ items, proteinByFood }: BasketTableProps) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full border-collapse text-sm">
        <caption className="sr-only">
          Your basket: {items.length} products, with quantity, weight, cost, and
          the cost per gram of protein each one delivers.
        </caption>

        <thead>
          <tr className="border-b border-[var(--green)] text-left text-xs uppercase tracking-[.08em]">
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
                className="border-b border-[var(--rule)] last:border-0"
              >
                <th
                  scope="row"
                  className="py-3 pr-3 text-left font-semibold text-[var(--ink)]"
                >
                  {it.food_name}
                </th>
                <td className="py-3 pr-3 text-[var(--ink-soft)]">
                  {it.product_name}
                </td>
                <td className="whitespace-nowrap py-2 pr-3 text-right tabular-nums">{it.packs}</td>
                <td className="whitespace-nowrap py-2 pr-3 text-right tabular-nums">{grams(it.grams)}</td>
                <td className="whitespace-nowrap py-2 pr-3 text-right tabular-nums">
                  {dollars(it.cost_cents)}
                </td>
                <td className="whitespace-nowrap py-2 text-right tabular-nums text-[var(--ink-soft)]">
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
