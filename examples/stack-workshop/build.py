#!/usr/bin/env python3
"""Build the web assets the way a Node frontend would.

The output is gitignored, so a checkout that only pulls code cannot serve the
frontend until this prepare step runs. The version stamp combines the source
digest with `variant.txt`, so either input changing makes the running output
stale.
"""

from __future__ import annotations

import hashlib
import os

SOURCE = "src/app.html"
VARIANT = "variant.txt"
DIST = os.environ.get("DIST", "dist")


def main() -> None:
    with open(SOURCE, encoding="utf-8") as handle:
        source = handle.read()
    try:
        with open(VARIANT, encoding="utf-8") as handle:
            variant = handle.read().strip() or "v1"
    except OSError:
        variant = "v1"
    digest = hashlib.sha256(source.encode("utf-8")).hexdigest()[:8]
    version = f"{variant}-{digest}"
    os.makedirs(DIST, exist_ok=True)
    with open(os.path.join(DIST, "index.html"), "w", encoding="utf-8") as handle:
        handle.write(source.replace("{{VERSION}}", version))
    with open(os.path.join(DIST, "version.txt"), "w", encoding="utf-8") as handle:
        handle.write(version + "\n")
    print(f"built {DIST}/version.txt version={version}")


if __name__ == "__main__":
    main()
