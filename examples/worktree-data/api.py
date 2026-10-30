#!/usr/bin/env python3
"""Serve a health fact read from this worktree's own SQLite database."""
import json
import os
import sqlite3
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

DB_PATH = Path(os.environ["DB_PATH"]).resolve()
PORT = int(os.environ["PORT"])


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/health":
            self.send_error(404)
            return
        with sqlite3.connect(DB_PATH) as db:
            migrations = db.execute("SELECT COUNT(*) FROM schema_migrations").fetchone()[0]
            notes = db.execute("SELECT COUNT(*) FROM notes").fetchone()[0]
        body = json.dumps(
            {"ok": True, "db": str(DB_PATH), "migrations": migrations, "notes": notes}
        ).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_args):
        pass


if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
