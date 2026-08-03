interface NumberFieldProps {
  /** Must match the Go server's JSON field name — see the note below. */
  name: string;
  label: string;
  suffix?: string;
  value: string;
  onChange: (value: string) => void;
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

  const describedBy = [hint ? hintId : null, error ? errorId : null]
    .filter(Boolean)
    .join(" ");

  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={name} className="text-sm font-semibold text-[var(--ink)]">
        {label}
        {suffix && (
          <span className="font-normal text-[var(--ink-soft)]"> ({suffix})</span>
        )}
        {optional && (
          <span className="font-normal text-[var(--ink-soft)]"> — optional</span>
        )}
      </label>

      {hint && (
        <p id={hintId} className="text-xs text-[var(--ink-soft)]">
          {hint}
        </p>
      )}

      <input
        id={name}
        name={name}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        inputMode="numeric"
        aria-invalid={error ? true : undefined}
        aria-describedby={describedBy || undefined}
        className={[
          "border px-3 py-2 text-base tabular-nums",
          "bg-[var(--paper-raised)] text-[var(--ink)]",
          "focus-visible:outline-none",
          error
            ? "border-[var(--red)]"
            : "border-[var(--rule)]",
        ].join(" ")}
      />

      {error && (
        <p id={errorId} role="alert" className="text-sm text-[var(--red-dark)]">
          {error}
        </p>
      )}
    </div>
  );
}
