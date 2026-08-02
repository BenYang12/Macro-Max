// Tailwind v4 config. Two things changed from v3 and both remove work:
//   1. The PostCSS plugin moved to its own package, @tailwindcss/postcss.
//   2. autoprefixer is no longer needed — v4 handles vendor prefixes itself.
// There is also no tailwind.config.ts any more: v4 is CSS-first, so the theme
// lives in globals.css. One fewer config file, which suits this project.
export default {
  plugins: { "@tailwindcss/postcss": {} },
};
