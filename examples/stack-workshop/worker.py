#!/usr/bin/env python3
"""A portless worker: it polls the API and records a heartbeat.

The process has no listener, so `oberth status` has to answer "is it running"
from the run record rather than from a port. The heartbeat is the outside
evidence the example and the benchmark judge use.
"""

from __future__ import annotations

import json
import os
import signal
import time
from urllib.request import ProxyHandler, build_opener

NAME = os.environ.get("SERVICE_NAME", "worker")
API_URL = os.environ["API_URL"].rstrip("/")
HEARTBEAT = os.environ["HEARTBEAT"]
INTERVAL = float(os.environ.get("INTERVAL", "0.5"))
OPENER = build_opener(ProxyHandler({}))

stopping = False


def stop(_signum: int, _frame: object) -> None:
    global stopping
    stopping = True


def main() -> None:
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    directory = os.path.dirname(HEARTBEAT)
    if directory:
        os.makedirs(directory, exist_ok=True)
    while not stopping:
        ok = False
        try:
            with OPENER.open(f"{API_URL}/health", timeout=2) as response:
                ok = response.status == 200
        except Exception:  # noqa: BLE001 - the heartbeat records the failure
            ok = False
        with open(HEARTBEAT, "a", encoding="utf-8") as handle:
            handle.write(json.dumps({"t": time.time(), "service": NAME, "ok": ok}) + "\n")
        if INTERVAL <= 0:
            break
        time.sleep(INTERVAL)


if __name__ == "__main__":
    main()
