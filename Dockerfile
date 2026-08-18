# Dockerfile for the Go API (Phase 7 deployment).
#
# The solver already has its own Dockerfile in solver/. This one containerizes
# the Go half so both services deploy the same way.
#
# MULTI-STAGE BUILD, and it's worth understanding why rather than copying it:
# the Go toolchain is ~800MB and the compiled binary is ~20MB. A single-stage
# image ships the compiler, the module cache, and the source to production —
# all of which are useless there and all of which are attack surface. Two
# stages let me build in a fat image and COPY only the binary into a tiny one.

# ---------------------------------------------------------------- build stage
#
# `AS builder` names this stage so the second one can copy from it.
# The Go version is pinned to match go.mod. "latest" would mean a toolchain
# upgrade could break my build on a commit that changed nothing.
FROM golang:1.26.6-alpine AS builder

WORKDIR /src

# COPY THE MANIFESTS FIRST, then download, THEN copy the source.
#
# This ordering is the single most valuable trick in Docker builds. Each
# instruction is a cached layer, invalidated when its inputs change. Source
# files change constantly; go.mod changes rarely. Downloading dependencies
# before copying the source means an ordinary code edit reuses the cached
# dependency layer instead of re-downloading everything — minutes saved on
# every build. Copy the source first and that cache is invalidated every time.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 produces a STATICALLY LINKED binary — no libc dependency at
# runtime. That's what lets the final stage be alpine (or even scratch) without
# "no such file or directory" errors, which is the confusing way a dynamically
# linked binary fails when its loader is missing.
#
# -ldflags="-s -w" strips the symbol table and DWARF debug info, cutting a few
# MB. The tradeoff is unreadable stack traces from a core dump; Go panics still
# print full traces, so for a service like this it's nearly free.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/api ./cmd/api

# Build the seeder and the ingester too. They're separate binaries and belong
# in the image because both are operational tasks I'll want to run against
# production — seeding a fresh database, refreshing prices — and shelling into
# a container that has them beats deploying a second image to run one command.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/seed ./cmd/seed
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/krogeringest ./cmd/krogeringest

# The golang-migrate CLI, so the deployed container can run its own migrations.
#
# Shipping the migration FILES without the tool that applies them would be a
# half-measure: the documented deploy step is `fly ssh console` then `migrate
# ... up`, and that only works if the binary is here. Same version I run
# locally and in CI, so all three environments apply schema identically.
#
# The build tags are required, not optional. golang-migrate compiles database
# drivers in conditionally; without `postgres` the binary builds fine and then
# fails at runtime with "unknown driver postgres", which reads like a DSN
# problem and isn't one.
RUN CGO_ENABLED=0 GOOS=linux go install -tags 'postgres,file' \
      github.com/golang-migrate/migrate/v4/cmd/migrate@v4.19.0 \
    && cp "$(go env GOPATH)/bin/migrate" /out/migrate

# --------------------------------------------------------------- runtime stage
#
# alpine rather than scratch, for exactly one reason worth the 7MB: TLS. Calling
# the Kroger and Anthropic APIs requires the root CA bundle to verify their
# certificates, and scratch has no files at all. On scratch this fails at
# runtime with "x509: certificate signed by unknown authority" — an error that
# looks like a certificate problem and is actually a missing-file problem.
FROM alpine:3.21

# ca-certificates for TLS; tzdata so time.Time renders in real zones rather
# than defaulting everything to UTC.
RUN apk add --no-cache ca-certificates tzdata

# RUN AS A NON-ROOT USER. Containers run as root by default, which means a
# container escape starts with root on the host. This costs one line and
# removes an entire class of privilege escalation.
RUN adduser -D -u 10001 macrocart

COPY --from=builder /out/api /usr/local/bin/api
COPY --from=builder /out/seed /usr/local/bin/seed
COPY --from=builder /out/krogeringest /usr/local/bin/krogeringest
COPY --from=builder /out/migrate /usr/local/bin/migrate

# Migrations travel WITH the image so the deployed version can always run the
# schema it expects. Deploying code and migrations separately is how you get a
# binary querying a column that doesn't exist yet.
COPY --chown=macrocart:macrocart migrations /migrations

USER macrocart

# Documentation, not configuration: EXPOSE doesn't publish anything, it records
# the intent for humans and for tools that read it. The actual port comes from
# PORT, which internal/config defaults to 4000.
EXPOSE 4000

# The exec form (a JSON array), NOT the shell form. The shell form wraps the
# process in /bin/sh, which does not forward signals — so SIGTERM would reach
# the shell instead of my binary, the graceful shutdown in cmd/api/main.go would
# never run, and every deploy would end in a hard kill after the grace period.
CMD ["/usr/local/bin/api"]
