#!/usr/bin/env python3
"""Apply the schema to this worktree's database.

The migration is idempotent: running it on every `up` and `restart` leaves one
schema and one seed row. `${worktree}` in the manifest keeps two checkouts from
sharing the same file.
"""

from __future__ import annotations

import os
import sqlite3
from pathlib import Path

DB_PATH = os.environ["DB_PATH"]


def main() -> None:
    path = Path(DB_PATH)
    path.parent.mkdir(parents=True, exist_ok=True)
    schema = Path("schema.sql").read_text(encoding="utf-8")
    connection = sqlite3.connect(path)
    try:
        connection.executescript(schema)
        count = connection.execute("SELECT COUNT(*) FROM notes").fetchone()[0]
        if count == 0:
            connection.execute("INSERT INTO notes (body) VALUES ('seed')")
            connection.commit()
            count = 1
    finally:
        connection.close()
    print(f"migrated {path} notes={count}")


if __name__ == "__main__":
    main()
