# Deploy Macro-Max

The portfolio deployment uses three free services and does not require a
billing account:

- **Vercel Hobby** hosts the Next.js frontend in `web/`.
- **Render Free** runs one container containing both the Go API and Python
  solver.
- **Neon Free** provides PostgreSQL.

Redis is intentionally omitted in production. It is only a solve cache, and the
API already falls back to solving without it.

## 1. Create the Neon database

1. Sign in at <https://console.neon.tech> with GitHub.
2. Create a project named `macro-max` in a US East region.
3. On the project dashboard, copy the **pooled** connection string.
4. Keep the connection string private. It becomes `DATABASE_URL` on Render and
   `NEON_DATABASE_URL` in GitHub Actions.

The Render container automatically applies migrations 1–5 and idempotently
seeds the food catalog whenever it starts.

## 2. Deploy the backend on Render

1. Sign in at <https://dashboard.render.com> with GitHub.
2. Choose **New > Blueprint**.
3. Select the `BenYang12/Macro-Max` repository.
4. Render detects the root `render.yaml`. Approve the `macro-max-api` free web
   service.
5. Enter the requested environment variables:

| Variable | Value |
|---|---|
| `DATABASE_URL` | Neon pooled connection string |
| `WEB_APP_URL` | Expected Vercel origin, such as `https://macro-max.vercel.app` |
| `KROGER_CLIENT_ID` | Optional; leave blank initially |
| `KROGER_CLIENT_SECRET` | Optional; leave blank initially |
| `FDC_API_KEY` | Optional |
| `ANTHROPIC_API_KEY` | Optional and not free |

6. Create the Blueprint and wait for the deploy to become **Live**.
7. Copy the service URL, such as `https://macro-max-api.onrender.com`.
8. Verify `https://YOUR-SERVICE.onrender.com/v1/healthcheck` returns an `ok`
   status.

The free service sleeps after inactivity. The first request after sleep can be
slow because it wakes the API, solver, and database.

## 3. Deploy the frontend on Vercel

1. Sign in at <https://vercel.com> with GitHub and import this repository.
2. Set **Root Directory** to `web`.
3. Keep the detected Next.js build settings.
4. Add these environment variables for Production, Preview, and Development:

```text
API_URL=https://YOUR-SERVICE.onrender.com
NEXT_PUBLIC_KROGER_CART=false
NEXT_PUBLIC_ANTHROPIC_RECIPES=false
```

5. Deploy and copy the final `https://YOUR-PROJECT.vercel.app` origin.
6. If that origin differs from `WEB_APP_URL` on Render, update the Render value
   and redeploy the backend.

## 4. Optional Kroger integration

In the Kroger developer portal, register this exact callback:

```text
https://YOUR-PROJECT.vercel.app/api/kroger/callback
```

Enable `product.compact` and `cart.basic:write`, then set the two Kroger secrets
on Render. Change `NEXT_PUBLIC_KROGER_CART` to `true` on Vercel and redeploy.

For scheduled price updates, add these GitHub Actions secrets:

| Secret | Value |
|---|---|
| `NEON_DATABASE_URL` | Neon pooled connection string |
| `KROGER_CLIENT_ID` | Kroger application client ID |
| `KROGER_CLIENT_SECRET` | Kroger application client secret |

Run **Refresh Kroger prices** manually with `dry_run` enabled before the first
real import.

## Production checks

1. Render reports the service as **Live**.
2. The Render health endpoint returns `database: ok`.
3. The Vercel page displays the University Place catalog.
4. A normal target returns an optimized basket.
5. A deliberately low budget reports the minimum feasible amount.
6. If Kroger is enabled, OAuth returns through Vercel and fills a small cart.
