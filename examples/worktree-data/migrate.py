#!/usr/bin/env python3
"""Apply this worktree's schema to its own SQLite database.

`oberth up` runs this as the api service's `prepare:`, before the long-lived
process starts. DB_PATH comes from the manifest's ${worktree} reference, so
each worktree migrates a different file. Every statement is idempotent, because
a `prepare:` runs again on restart.
"""
import os
import sqlite3
import sys
from pathlib import Path


def main() -> int:
    raw = os.environ.get("DB_PATH", "").strip()
    if not raw:
        print("DB_PATH is not set; run this through `oberth up`", file=sys.stderr)
        return 2
    path = Path(raw)
    path.parent.mkdir(parents=True, exist_ok=True)
    with sqlite3.connect(path) as db:
        db.execute("CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY)")
        db.execute("CREATE TABLE IF NOT EXISTS notes (id INTEGER PRIMARY KEY, body TEXT NOT NULL)")
        db.execute("INSERT OR IGNORE INTO schema_migrations(version) VALUES (1)")
        db.execute("INSERT OR IGNORE INTO notes(id, body) VALUES (1, ?)", (f"seeded in {path}",))
    print(f"migrated {path}", flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
