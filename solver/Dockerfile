# Container for the Python solver.
#
# I only containerize the SOLVER, not the Go API. My Go binary runs on the host
# via `make run` for a fast edit-rebuild loop, but the solver has a heavy native
# dependency (OR-Tools ships compiled C++), and I'd rather pin its environment
# once in an image than fight Python versions on every machine.

FROM python:3.12-slim

# Where uv puts things. Setting these explicitly rather than relying on defaults
# so the COPY paths below can't drift.
ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1

WORKDIR /app

# Same reason as pythonpath in pyproject.toml: protoc emits imports relative to
# the proto root ("from solver.v1 import ..."), so gen/ has to be importable as
# a root, not just as a subpackage.
ENV PYTHONPATH=/app:/app/gen

# Install uv itself, from its official image, by copying the single static
# binary. Faster and smaller than pip-installing it.
COPY --from=ghcr.io/astral-sh/uv:latest /uv /usr/local/bin/uv

# Dependency layer FIRST, source SECOND. Docker caches each layer, so as long as
# pyproject.toml is unchanged, editing lp.py reuses the cached dependency
# install instead of redownloading OR-Tools (which is ~100MB). Getting this
# order wrong makes every rebuild painfully slow.
COPY pyproject.toml ./
RUN uv pip install --system --no-cache \
    "ortools>=9.11" "grpcio>=1.67" "protobuf>=5.29"

COPY gen/ ./gen/
COPY lp.py server.py ./

EXPOSE 50051

# No shell form: exec form means the Python process is PID 1 and receives
# SIGTERM directly, so the graceful-shutdown handler in server.py actually runs.
# The shell form would wrap it in /bin/sh, which swallows signals and forces
# Docker to SIGKILL after its timeout.
CMD ["python", "server.py"]
