# Makefile — command bookmarks for the project.
#
# `make <target>` runs the indented commands under that target.
# RULE: recipe lines are indented with a real TAB, never spaces —
# spaces produce the famously unhelpful error "*** missing separator".

# Pull in .env if it exists (-include = don't error when it doesn't), then
# `export` pushes those variables into the environment of every command make
# runs. This is why the Go code never needs a dotenv library: `make run`
# already has PORT, DATABASE_URL, etc. set.
-include .env
export

# ?= assigns only if the variable isn't already set — so a .env file (or the
# shell environment) always wins over this default. Must match the default in
# internal/config/config.go: one source of truth per environment, two places
# that agree on the fallback.
DATABASE_URL ?= postgres://macrocart:macrocart@localhost:5432/macrocart?sslmode=disable

# Same ?= fallback rule, so `make kroger-stores` works before .env defines a
# zip. Must agree with the default in internal/config/config.go.
KROGER_ZIP ?= 27514
KROGER_LOCATION_ID ?= 09700117

# .PHONY tells make these targets are commands, not files it should build.
# Without it, creating a file literally named "test" would break `make test`
# (make would see the file exists and say "nothing to do").
.PHONY: run test test-int seed up down down-v psql logs migrate-new migrate-up migrate-down fdc-suggest fdc-import fdc-dry proto solver-test solver-up solver-logs solver-shell kroger-stores kroger-dry kroger-ingest web web-install web-build

## Development loop

run:            # start the API on the host (fast restarts)
	go run ./cmd/api

test:           # run every Go test in the module — UNIT tests only in practice,
                # because integration tests self-skip when TEST_DATABASE_URL is
                # unset. This is the target that must stay green on any laptop
                # with no database running.
	go test ./...

test-int:       # unit AND integration tests, against the compose Postgres.
                # Integration tests self-skip unless TEST_DATABASE_URL is set,
                # so this target's whole job is to SET it. It reuses
                # DATABASE_URL because there's one local database; CI will
                # point it at its own throwaway service container instead.
                #
                # -count=1 is the documented way to DISABLE Go's test result
                # cache. Go caches a passing result and reprints it without
                # re-running when the code is unchanged — but these tests also
                # depend on DATABASE STATE, which Go can't see. A cached "ok"
                # after the data changed would be a lie.
	TEST_DATABASE_URL="$(DATABASE_URL)" go test -count=1 ./...

seed:           # load the ~42-food dev catalog into Postgres.
                # Safe to re-run: the seeder upserts, so running it twice
                # leaves the same rows rather than duplicating or erroring.
                # Run it AFTER migrate-up — it writes rows, it doesn't create
                # tables.
	go run ./cmd/seed

## Infrastructure (Docker Compose)

up:             # start Postgres + Redis in the background
	docker compose up -d

down:           # stop them (data survives in the pgdata volume)
	docker compose down

down-v:         # stop AND wipe data volumes — the "fresh start" button
	docker compose down -v

psql:           # open an interactive SQL shell inside the Postgres container
	docker compose exec postgres psql -U macrocart -d macrocart

logs:           # tail all service logs
	docker compose logs -f

## Migrations (golang-migrate CLI)
## The first time migrate up touches my database, it creates schema-migrations -> one row: version (highest migration applied) and dirty (did one fail halfway?)
migrate-new:    # creates a new pair of empty migration files. Files include .sql, they are put in migrations/, use sequential numbering. One is what to do, and one is how to undo it. Every migration comes in an up/down pair so I can roll changes forward and backward
	migrate create -ext sql -dir migrations -seq $(name) 

## "make migrate-up" -> make finds the migrate-up target and runs the indented command below it
migrate-up:     # apply everything not applied yet
	migrate -path migrations -database "$(DATABASE_URL)" up 
	
migrate-down:   # undo exactly ONE migration (the most recent) — deliberate, not "down all"
	migrate -path migrations -database "$(DATABASE_URL)" down 1

## USDA FoodData Central import (Phase 2)
## Needs FDC_API_KEY in .env — get one free at
## https://fdc.nal.usda.gov/api-key-signup.html

fdc-suggest:    # search FDC for every seeded food, print paste-ready mapping entries
	go run ./cmd/fdcimport -suggest

fdc-import:     # apply the curated mapping in cmd/fdcimport/mapping.go
	go run ./cmd/fdcimport -all

fdc-dry:        # same as fdc-import but writes nothing — always run this first
	go run ./cmd/fdcimport -all -dry-run

## Solver (Phase 3) — Python OR-Tools over gRPC

proto:          # regenerate Go AND Python stubs from proto/solver/v1/solver.proto.
                # Both languages come out of ONE command, which is the whole
                # reason I'm using buf: they can never drift because I forgot to
                # rerun one of them. Generated code IS committed, so a fresh
                # clone builds without needing buf installed.
	buf lint
	PATH="$$PATH:$$(go env GOPATH)/bin" buf generate

solver-test:    # pytest against the pure LP — no gRPC, no database, fast
	cd solver && uv run pytest -q

solver-up:      # build and start just the solver container
	docker compose up -d --build solver

solver-logs:    # tail the solver's logs (every solve logs its status and timing)
	docker compose logs -f solver

solver-shell:   # a shell inside the solver container, for poking at imports
	docker compose exec solver /bin/sh

## Kroger price ingestion (Phase 5)
## Needs KROGER_CLIENT_ID and KROGER_CLIENT_SECRET in .env.

kroger-stores:  # find stores near KROGER_ZIP and print their locationIds.
                # Read-only — makes one API call and writes nothing. Run this
                # first, then paste the locationId into .env.
	go run ./cmd/krogeringest -zip "$(KROGER_ZIP)"

kroger-dry:     # fetch and parse everything, print what WOULD be written.
                # Always run this before a real ingest: it shows which products
                # matched, which were skipped, and why.
                # Fails fast with a clear message if the locationId isn't set,
                # rather than sending an empty one to Kroger and getting a
                # confusing 400 back.
	@test -n "$(KROGER_LOCATION_ID)" || \
		(echo "KROGER_LOCATION_ID is not set. Run 'make kroger-stores' and put a locationId in .env"; exit 1)
	go run ./cmd/krogeringest -location "$(KROGER_LOCATION_ID)" -dry-run

kroger-ingest:  # the real thing: upsert products, append price history on
                # change, mark vanished SKUs unavailable.
	@test -n "$(KROGER_LOCATION_ID)" || \
		(echo "KROGER_LOCATION_ID is not set. Run 'make kroger-stores' and put a locationId in .env"; exit 1)
	go run ./cmd/krogeringest -location "$(KROGER_LOCATION_ID)"

## Frontend (Phase 6) — Next.js on :3000
## Needs `make run` in another terminal: the web app proxies /api/* to :4000.

web-install:    # first-time setup (or after pulling package.json changes)
	cd web && npm install

web:            # start the Next dev server with hot reload
	cd web && npm run dev

web-build:      # production build + typecheck, the same thing CI would run
	cd web && npm run build
