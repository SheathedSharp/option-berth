#!/usr/bin/env python3
"""Native fake-source before/after measurements. No product/daemon is executed.

Archive a specified existing local commit into an owned temporary directory (no
clone/fetch). Only the isolated runtimecheck package runs; dependency TestMain
files are not linked. Timings are scaled synthetic cases, not a product SLA.
"""
import argparse
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", required=True, help="existing local commit")
    args = parser.parse_args()
    repo = Path(__file__).resolve().parents[1]
    baseline_sha = subprocess.check_output(["git", "rev-parse", "--verify", "--end-of-options", args.baseline + "^{commit}"], cwd=repo, text=True).strip()
    # Paths are captured before the per-child HOME override.
    cache = subprocess.check_output(["go", "env", "GOCACHE"], cwd=repo / "engine", text=True).strip()
    gopath = subprocess.check_output(["go", "env", "GOPATH"], cwd=repo / "engine", text=True).strip()
    with tempfile.TemporaryDirectory(prefix="oberth-runtime-measure-") as temporary:
        root = Path(temporary)
        archive = root / "baseline.tar"
        with archive.open("wb") as output:
            subprocess.run(["git", "archive", baseline_sha, "engine"], cwd=repo, stdout=output, check=True)
        baseline = root / "baseline"
        baseline.mkdir()
        with tarfile.open(archive) as source:
            source.extractall(baseline, filter="data")
        target = baseline / "engine/internal/runtimecheck"
        target.mkdir()
        for name in ("measure_test.go", "measure_poll_test.go"):
            # Adapt only shared harness imports when the archive predates the host rename.
            fixture = (repo / "engine/internal/runtimecheck" / name).read_text()
            if (baseline / "engine/internal/daemon").is_dir():
                fixture = fixture.replace("/engine/internal/runtimesvc/", "/engine/internal/daemon/")
            (target / name).write_text(fixture)
        env = os.environ.copy()
        env.update(HOME=str(root / "home"), BERTH_HOME=str(root / "state"), BERTH_ATTN="0",
                   GOPROXY="off", GOTOOLCHAIN="local", GOCACHE=cache, GOPATH=gopath,
                   OBERTH_RUNTIME_MEASURE="1", OBERTH_COMPAT_PATH=str(root / "compat.db"))
        for candidate in (True, False):
            env["OBERTH_COMPAT_WRITE"] = "1" if candidate else "0"
            label = "candidate" if candidate else "baseline"
            print(label, "native fake-source; baseline=" + baseline_sha, flush=True)
            command = ["go", "test", "-v", "-count=1", "-timeout=30s"]
            if candidate:
                command += ["-tags=runtime_events"]
            command += ["./internal/runtimecheck", "-run", "^(TestRuntimeMeasurement|TestAttentionReadMeasurement|TestRuntimeFormatCompatibility)$"]
            subprocess.run(command, cwd=(repo if candidate else baseline) / "engine", env=env, check=True)

        # Reverse direction: prior writer -> candidate read-only history reader.
        env["OBERTH_COMPAT_PATH"] = str(root / "compat-from-baseline.db")
        for candidate in (False, True):
            env["OBERTH_COMPAT_WRITE"] = "0" if candidate else "1"
            print("compatibility baseline writer ->", "candidate reader" if candidate else "baseline fixture", flush=True)
            subprocess.run(["go", "test", "-v", "-count=1", "-timeout=30s", "./internal/runtimecheck", "-run", "^TestRuntimeFormatCompatibility$"],
                           cwd=(repo if candidate else baseline) / "engine", env=env, check=True)


if __name__ == "__main__":
    main()
