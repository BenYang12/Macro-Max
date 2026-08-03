import type { Metadata } from "next";
import "@fontsource-variable/geist";
import "@fontsource-variable/source-serif-4";
import "./globals.css";

export const metadata: Metadata = {
  title: "Macro-Max — a grocery basket built around your macros",
  description:
    "Computes the provably cheapest real grocery basket that meets your macro targets within a weekly budget, using live Harris Teeter prices.",
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en">
      <body className="antialiased">
        <a
          href="#main"
          className="skip-link"
        >
          Skip to main content
        </a>
        {children}
      </body>
    </html>
  );
}
