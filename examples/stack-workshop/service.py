#!/usr/bin/env python3
"""A small HTTP service used by the stack example.

It can warm up before binding (READY_DELAY) and can keep /health at 503 until
the upstream services named in REQUIRE answer. Both are real behaviors a large
stack has: a JVM takes time to listen, and an API is not healthy just because
its own socket is open.
"""

from __future__ import annotations

import json
import os
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.request import ProxyHandler, build_opener

NAME = os.environ.get("SERVICE_NAME", "service")
PORT = int(os.environ["PORT"])
READY_DELAY = float(os.environ.get("READY_DELAY", "0"))
VERSION_FILE = os.environ.get("VERSION_FILE", "variant.txt")
REQUIRED_FILE = os.environ.get("REQUIRED_FILE", "")
REQUIRE = [item.strip() for item in os.environ.get("REQUIRE", "").split(",") if item.strip()]
OPENER = build_opener(ProxyHandler({}))


def read_version() -> str:
    try:
        with open(VERSION_FILE, encoding="utf-8") as handle:
            return handle.read().strip() or "v1"
    except OSError:
        return "v1"


VERSION = read_version()


def waiting_on() -> list[str]:
    waiting = []
    for url in REQUIRE:
        try:
            with OPENER.open(url, timeout=1.5) as response:
                if response.status != 200:
                    waiting.append(url)
        except Exception:  # noqa: BLE001 - a dependency probe is evidence, not control flow
            waiting.append(url)
    return waiting


class Handler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler's spelling
        path = self.path.split("?")[0]
        waiting = waiting_on()
        if path == "/health" and waiting:
            code = 503
            body = {"ok": False, "service": NAME, "waiting_on": waiting}
        elif path in ("/", "/health", "/version"):
            code = 200
            body = {"ok": True, "service": NAME, "version": VERSION, "pid": os.getpid()}
        else:
            code = 404
            body = {"ok": False, "service": NAME, "path": path}
        data = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, fmt: str, *args: object) -> None:
        print(fmt % args, flush=True)


def main() -> None:
    if REQUIRED_FILE and not os.path.exists(REQUIRED_FILE):
        print(f"{NAME}: missing {REQUIRED_FILE}; run the documented setup step", file=sys.stderr, flush=True)
        sys.exit(1)
    if READY_DELAY > 0:
        print(f"{NAME}: warming up for {READY_DELAY:g}s", flush=True)
        time.sleep(READY_DELAY)
    server = ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
    print(f"{NAME}: listening on {PORT} version={VERSION}", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
