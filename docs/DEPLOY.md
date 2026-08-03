# Deployment

Macro-Max deploys as three services:

- Go API on Fly.io
- private Python solver on Fly.io
- Next.js frontend on Vercel

Postgres and Redis are runtime dependencies. The only supported product catalog is Harris Teeter at University Place, Kroger location `09700117`.

## 1. Deploy the solver and API

Create the Fly apps and managed data services, then configure the API secrets:

```bash
fly apps create macrocart-solver
fly apps create macrocart-api
fly postgres create --name macrocart-db
fly postgres attach macrocart-db --app macrocart-api
fly redis create

fly secrets set --app macrocart-api \
  KROGER_CLIENT_ID=... \
  KROGER_CLIENT_SECRET=... \
  WEB_APP_URL=https://YOUR-PROJECT.vercel.app
```

Optional API secrets:

```bash
fly secrets set --app macrocart-api FDC_API_KEY=... ANTHROPIC_API_KEY=...
```

Deploy the solver first so the API can reach `macrocart-solver.internal:50051`:

```bash
fly deploy --config solver/fly.toml
fly deploy
```

Apply migrations 1–5 to the production database, seed the food catalog, and run the Kroger importer once. There is no Kroger token table and no token-encryption secret.

## 2. Deploy the frontend

Import `web/` as a Vercel project and set:

```text
API_URL=https://macrocart-api.fly.dev
NEXT_PUBLIC_KROGER_CART=true
```

Only if recipes are configured on the API, also set:

```text
NEXT_PUBLIC_ANTHROPIC_RECIPES=true
```

Use the final Vercel production origin as `WEB_APP_URL` on Fly. It must be an origin only: HTTPS scheme and host, with no path, query, fragment, or trailing application route.

## 3. Register the Kroger callback

Register the frontend-origin callback exactly:

```text
https://YOUR-PROJECT.vercel.app/api/kroger/callback
```

The Vercel app proxies that path to the Go callback. Kroger redirect matching is exact, including scheme, host, and path. Enable `cart.basic:write` for the developer application.

Macro-Max does not store Kroger accounts or OAuth tokens. Each cart fill starts a new authorization, uses its access token during the callback, and discards it.

## 4. Schedule price refreshes

Configure the GitHub repository:

| Setting | Kind | Value |
|---|---|---|
| `FLY_API_TOKEN` | Actions secret | App-scoped Fly SSH token |
| `MACRO_MAX_FLY_API_APP` | Actions variable | API app name, such as `macrocart-api` |

The scheduled workflow runs the importer for location `09700117`; no location variable is accepted. Dispatch it once with **dry run** enabled, review the mappings, then dispatch a real ingest before relying on the schedule.

## Production checks

1. `fly status --app macrocart-solver` reports a healthy private solver.
2. `fly status --app macrocart-api` reports a passing `/v1/healthcheck` check.
3. The Vercel UI displays the University Place address and returns an optimized basket.
4. A low budget reports the minimum feasible amount.
5. A dry-run price refresh succeeds, followed by one real refresh.
6. Kroger OAuth returns through the Vercel callback and fills one deliberately small cart.
7. Recipe generation appears only when both recipe settings are configured.

Cart writes are additive and cannot be undone through the public Kroger API.
