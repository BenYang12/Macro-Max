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

# .PHONY tells make these targets are commands, not files it should build.
# Without it, creating a file literally named "test" would break `make test`
# (make would see the file exists and say "nothing to do").
.PHONY: run test up down down-v psql logs migrate-new migrate-up migrate-down

## Development loop

run:            # start the API on the host (fast restarts)
	go run ./cmd/api

test:           # run every Go test in the module
	go test ./...

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
