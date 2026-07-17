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

# .PHONY tells make these targets are commands, not files it should build.
# Without it, creating a file literally named "test" would break `make test`
# (make would see the file exists and say "nothing to do").
.PHONY: run test up down down-v psql logs

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
