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

The Render container automatically applies migrations 1–6 and idempotently
seeds the food catalog whenever it starts.

### Irreversible capability migration

Migration `000006` adds the digests that authorize access to targets. Its down
migration deliberately fails instead of dropping those digests: removing them
would silently remove authorization from existing records. Roll back application
code only to a capability-aware build that is compatible with schema version 6,
or forward-fix the current build. Never deploy pre-capability code against schema
version 6 or later: its writes omit the required digest and its reads do not
enforce capability ownership.

An attempted rollback across `000006` can leave `golang-migrate` at dirty
version 6 even though the down migration fails before changing the schema. If
that happens:

1. Confirm `user_targets.capability_digest` still exists and migration 6's
   schema is intact.
2. Mark that unchanged schema as version 6 again:

   ```sh
   migrate -path migrations -database "$DATABASE_URL" force 6
   ```

3. Run `migrate -path migrations -database "$DATABASE_URL" up` and verify the
   API healthcheck.

Do not force version 5 or manually drop `capability_digest`; either action can
remove the ownership boundary protecting persisted targets.

## 2. Deploy the backend on Render

1. Sign in at <https://dashboard.render.com> with GitHub.
2. Choose **New > Blueprint**.
3. Select the `BenYang12/Macro-Max` repository.
4. Render detects the root `render.yaml`. Approve the `macro-max-api` free web
   service.
5. Enter the one requested environment variable:

| Variable | Value |
|---|---|
| `DATABASE_URL` | Neon pooled connection string |

6. Create the Blueprint and wait for the deploy to become **Live**.
7. Copy the service URL, such as `https://macro-max-api.onrender.com`.
8. Verify `https://YOUR-SERVICE.onrender.com/v1/healthcheck` returns an `ok`
   status.

The free service sleeps after inactivity. The first request after sleep can be
slow because it wakes the API, solver, and database.

To enable paid recipe generation, set both `ANTHROPIC_API_KEY` and a long,
random `RECIPE_ACCESS_KEY` on the backend. Only a trusted server-side caller
may send `RECIPE_ACCESS_KEY` as `X-Recipe-Key`; do not expose it through a
`NEXT_PUBLIC_*` variable or an anonymous browser bundle. Leave the browser
recipe flag disabled in the deployment below. If the host publishes its proxy
network ranges, they may be configured as comma-separated
`TRUSTED_PROXY_CIDRS`; otherwise leave it unset so forwarding headers are not
trusted.

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

## 4. Optional Kroger integration

In the Kroger developer portal, register this exact callback:

```text
https://YOUR-PROJECT.vercel.app/api/kroger/callback
```

Enable `product.compact` and `cart.basic:write`, then add `KROGER_CLIENT_ID`,
`KROGER_CLIENT_SECRET`, and `WEB_APP_URL` (the Vercel origin) on Render. Change
`NEXT_PUBLIC_KROGER_CART` to `true` on Vercel and redeploy both services.

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
