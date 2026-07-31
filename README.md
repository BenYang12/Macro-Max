# Macro-Max

[![CI](https://github.com/BenYang12/Macro-Max/actions/workflows/ci.yml/badge.svg)](https://github.com/BenYang12/Macro-Max/actions/workflows/ci.yml)

A project designed to help college weightlifters hit their protein and nutrition goals at the cheapest price.

Macro-Max computes the **provably cheapest** real grocery basket that hits your
macro targets within a weekly budget, using live per-store prices — with
variety constraints so the answer is actually cookable. The core is a
deterministic mixed-integer program, not an LLM guess.

## Status

Phases 1–3 complete. The API solves for a real basket end to end.

```bash
curl -X POST localhost:4000/v1/solve -d '{"target_id": 1}'
```
```json
{"basket": {"status": "optimal", "total_cost_cents": 2619, "items": [
  {"food_name": "Lentils, dried", "grams": 2739, "cost_cents": 1202},
  {"food_name": "Whey Protein Isolate, powder", "grams": 499, "cost_cents": 991},
  {"food_name": "Peanut Butter", "grams": 744, "cost_cents": 426}]}}
```

That basket is **deliberately terrible**, and it is the point of Phase 3. A pure
cost-minimizing LP finds the cheapest source of each macro and buys nothing
else — the [Stigler diet](https://en.wikipedia.org/wiki/Stigler_diet) result
from 1945. Phase 4 adds the variety and integer-pack constraints that turn a
mathematically optimal answer into a humanly edible one.

## Stack

| Layer | Choice |
|---|---|
| API | Go, stdlib `net/http` with Go 1.22 method routing |
| Database | PostgreSQL 16 via pgx v5, migrations by golang-migrate |
| Cache | Redis 7 |
| Solver | Python OR-Tools (GLOP) over gRPC, contract in `proto/solver/v1` |
| Frontend | Next.js / TypeScript / Tailwind (Phase 6) |

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
make test         # Go unit tests (integration tests self-skip without a database)
make test-int     # Go unit + integration tests against the compose Postgres
make solver-test  # pytest against the pure LP — no gRPC, no database
make proto        # regenerate Go + Python stubs from the .proto contract
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
