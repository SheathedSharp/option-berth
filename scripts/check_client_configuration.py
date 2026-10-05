#!/usr/bin/env python3
"""Check client JSON Schemas against the real Swift parser in a disposable venv.

Requires Python with venv, Swift, and access to the test dependency index.
No dependencies are installed globally or bundled into the application. This
entry point is shared by PR CI and release verification; failure is not skipped.
"""
from __future__ import annotations
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parent.parent


def main() -> int:
    try:
        with tempfile.TemporaryDirectory(prefix="oberth-schema-") as tmp:
            environment = Path(tmp) / "venv"
            binary = Path(tmp) / "schema-checks"
            python = environment / ("Scripts/python.exe" if os.name == "nt" else "bin/python")
            commands = [
                [sys.executable, "-m", "venv", str(environment)],
                [str(python), "-m", "pip", "install", "--disable-pip-version-check",
                 "--only-binary=:all:", "-r", "client/macos/Configuration/requirements-test.txt"],
                ["swiftc", "-swift-version", "5", "client/macos/Sources/ClientConfiguration.swift",
                 "client/macos/Tests/ClientSchemaChecks.swift", "-o", str(binary)],
                [str(python), "client/macos/Configuration/verify_schemas.py", str(binary)],
            ]
            for command in commands:
                subprocess.run(command, cwd=ROOT, check=True, timeout=180)
        return 0
    except (OSError, subprocess.SubprocessError) as error:
        print(f"Client configuration schema gate failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
