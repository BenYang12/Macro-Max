import { grams, percentOf } from "@/lib/format";

// MacroBar — used four times: protein, carbs, fat, calories.
//
// ---------------------------------------------------------------------------
// WHY THIS IS A METER AND NOT A CHART
//
// The data's job here is "did I hit my target?", which is a single magnitude
// against a single reference. That's a meter, not a plot: there's no series to
// compare, no time axis, and no second dimension. Reaching for a chart library
// would be machinery for one number.
//
// COLOR JOB: STATUS, not categorical.
//
// The four bars are not four categories that need distinguishing — they're
// four instances of the same question with a two-state answer: met, or under.
// So this uses the STATUS palette (good / warning), not categorical slots.
//
//   good    #0ca30c   at or above target
//   warning #fab219   below target
//
// I ran these through the palette validator in both modes:
//   CVD separation      ΔE 11.3 (protan)  — target is >= 8, passes
//   Normal-vision floor ΔE 27.6           — floor is 15, passes
//   Contrast (light)    amber at 1.79:1   — sub-3:1, and that is BY DESIGN for
//                                            the status palette
//
// That last one is not something I get to shrug off. The documented mitigation
// is that a status color never carries meaning alone, so every bar here also
// has:
//   1. an ICON (✓ vs ↓) that differs by shape, not hue
//   2. the exact numbers in text ("1,284 g / 1,260 g — 102%")
//   3. a ring on the fill, so the mark has an edge even when its interior is
//      low-contrast against the surface
//
// Any one of those would let a reader who can't see the color get the answer.
// The color is the fastest channel, never the only one.
//
// TEXT WEARS TEXT TOKENS. The label and numbers are in normal ink, not in the
// status color — a colored mark sits beside them and carries the state. Tinting
// the text would make the type harder to read for no added information.
// ---------------------------------------------------------------------------

interface MacroBarProps {
  label: string;
  achieved: number;
  target: number;
  /** "g" for macros, "kcal" for the calorie line. */
  unit?: "g" | "kcal";
  /**
   * Calories are a CEILING, not a floor — being under is the good outcome.
   * Flipping the comparison here rather than at the call site keeps the
   * met/under logic in one place.
   */
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

  // The BAR is clamped to 100% so a 300% overshoot doesn't blow out the
  // layout — but the TEXT always reports the true number. Clamping the visual
  // while hiding the real value would be lying with the chart.
  const width = Math.min(100, Math.max(0, pct));

  const fill = met ? "#0ca30c" : "#fab219";
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
        <span className="font-medium text-slate-800 dark:text-slate-200">
          <span aria-hidden="true" className="mr-1.5 font-bold" style={{ color: fill }}>
            {icon}
          </span>
          {label}
        </span>
        {/* tabular-nums keeps the digits from shifting as values change. */}
        <span className="tabular-nums text-slate-600 dark:text-slate-400">
          {fmt(achieved)} / {fmt(target)} — {Math.round(pct)}%
        </span>
      </div>

      {/*
        role="meter" is the correct role for "a value within a known range",
        as opposed to progressbar, which implies a task advancing toward
        completion. aria-valuetext carries the human sentence so a screen
        reader announces "1,284 grams of 1,260, target met" rather than the
        bare number 102.
      */}
      <div
        role="meter"
        aria-valuenow={Math.round(pct)}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuetext={`${label}: ${fmt(achieved)} of ${fmt(target)}, ${Math.round(pct)} percent, ${state}`}
        className="h-2.5 w-full overflow-hidden rounded-full bg-slate-200 dark:bg-slate-800"
      >
        {/*
          Thin mark, 4px rounded data-end, anchored to the baseline (left edge).
          The inset ring gives the fill an edge so it stays visible even where
          its interior contrast against the surface is low — the relief the
          validator's contrast WARN requires.

          No transition: this appears once per solve rather than animating
          continuously, and a width transition here would just delay the answer.
        */}
        <div
          className="h-full rounded-full ring-1 ring-inset ring-black/20 dark:ring-white/20"
          style={{ width: `${width}%`, backgroundColor: fill }}
        />
      </div>
    </div>
  );
}
