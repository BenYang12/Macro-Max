import type { ReactNode } from "react";

// Callout — a bordered message box, used three times: the cost-vs-budget
// summary, the infeasible result, and any unexpected server error.
//
// The reason it's one component with a `tone` prop rather than three separate
// components: the three differ only in color and icon, and everything else
// (spacing, border, heading structure, how the body flows) is identical. Three
// copies would drift apart the first time I adjusted padding.
//
// TONE IS NEVER THE ONLY SIGNAL. Each tone carries an icon glyph as well as a
// color, because a red border and an amber border are the same border to
// someone who can't distinguish them. Same rule as the macro bars.

type Tone = "info" | "warn" | "error";

const TONES: Record<Tone, { box: string; icon: string; label: string }> = {
  info: {
    box: "border-sky-300 bg-sky-50 text-sky-950 dark:border-sky-800 dark:bg-sky-950/40 dark:text-sky-100",
    icon: "✓",
    label: "Information",
  },
  warn: {
    box: "border-amber-400 bg-amber-50 text-amber-950 dark:border-amber-700 dark:bg-amber-950/40 dark:text-amber-100",
    icon: "!",
    label: "Warning",
  },
  error: {
    box: "border-red-400 bg-red-50 text-red-950 dark:border-red-800 dark:bg-red-950/40 dark:text-red-100",
    icon: "×",
    label: "Error",
  },
};

interface CalloutProps {
  tone: Tone;
  title: string;
  children?: ReactNode;
}

export function Callout({ tone, title, children }: CalloutProps) {
  const t = TONES[tone];

  return (
    <div className={`rounded-lg border p-4 ${t.box}`}>
      <div className="flex items-start gap-3">
        {/*
          aria-hidden on the glyph, with the meaning carried by the visually
          hidden text next to it. A screen reader announcing "times" or "check
          mark" would be noise; announcing "Error" is the actual information.
        */}
        <span aria-hidden="true" className="mt-0.5 select-none text-lg font-bold leading-none">
          {t.icon}
        </span>
        <div className="min-w-0 flex-1">
          <span className="sr-only">{t.label}: </span>
          <h3 className="font-semibold">{title}</h3>
          {children && <div className="mt-1 text-sm">{children}</div>}
        </div>
      </div>
    </div>
  );
}
