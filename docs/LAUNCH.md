# Launch checklist

Where the project actually stands, and the exact remaining steps to a live demo.

## State of the code (verified 2026-08-19)

Phases 1–7 of `docs/PLAN.md` are implemented. `go build ./...` and `go test ./...`
pass across all 9 packages. Everything below is deployment and polish — there is
no missing feature work blocking a launch.

## The one non-obvious ordering trap

`cmd/seed` seeds **foods only**. Every row in `products` comes from
`cmd/krogeringest`. `deploy/render_entrypoint.py` runs `migrate up` and `seed` on
boot, so a freshly deployed Render service has a nutrition catalog and **zero
products** — solves will fail with "store catalog unavailable" until the price
importer has run against Neon at least once.

So the Kroger ingest is **not** the optional last step that `docs/DEPLOY.md`
implies. It must run before the deployment is demo-able. Step 4 below.

---

## Step 0 — Pre-flight, locally (~10 min)

```sh
make lint && make test && make test-int && make solver-test
make web-build
cd web && npm run test:e2e
```

All should be green today. Commit and push so CI is green on `main` before any
host reads the repo.

## Step 1 — Neon Postgres (~5 min)

1. Create project `macro-max`, US East, at <https://console.neon.tech>.
2. Copy the **pooled** connection string. It is used in two places, both below:
   Render's `DATABASE_URL` and the GitHub secret `NEON_DATABASE_URL`.

## Step 2 — Render backend (~15 min, mostly build time)

New > Blueprint > `BenYang12/Macro-Max`. Render reads `render.yaml`.

Environment variables to set on the `macro-max-api` service:

| Variable | Value | Required? |
|---|---|---|
| `DATABASE_URL` | Neon pooled string | yes |
| `REDIS_URL` | `disabled` | already in `render.yaml` |
| `KROGER_CLIENT_ID` | from `.env` | only for the cart flow |
| `KROGER_CLIENT_SECRET` | from `.env` | only for the cart flow |
| `WEB_APP_URL` | `https://YOUR-PROJECT.vercel.app` | required whenever Kroger creds are set |
| `ANTHROPIC_API_KEY` | `sk-ant-...` | only for `POST /v1/recipes` |
| `RECIPE_ACCESS_KEY` | `openssl rand -hex 32` | required if `ANTHROPIC_API_KEY` is set |

`WEB_APP_URL` is chicken-and-egg with Step 3. Deploy Render first without the
Kroger variables, take the Vercel URL from Step 3, then add all four Kroger/web
variables and redeploy.

Verify: `https://YOUR-SERVICE.onrender.com/v1/healthcheck` returns
`"database":"ok"`.

## Step 3 — Vercel frontend (~5 min)

Import the repo, **Root Directory = `web`**. Environment variables for
Production, Preview, and Development:

```text
API_URL=https://YOUR-SERVICE.onrender.com
NEXT_PUBLIC_KROGER_CART=false
```

`API_URL` is mandatory — `web/next.config.ts` throws on a production build
without it. The browser never calls Render directly; Next rewrites `/api/*` to
`${API_URL}/v1/*`, which is also why no CORS config exists.

## Step 4 — Fill the product catalog (required, ~10 min)

GitHub repo > Settings > Secrets and variables > Actions:

| Secret | Value |
|---|---|
| `NEON_DATABASE_URL` | Neon pooled string |
| `KROGER_CLIENT_ID` | from `.env` |
| `KROGER_CLIENT_SECRET` | from `.env` |

Then Actions > **Refresh Kroger prices** > Run workflow:

1. Run once with `dry_run` **checked**. Read the log: it should resolve the
   curated mapping and report parsed net weights, no writes.
2. Run again with `dry_run` **unchecked**. This is what makes the deployment
   work.

After that it runs nightly at 10:17 UTC on its own.

## Step 5 — Turn on the Kroger cart (~10 min, optional)

1. Kroger developer portal: register the redirect URI exactly as
   `https://YOUR-PROJECT.vercel.app/api/kroger/callback`, scopes
   `product.compact` + `cart.basic:write`.
2. Render: add `KROGER_CLIENT_ID`, `KROGER_CLIENT_SECRET`, `WEB_APP_URL`.
3. Vercel: `NEXT_PUBLIC_KROGER_CART=true`.
4. Redeploy both. Test with a small basket — cart writes are additive.

## Step 6 — Production verification

1. Render is **Live**; `/v1/healthcheck` reports `database: ok`.
2. The Vercel page lists the University Place catalog (proves Step 4 landed).
3. A normal target returns an optimized basket with ≥3 protein sources,
   ≥2 vegetables, and no food over 30% of calories.
4. A deliberately low budget returns `min_feasible_budget_cents`, not a crash.
5. If enabled, Kroger OAuth returns through Vercel and fills a cart.

Note the free tier's cold start: the first request after idle wakes the API, the
solver, and Neon. Warn anyone you demo to, or hit the URL a minute beforehand.

---

## Portfolio polish (after it is live)

These are the remaining items from Phase 7 of `docs/PLAN.md`, none of which
block deployment:

- **Demo GIF in the README.** The architecture diagram is already there; the
  missing piece is a 15-second capture of target → solve → basket → cart.
- **"Why not just ChatGPT?" section.** The strongest thing about this project is
  that a MILP *proves* optimality and an LLM cannot. Say so explicitly in the
  README, above the fold.
- **Live demo link** at the top of the README once the Vercel URL exists.
- Deferred stretch features: price-drop alerts (`price_history` + `LAG()`) and
  pantry mode (subtract owned grams pre-solve).

## Secrets reference

| Secret | Local file | Production home |
|---|---|---|
| `KROGER_CLIENT_ID` / `_SECRET` | `.env` (set) | Render env + GitHub Actions secrets |
| `FDC_API_KEY` | `.env` (set) | not needed in production |
| `DATABASE_URL` | `.env` (local Postgres on 55432) | Render env (Neon) |
| `NEON_DATABASE_URL` | — | GitHub Actions secret only |
| `ANTHROPIC_API_KEY` | `.env` (commented placeholder) | Render env |
| `RECIPE_ACCESS_KEY` | `.env` (commented placeholder) | Render env |
| `WEB_APP_URL` | `.env` (localhost:3000) | Render env (Vercel origin) |
| `API_URL` | — | Vercel env only |

`.env` is gitignored (`.gitignore:12`). `RECIPE_ACCESS_KEY` must never appear in
a `NEXT_PUBLIC_*` variable — that would publish it in the browser bundle.
