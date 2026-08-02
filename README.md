# Macro-Max

[![CI](https://github.com/BenYang12/Macro-Max/actions/workflows/ci.yml/badge.svg)](https://github.com/BenYang12/Macro-Max/actions/workflows/ci.yml)

A project designed to help college weightlifters hit their protein and nutrition goals at the cheapest price.

Macro-Max computes the **provably cheapest** real grocery basket that hits your
macro targets within a weekly budget, using live per-store prices — with
variety constraints so the answer is actually cookable. The core is a
deterministic mixed-integer program, not an LLM guess.

## Status

**All seven phases complete.** Live Harris Teeter prices, a MILP that returns
whole packs with variety constraints, a web UI, recipe generation, and cart
push. Deployment configs are written but nothing is deployed.

```bash
curl -X POST localhost:4000/v1/solve -d '{"target_id": 1, "integer_packs": true}'
```
```json
{"basket": {"status": "optimal", "total_cost_cents": 3175, "items": [
  {"food_name": "Tuna, canned in water, drained", "packs": 15, "cost_cents": 1500},
  {"food_name": "Black Beans, dried",  "packs": 3, "cost_cents": 507},
  {"food_name": "Lentils, dried",      "packs": 2, "cost_cents": 438},
  {"food_name": "Peanut Butter",       "packs": 2, "cost_cents": 398},
  {"food_name": "Broccoli, raw",       "packs": 1, "cost_cents": 167},
  {"food_name": "Carrots, raw",        "packs": 1, "cost_cents": 100},
  {"food_name": "Bananas",             "packs": 1, "cost_cents":  65}]}}
```

**$31.75/week**, hitting 180g protein / 200g carbs / 60g fat per day at a real
store — three protein sources, two vegetables, a fruit, and whole packs only.

The Phase 3 LP answered the same question with three foods and 2.7kg of lentils:
the [Stigler diet](https://en.wikipedia.org/wiki/Stigler_diet) result from 1945,
mathematically optimal and completely inedible. The integer and variety
constraints added in Phase 4 are the difference between those two baskets.

### What the solver gives you that an LLM cannot

Ask for a basket under $20 and the answer isn't an error — it's a **proof**:

```json
{"error": {"code": "infeasible",
           "message": "no basket meets these macros within the budget",
           "min_feasible_budget_cents": 3175}}
```

"Nothing below $31.75 works" required showing that *every* cheaper combination
fails. A language model can guess a grocery list; it cannot tell you that no
cheaper one exists.

## Stack

| Layer | Choice |
|---|---|
| API | Go, stdlib `net/http` with Go 1.22 method routing |
| Database | PostgreSQL 16 via pgx v5, migrations by golang-migrate |
| Cache | Redis 7, content-addressed solve cache |
| Solver | Python OR-Tools — GLOP (LP) and SCIP (MILP) over gRPC, contract in `proto/solver/v1` |
| Prices | Kroger API (Harris Teeter, chain `HART`) |
| Nutrition | USDA FoodData Central |
| Frontend | Next.js 16 / TypeScript / Tailwind v4 |
| Recipes | Claude (`claude-opus-5`) via the Anthropic Go SDK |
| Deploy | Fly.io ×2 + Vercel — **configs written, not deployed** ([docs/DEPLOY.md](docs/DEPLOY.md)) |

### Where the LLM sits, and why it sits there

Claude writes recipes from a basket the solver already chose. It never picks
foods, quantities, or prices — the moment it did, "provably cheapest" would stop
being true, because nothing about a language model's output is provable.

That boundary is structural, not a convention: `/v1/recipes` is only registered
when `ANTHROPIC_API_KEY` is set, so the whole product works with the LLM
removed. Same rule for the cart routes, which need `TOKEN_ENCRYPTION_KEY`.

## Endpoints

| Route | What it does |
|---|---|
| `GET /v1/healthcheck` | Liveness, including a Postgres ping |
| `GET /v1/foods`, `/v1/foods/{id}` | The nutrition catalog |
| `GET /v1/products`, `/v1/products/{id}` | Per-store pack sizes and prices |
| `GET /v1/stores` | Store lookup, proxied so Kroger credentials never reach a browser |
| `POST /v1/targets`, `GET /v1/targets/{id}` | What the user wants to hit |
| `POST /v1/solve` | **The optimizer.** 422 + a minimum budget when infeasible |
| `POST /v1/recipes` | A week of meals from the solved basket *(needs an Anthropic key)* |
| `GET /v1/kroger/authorize` → `/v1/kroger/callback` | OAuth authorization-code flow for cart access |
| `POST /v1/kroger/cart` | Push the basket into a real Kroger cart *(not idempotent — see below)* |

**The cart write is additive and cannot be undone.** Kroger's API offers no way
to read a cart back or remove from it, so calling it twice doubles the
quantities. The response says so and the UI disables the button after a success.

## Getting started

Requires Go (see `go.mod`), Docker, and `golang-migrate`
(`brew install golang-migrate`).

```bash
make up          # start Postgres + Redis
make migrate-up  # create the schema
make seed        # load ~42 dev foods and their fake 'SEED' products
make run         # start the API on :4000
```

```bash
curl localhost:4000/v1/healthcheck
curl 'localhost:4000/v1/foods?category=protein'
curl 'localhost:4000/v1/products?store_id=SEED'
```

## Development

```bash
make test          # Go unit tests (integration tests self-skip without a database)
make test-int      # Go unit + integration tests against the compose Postgres
make lint          # golangci-lint, the same config CI runs
make solver-test   # pytest against the pure LP — no gRPC, no database
make proto         # regenerate Go + Python stubs from the .proto contract
make web           # Next dev server on :3000, proxying /api/* to :4000
make docker-build  # build the API image exactly as a deploy would
make docker-verify # prove the image is non-root and contains no .env
```

The solver runs in Compose (`make solver-up`); the Go API runs on the host for a
fast edit loop. `make test-int` includes an end-to-end test that drives
Postgres → Go → gRPC → Python → basket, and self-skips if the solver isn't up.

## Conventions

- **Money is integer cents** everywhere — Go `int64`, Postgres `INT`. Never floats.
- **Mass is grams.** Nutrition is stored per-100g (matching USDA) and converted
  to per-gram exactly once, at the Go→solver boundary.
- **Targets are entered daily, solved weekly.** The `_daily` / `_weekly` column
  suffixes make the two impossible to confuse.
- API responses are enveloped: `{"foods": [...]}` on success,
  `{"error": {"code", "message"}}` on failure.
