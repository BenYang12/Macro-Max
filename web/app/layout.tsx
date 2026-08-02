import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "MacroCart — the cheapest basket that hits your macros",
  description:
    "Computes the provably cheapest real grocery basket that meets your macro targets within a weekly budget, using live Harris Teeter prices.",
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    // lang is required for screen readers to pick the right pronunciation
    // rules. It's one attribute and it's wrong on a startling number of sites.
    <html lang="en">
      <body className="bg-white text-slate-900 antialiased dark:bg-slate-950 dark:text-slate-100">
        {/*
          SKIP LINK. Visually hidden until focused, then it appears as the first
          tab stop. Without it, a keyboard user has to tab through the entire
          form to reach results after every solve. Here the form IS most of the
          page, so this is genuinely useful rather than ceremonial.
        */}
        <a
          href="#main"
          className="sr-only focus:not-sr-only focus:absolute focus:left-4 focus:top-4 focus:z-50 focus:rounded-md focus:bg-sky-700 focus:px-4 focus:py-2 focus:text-white"
        >
          Skip to main content
        </a>
        {children}
      </body>
    </html>
  );
}
