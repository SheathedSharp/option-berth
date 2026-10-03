#!/usr/bin/env python3
"""Run core Go benchmarks repeatedly and summarize aggregate ns/op samples.

The Go benchmark harness reports one aggregate for each benchmark run. Repeating
the benchmark measures run-to-run variation of aggregate costs. These are not
individual request latencies and must not be reported as a request p95.
"""

from __future__ import annotations

import argparse
import statistics
import pathlib
import re
import subprocess
import sys


LINE = re.compile(r"^(Benchmark\S+)\s+\d+\s+([0-9.]+)\s+ns/op(?:\s|$)")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--count", type=int, default=7, help="benchmark repetitions (each ns/op is an aggregate)")
    args = parser.parse_args()
    if args.count < 3:
        parser.error("--count must be at least 3")

    root = pathlib.Path(__file__).resolve().parents[1]
    command = [
        "go",
        "-C",
        "engine",
        "test",
        "-run",
        "^$",
        "-bench",
        "Benchmark(IndexMatchPort|Resolve(?:SharedCwd)?|GroupsWith|Diff|Snapshot)$",
        "-benchmem",
        "-count",
        str(args.count),
        "./internal/groups",
        "./internal/state",
        "./internal/scanner",
    ]
    proc = subprocess.run(command, cwd=root, text=True, capture_output=True)
    sys.stdout.write(proc.stdout)
    sys.stderr.write(proc.stderr)
    if proc.returncode:
        return proc.returncode

    samples: dict[str, list[float]] = {}
    for line in proc.stdout.splitlines():
        match = LINE.match(line)
        if match:
            samples.setdefault(match.group(1), []).append(float(match.group(2)))
    if not samples:
        print("benchmark-core: no benchmark samples found", file=sys.stderr)
        return 1

    print("\nAggregate ns/op across repetitions (not per-request latency percentiles)")
    for name in sorted(samples):
        values = sorted(samples[name])
        print(f"{name}\tn={len(values)}\tmin={values[0]:g}"
              f"\tmedian={statistics.median(values):g}\tmax={values[-1]:g}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
