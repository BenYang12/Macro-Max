import type { ReactNode } from "react";

type Tone = "info" | "warn" | "error";

const TONES: Record<Tone, { box: string; icon: string; label: string }> = {
  info: {
    box: "border-[var(--green)] bg-[var(--green-wash)] text-[var(--ink)]",
    icon: "i",
    label: "Information",
  },
  warn: {
    box: "border-[var(--warning)] bg-[var(--paper-raised)] text-[var(--ink)]",
    icon: "!",
    label: "Warning",
  },
  error: {
    box: "border-[var(--red)] bg-[var(--paper-raised)] text-[var(--ink)]",
    icon: "!",
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
    <div className={`border p-4 ${t.box}`}>
      <div className="flex items-start gap-3">
        <span aria-hidden="true" className="mt-0.5 grid size-5 select-none place-items-center rounded-full border border-current text-xs font-bold leading-none">
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
