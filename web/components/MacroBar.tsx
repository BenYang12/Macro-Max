import { grams, percentOf } from "@/lib/format";

interface MacroBarProps {
  label: string;
  achieved: number;
  target: number;
  unit?: "g" | "kcal";
  isCeiling?: boolean;
}

export function MacroBar({
  label,
  achieved,
  target,
  unit = "g",
  isCeiling = false,
}: MacroBarProps) {
  const pct = percentOf(achieved, target);
  const met = isCeiling ? achieved <= target : achieved >= target - 0.01;

  const width = Math.min(100, Math.max(0, pct));

  const fill = met ? "var(--green)" : "var(--warning)";
  const icon = met ? "✓" : "↓";
  const state = met
    ? isCeiling
      ? "under the ceiling"
      : "target met"
    : "below target";

  const fmt = (v: number) =>
    unit === "g" ? grams(v) : `${Math.round(v).toLocaleString("en-US")} kcal`;

  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-baseline justify-between gap-3 text-sm">
        <span className="font-semibold text-[var(--ink)]">
          <span aria-hidden="true" className="mr-1.5 font-bold" style={{ color: fill }}>
            {icon}
          </span>
          {label}
        </span>
        <span className="tabular-nums text-[var(--ink-soft)]">
          {fmt(achieved)} / {fmt(target)} — {Math.round(pct)}%
        </span>
      </div>

      <div
        role="meter"
        aria-valuenow={Math.round(pct)}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuetext={`${label}: ${fmt(achieved)} of ${fmt(target)}, ${Math.round(pct)} percent, ${state}`}
        className="h-2 w-full overflow-hidden bg-[var(--green-wash)]"
      >
        <div
          className="h-full ring-1 ring-inset ring-black/20 dark:ring-white/20"
          style={{ width: `${width}%`, backgroundColor: fill }}
        />
      </div>
    </div>
  );
}
