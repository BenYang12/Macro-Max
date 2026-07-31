"""gRPC server wrapping the pure solver.

This file is deliberately thin. Everything it does is TRANSPORT: accept a
connection, hand the request to lp.solve(), send the answer back. All the
optimization lives in lp.py, which knows nothing about gRPC.

That split is the same one I made in Go between internal/fdc (pure normalize)
and its HTTP client, and for the same reason: the hard logic should be testable
without starting a server. My whole pytest suite calls solve() directly and
never binds a port. If I'd put the model-building inside the servicer method,
every test would need a running server and I'd have written far fewer of them.
"""

import logging
import os
import signal
import sys
import time
from concurrent import futures

import grpc

import lp
from solver.v1 import solver_pb2, solver_pb2_grpc

# gRPC's default listen port convention. I keep it configurable because in
# Docker Compose I bind inside a container, and on Fly.io (Phase 7) this service
# will only be reachable over the private network.
DEFAULT_PORT = 50051

# Thread pool size. gRPC Python hands each RPC to a worker thread. Solves are
# CPU-bound inside OR-Tools' C++ core, which releases the GIL, so real
# parallelism is possible here — but I keep the pool small because a dozen
# simultaneous MILP solves would thrash the CPU rather than finish faster.
MAX_WORKERS = 4

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(message)s",
)
log = logging.getLogger("solver")


class SolverService(solver_pb2_grpc.SolverServiceServicer):
    """Implements the SolverService defined in solver.proto.

    The base class is GENERATED. That's the payoff of the proto contract: if I
    misspell a method name, or my signature stops matching the contract, this
    class simply stops overriding the base and gRPC returns UNIMPLEMENTED — a
    loud failure rather than a silent one.
    """

    def Solve(self, request, context):
        started = time.monotonic()
        log.info(
            "solve: %d foods, budget %d cents, targets P%.0f/C%.0f/F%.0f",
            len(request.foods),
            request.budget_cents,
            request.targets.protein_g,
            request.targets.carbs_g,
            request.targets.fat_g,
        )

        try:
            response = lp.solve(request)
        except Exception as exc:  # noqa: BLE001 - deliberate catch-all, see below
            # A bare except is usually bad practice, but this is a SERVER
            # boundary. If lp.solve() raises something I didn't anticipate, the
            # alternatives are: crash the worker thread and return a generic
            # gRPC UNKNOWN with no context, or catch it here and return a
            # structured ERROR my Go client already knows how to handle. The
            # second is strictly better for debugging, and the exception is
            # still logged with a full traceback.
            log.exception("solve failed")
            return solver_pb2.SolveResponse(
                status=solver_pb2.SOLVE_STATUS_ERROR,
                message=f"solver raised {type(exc).__name__}: {exc}",
                solve_seconds=time.monotonic() - started,
            )

        log.info(
            "  -> %s, %d items, %d cents, %.3fs",
            solver_pb2.SolveStatus.Name(response.status),
            len(response.items),
            response.total_cost_cents,
            response.solve_seconds,
        )
        return response


def serve():
    port = int(os.getenv("SOLVER_PORT", DEFAULT_PORT))

    server = grpc.server(futures.ThreadPoolExecutor(max_workers=MAX_WORKERS))
    solver_pb2_grpc.add_SolverServiceServicer_to_server(SolverService(), server)

    # 0.0.0.0 rather than localhost: inside a container, binding to localhost
    # would make the service unreachable from outside the container. This is a
    # classic Docker gotcha and I'd rather hit it here in a comment than at
    # 2am wondering why my Go client can't connect.
    server.add_insecure_port(f"0.0.0.0:{port}")

    # "Insecure" means no TLS. That's a deliberate, bounded decision: this
    # service is never exposed publicly. Locally it's on the Compose network;
    # in production (Phase 7) it's on Fly's private IPv6 network with no public
    # address at all. Adding TLS between two processes inside a private network
    # would be ceremony without a threat model.

    server.start()
    log.info("solver listening on 0.0.0.0:%d", port)

    # Graceful shutdown, mirroring what I did in cmd/api/main.go. Docker sends
    # SIGTERM when stopping a container; without handling it, an in-flight solve
    # gets killed mid-request and the client sees a connection reset instead of
    # an answer.
    def shutdown(signum, _frame):
        log.info("received signal %d, draining", signum)
        # The argument is a grace period in seconds: stop accepting new RPCs,
        # let in-flight ones finish, then close.
        server.stop(grace=10).wait()
        log.info("solver stopped cleanly")
        sys.exit(0)

    signal.signal(signal.SIGTERM, shutdown)
    signal.signal(signal.SIGINT, shutdown)

    server.wait_for_termination()


if __name__ == "__main__":
    serve()
