# Macro-Max

[![CI](https://github.com/BenYang12/Macro-Max/actions/workflows/ci.yml/badge.svg)](https://github.com/BenYang12/Macro-Max/actions/workflows/ci.yml)

A project designed to help college weightlifters hit their protein and nutrition goals at the cheapest price.

Macro-Max computes the **provably cheapest** real grocery basket that hits your
macro targets within a weekly budget, using live per-store prices — with
variety constraints so the answer is actually cookable. The core is a
deterministic mixed-integer program, not an LLM guess.

## Status

Phase 1 (skeleton + schema) — in progress.

## Stack

| Layer | Choice |
|---|---|
| API | Go, stdlib `net/http` with Go 1.22 method routing |
| Database | PostgreSQL 16 via pgx v5, migrations by golang-migrate |
| Cache | Redis 7 |
| Solver | Python OR-Tools over gRPC (Phase 3+) |
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
make test      # unit tests (integration tests self-skip without a database)
make test-int  # unit + integration tests against the compose Postgres
```

## Conventions

- **Money is integer cents** everywhere — Go `int64`, Postgres `INT`. Never floats.
- **Mass is grams.** Nutrition is stored per-100g (matching USDA) and converted
  to per-gram exactly once, at the Go→solver boundary.
- **Targets are entered daily, solved weekly.** The `_daily` / `_weekly` column
  suffixes make the two impossible to confuse.
- API responses are enveloped: `{"foods": [...]}` on success,
  `{"error": {"code", "message"}}` on failure.
