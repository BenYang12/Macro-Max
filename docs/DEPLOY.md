# Deploying MacroCart

**Status: prepared, not deployed.** Every config file described here is written
and reviewed; none of it has been run. `fly launch` and `vercel deploy` create
billable infrastructure, so that stays a deliberate decision rather than a side
effect of finishing Phase 7.

---

## The shape of it

Three pieces, three places, chosen by what each one actually needs:

| Piece | Where | Why there |
|---|---|---|
| Go API | Fly.io (`macrocart-api`) | Needs a private network to reach the solver, and Fly's `*.internal` DNS gives that for free |
| Python solver | Fly.io (`macrocart-solver`) | Same private network. **No public IP** — see below |
| Next.js frontend | Vercel | It's a Next app; Vercel is the first-party host and the free tier covers it |
| Postgres | Fly Postgres | Colocated with the API — a cross-provider database call on every request is latency for nothing |
| Redis | Fly Redis (Upstash) | Cache only. Losing it costs recomputed solves, nothing more |

The two Fly apps are separate rather than two processes in one app, because they
scale on different axes. The API is I/O-bound and cheap; the solver is CPU- and
memory-bound. Coupling them means paying for solver-sized machines to serve
health checks.

### The solver has no public address, on purpose

`solver/fly.toml` has no `[http_service]` block, so Fly assigns it no public IP.
The only way to reach it is the private network, where the API addresses it as
`macrocart-solver.internal:50051`.

This matters because **the solver has no authentication of any kind.** It trusts
whoever can open a socket to it. The network boundary *is* the security model.
If it ever needs a public address, it needs authentication first — in that
order, not the other way around.

---

## Files

| File | What it does |
|---|---|
| `Dockerfile` | Multi-stage build of the Go API. Ships `api`, `seed`, and `krogeringest` |
| `.dockerignore` | **Keeps `.env` out of the image.** Non-negotiable — see below |
| `fly.toml` | The API app |
| `solver/fly.toml` | The solver app |
| `solver/Dockerfile` | Already existed (Phase 3) |

### `.dockerignore` is a security control, not tidiness

The Dockerfile does `COPY . .`. Without `.dockerignore`, that copies `.env` —
every API key — into a build layer. Layers are immutable and travel with the
image, so deleting the file in a later `RUN` does **not** remove it. Anyone who
can pull the image can recover it with `docker history` or by unpacking the
layer tarball.

Docker has never read `.gitignore`. The two files are unrelated and their
contents legitimately differ.

---

## Deploy order

The order matters — each step depends on the one before it.

### 1. Solver first

The API's health check doesn't depend on the solver, but a deployed API with no
solver serves 404 on `/v1/solve`, which is the whole product.

```bash
fly apps create macrocart-solver
fly deploy --config solver/fly.toml --dockerfile solver/Dockerfile
```

### 2. Data stores

```bash
fly apps create macrocart-api
fly postgres create --name macrocart-db --region iad
fly postgres attach macrocart-db --app macrocart-api   # sets DATABASE_URL
fly redis create                                       # sets REDIS_URL
```

`attach` sets `DATABASE_URL` as a secret automatically. Don't set it by hand.

### 3. Secrets

```bash
fly secrets set --app macrocart-api \
  FDC_API_KEY=... \
  KROGER_CLIENT_ID=... \
  KROGER_CLIENT_SECRET=... \
  ANTHROPIC_API_KEY=... \
  TOKEN_ENCRYPTION_KEY=$(openssl rand -hex 32)
```

**`TOKEN_ENCRYPTION_KEY` is permanent once set.** It encrypts stored Kroger
refresh tokens; changing it makes every already-stored token undecryptable, and
the only recovery is sending users back through Kroger's consent screen.

Secrets never go in `fly.toml` — that file is committed. `fly secrets` encrypts
them at rest and injects them as environment variables at boot, which is exactly
what `internal/config` already reads.

### 4. API

```bash
fly deploy --app macrocart-api
```

### 5. Schema and data

Migrations ship inside the image, so run them from a machine that has them:

```bash
fly ssh console --app macrocart-api
# inside:
migrate -path /migrations -database "$DATABASE_URL" up
seed
krogeringest -location 09700117
```

### 6. Frontend

```bash
cd web
vercel                       # first run links the project
vercel env add API_URL       # https://macrocart-api.fly.dev   <- NO /v1
vercel --prod
```

`web/next.config.ts` already reads `API_URL` for its rewrite destination, so no
code change is needed — the proxy that avoids CORS locally does the same job in
production.

**`API_URL` is an origin, not a base path.** The rewrite is
`${API_URL}/v1/:path*`, so it appends `/v1` itself. Including `/v1` in the env
var produces `/v1/v1/solve` and a 404 on every call.

### 7. Point Kroger at the deployed callback

On the Kroger developer app, add:

```
https://macrocart-api.fly.dev/v1/kroger/callback
```

and set `KROGER_REDIRECT_URI` in `fly.toml` to the identical string.
**Identical** means scheme, host, port, path, and trailing slash. A mismatch is
the most common OAuth failure and Kroger's error won't say which part disagreed.

---

## What will actually go wrong

Roughly in order of likelihood.

**`x509: certificate signed by unknown authority`** — the runtime image is
missing CA certificates. This is why the Dockerfile's final stage is `alpine`
with `ca-certificates` rather than `scratch`. It looks like a certificate
problem; it's a missing-file problem.

**Kroger OAuth fails with a generic error after deploying** — `redirect_uri`
mismatch, nine times out of ten. Compare the registered value and
`KROGER_REDIRECT_URI` character by character.

**The API can't reach the solver** — check the region. `SOLVER_ADDR` uses
`*.internal`, which only resolves inside the Fly organization, and both apps
must be in the same `primary_region` or every solve pays a cross-region round
trip.

**Solve times out after idle** — the API scales to zero; the solver deliberately
does not (`min_machines_running = 1`). A cold OR-Tools import is seconds, long
enough to exceed the gRPC deadline. If the solver ever gets `auto_stop`, this
becomes an intermittent failure that's very hard to reproduce.

**Deploys hang for the full grace period** — a `CMD` written in shell form
instead of exec form. The shell doesn't forward `SIGTERM`, so the graceful
shutdown in `cmd/api/main.go` never runs. The Dockerfile uses the JSON-array
form for exactly this reason.

---

## Cost

Scale-to-zero on the API plus a single always-on solver machine should sit near
Fly's free allowance for a portfolio project's traffic. The solver's
`min_machines_running = 1` is the one line with a standing cost, and it buys
solves that don't time out.

Vercel's hobby tier covers the frontend.
