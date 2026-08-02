// NumberField — the most-reused component in the app. Five instances: protein,
// carbs, fat, calorie ceiling, and budget.
//
// Everything accessibility-related about a text input is solved once, HERE,
// rather than five times in the page:
//
//   - a real <label htmlFor> tied to the input's id, so clicking the label
//     focuses the field and a screen reader announces the two together
//   - the UNIT lives in the label text, not the placeholder. A placeholder
//     disappears the moment you type, so "g per day" as a placeholder is
//     invisible exactly when you're checking whether you typed the right unit
//   - inputMode="numeric" brings up a number keypad on phones without the
//     spinner arrows and scroll-wheel hazards of type="number"
//   - the error is joined to the input by aria-describedby AND marked
//     role="alert", so it's both discoverable on focus and announced when it
//     appears

interface NumberFieldProps {
  /** Must match the Go server's JSON field name — see the note below. */
  name: string;
  label: string;
  /** Rendered in the label, e.g. "g / day". Never a placeholder. */
  suffix?: string;
  value: string;
  onChange: (value: string) => void;
  /**
   * A message from the server's 422 `fields` map.
   *
   * The reason `name` must match the API's field name exactly: the page looks
   * errors up as fields[name], so a mismatch silently drops the message rather
   * than failing loudly. Naming the inputs protein_g_daily, budget_cents_weekly
   * and so on makes that lookup a direct hit with no translation table.
   */
  error?: string;
  optional?: boolean;
  hint?: string;
}

export function NumberField({
  name,
  label,
  suffix,
  value,
  onChange,
  error,
  optional,
  hint,
}: NumberFieldProps) {
  const errorId = `${name}-error`;
  const hintId = `${name}-hint`;

  // aria-describedby takes a space-separated list of ids. Filtering out the
  // absent ones keeps me from pointing at an element that doesn't exist, which
  // screen readers handle badly.
  const describedBy = [hint ? hintId : null, error ? errorId : null]
    .filter(Boolean)
    .join(" ");

  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={name} className="text-sm font-medium text-slate-800 dark:text-slate-200">
        {label}
        {suffix && (
          <span className="font-normal text-slate-500 dark:text-slate-400"> ({suffix})</span>
        )}
        {optional && (
          <span className="font-normal text-slate-500 dark:text-slate-400"> — optional</span>
        )}
      </label>

      {hint && (
        <p id={hintId} className="text-xs text-slate-500 dark:text-slate-400">
          {hint}
        </p>
      )}

      <input
        id={name}
        name={name}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        inputMode="numeric"
        // aria-invalid tells assistive tech the field is in an error state,
        // which is information the red border conveys only visually.
        aria-invalid={error ? true : undefined}
        aria-describedby={describedBy || undefined}
        className={[
          "rounded-md border px-3 py-2 text-base tabular-nums",
          "bg-white text-slate-900 dark:bg-slate-900 dark:text-slate-100",
          "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-offset-2",
          "focus-visible:ring-sky-600 dark:focus-visible:ring-offset-slate-950",
          error
            ? "border-red-600 dark:border-red-500"
            : "border-slate-300 dark:border-slate-700",
        ].join(" ")}
      />

      {error && (
        // role="alert" makes this announced the moment it appears, which
        // matters because the error arrives from the server AFTER submit —
        // a sighted user sees it, and without this a screen reader user
        // would not be told anything happened.
        <p id={errorId} role="alert" className="text-sm text-red-700 dark:text-red-400">
          {error}
        </p>
      )}
    </div>
  );
}
