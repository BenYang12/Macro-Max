"""Start the solver and API together on Render's single free web service."""

from __future__ import annotations

import os
import signal
import socket
import subprocess
import sys
import time


children: list[subprocess.Popen[bytes]] = []


def run_once(command: list[str]) -> None:
    completed = subprocess.run(command, check=False)
    if completed.returncode != 0:
        raise SystemExit(completed.returncode)


def stop_children(signum: int, _frame: object) -> None:
    for child in children:
        if child.poll() is None:
            child.send_signal(signum)


def wait_for_solver(timeout: float = 30) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            with socket.create_connection(("127.0.0.1", 50051), timeout=1):
                return
        except OSError:
            time.sleep(0.25)
    raise SystemExit("solver did not become ready within 30 seconds")


def main() -> None:
    database_url = os.environ.get("DATABASE_URL")
    if not database_url:
        raise SystemExit("DATABASE_URL is required")

    run_once(["migrate", "-path", "/migrations", "-database", database_url, "up"])
    run_once(["seed"])

    for sig in (signal.SIGTERM, signal.SIGINT):
        signal.signal(sig, stop_children)

    solver = subprocess.Popen([sys.executable, "/app/solver/server.py"])
    children.append(solver)
    wait_for_solver()

    api = subprocess.Popen(["api"])
    children.append(api)

    while True:
        for child in children:
            code = child.poll()
            if code is not None:
                stop_children(signal.SIGTERM, None)
                for remaining in children:
                    if remaining.poll() is None:
                        remaining.wait(timeout=10)
                raise SystemExit(code)
        time.sleep(0.5)


if __name__ == "__main__":
    main()
