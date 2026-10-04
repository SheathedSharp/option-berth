#!/usr/bin/env python3
"""Plan or explicitly publish a protocol.feature.fix release from reviewed main."""
from __future__ import annotations

import argparse
import contextlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
from typing import Callable

ROOT = Path(__file__).resolve().parent.parent
VERSION_RE = re.compile(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\Z")
PROTOCOL_FILE = Path("engine/internal/daemon/rpc/types.go")
SCHEMA_FILE = Path("engine/docs/schema/protocol.schema.json")
PROTOCOL_RE = re.compile(r'(?m)^const ProtocolVersion = "([0-9]+\.[0-9]+\.[0-9]+)"$')
ACCEPTED_REMOTES = {
    "https://github.com/SheathedSharp/option-berth.git",
    "https://github.com/SheathedSharp/option-berth",
    "git@github.com:SheathedSharp/option-berth.git",
}


def run(root: Path, *args: str, capture: bool = True, environment: dict[str, str] | None = None) -> str:
    env = dict(os.environ, GOMAXPROCS="4")
    if environment:
        env.update(environment)
    result = subprocess.run(args, cwd=root, env=env, text=True,
                            stdout=subprocess.PIPE if capture else None,
                            stderr=subprocess.PIPE if capture else None, check=False)
    if result.returncode:
        # Do not print command arguments: a future caller might pass a credential.
        raise RuntimeError(f"{args[0]} failed (exit {result.returncode}): " + (result.stderr or "see verification output"))
    return (result.stdout or "").strip()


def bump(version: str, kind: str) -> str:
    if not VERSION_RE.fullmatch(version):
        raise ValueError("version must be three nonnegative integers without leading zeros")
    index = {"protocol": 0, "feature": 1, "fix": 2}.get(kind)
    if index is None:
        raise ValueError("release kind must be protocol, feature or fix")
    values = list(map(int, version.split(".")))
    values[index] += 1
    values[index + 1:] = [0] * (2 - index)
    return ".".join(map(str, values))


def protocol(root: Path) -> str:
    matches = PROTOCOL_RE.findall((root / PROTOCOL_FILE).read_text())
    if len(matches) != 1:
        raise ValueError("expected one daemon protocol version declaration")
    return matches[0]


def plan(root: Path, kind: str) -> dict[str, str]:
    current = (root / "VERSION").read_text().strip()
    current_protocol = protocol(root)
    next_version = bump(current, kind)
    return {"kind": kind, "from": current, "version": next_version, "tag": "v" + next_version,
            "protocol_from": current_protocol,
            "protocol": bump(current_protocol, "protocol") if kind == "protocol" else current_protocol}


def check_versions(root: Path) -> None:
    if not VERSION_RE.fullmatch((root / "VERSION").read_text().strip()):
        raise ValueError("invalid VERSION")
    if json.loads((root / SCHEMA_FILE).read_text())["protocol_version"] != protocol(root):
        raise ValueError("generated schema does not match daemon protocol version")


def verify(root: Path) -> None:
    """No skip-checks mode is exposed by the release command."""
    check_versions(root)
    engine = root / "engine"
    for args in [("go", "build", "./..."), ("go", "vet", "./..."),
                 ("go", "test", "-count=1", "-timeout=8m", "./..."),
                 ("go", "test", "-race", "-count=1", "-timeout=10m", "./internal/claims", "./internal/runs", "./internal/spawn", "./internal/daemon/...", "./internal/cmd", "./internal/doctor", "./internal/groups", "./internal/store"),
                 ("go", "test", "-tags", "integration", "-count=1", "-timeout=8m", "./internal/scenario/...")]:
        run(engine, *args, capture=False)
    run(root, "go", "test", "-count=1", "magefiles/build.go", "magefiles/workflow.go", "magefiles/workflow_test.go", capture=False)
    run(root, sys.executable, "-m", "unittest", "discover", "-s", "scripts", "-p", "*_test.py", capture=False)
    run(root, sys.executable, "brand/build-option-berth.py", "--check", capture=False)
    if sys.platform == "darwin":
        with tempfile.TemporaryDirectory(prefix="oberth-cli-io-") as tmp:
            binary = str(Path(tmp) / "checks")
            run(root, "swiftc", "client/macos/Sources/CLI.swift", "client/macos/Sources/DaemonLaunch.swift",
                "client/macos/Tests/CLIIOTests.swift", "-o", binary, capture=False)
            run(root, binary, capture=False)
        with tempfile.TemporaryDirectory(prefix="oberth-read-lifetime-") as tmp:
            binary = str(Path(tmp) / "git-checks")
            run(root, "swiftc", "client/macos/Sources/CLI.swift", "client/macos/Sources/DaemonLaunch.swift",
                "client/macos/Sources/GitStore.swift", "client/macos/Sources/GitReadQueue.swift",
                "client/macos/Sources/GitModel.swift", "client/macos/Tests/GitCancellationTests.swift",
                "-o", binary, capture=False)
            run(root, binary, capture=False)
            binary = str(Path(tmp) / "draft-checks")
            run(root, "swiftc", "client/macos/Sources/DraftRunState.swift",
                "client/macos/Tests/DraftRunStateTests.swift", "-o", binary, capture=False)
            run(root, binary, capture=False)
        with tempfile.TemporaryDirectory(prefix="oberth-draft-native-") as tmp:
            binary = str(Path(tmp) / "checks")
            sources = ["AddProjectSheet", "DraftRunState", "CLI", "DaemonLaunch", "Brand", "UISettings", "BerthGeometry"]
            run(root, "swiftc", *(f"client/macos/Sources/{name}.swift" for name in sources),
                "client/macos/Tests/DraftSheetInteractionTests.swift", "-o", binary, capture=False)
            home, berth = Path(tmp) / "home", Path(tmp) / "berth"
            home.mkdir()
            berth.mkdir()
            run(root, binary, capture=False, environment={"HOME": str(home), "CFFIXED_USER_HOME": str(home),
                                                          "BERTH_HOME": str(berth)})
        run(root, "bash", "client/macos/build.sh", capture=False)
        with tempfile.TemporaryDirectory(prefix="oberth-git-queue-") as tmp:
            binary = str(Path(tmp) / "checks")
            run(root, "swiftc", "client/macos/Sources/GitReadQueue.swift",
                "client/macos/Tests/GitReadQueueTests.swift", "-o", binary, capture=False)
            run(root, binary, capture=False)
        if (root / "client/macos/TerminalTests").is_dir():
            run(root, "swift", "run", "--package-path", "client/macos", "--force-resolved-versions", "TerminalChecks", capture=False)
        if (root / "client/macos/AgentTests").is_dir():
            # Validate the product planner and GUI together, not only a synthetic
            # replacement. AgentChecks creates its own fake providers and HOME.
            with tempfile.TemporaryDirectory(prefix="oberth-release-agent-") as tmp:
                binary = str(Path(tmp) / "oberth")
                run(engine, "go", "build", "-o", binary, ".", capture=False)
                run(root, "swift", "run", "--package-path", "client/macos", "--force-resolved-versions",
                    "AgentChecks", capture=False, environment={"BERTH_AGENT_TEST_BINARY": binary})


def clean(root: Path) -> None:
    if run(root, "git", "status", "--porcelain", "--untracked-files=all"):
        raise RuntimeError("release requires a clean worktree, including untracked files")


def remote_preflight(root: Path, head: str, tag: str) -> None:
    if run(root, "git", "remote", "get-url", "origin") not in ACCEPTED_REMOTES:
        raise RuntimeError("origin is not the expected release repository")
    remote = run(root, "git", "ls-remote", "--refs", "origin", "refs/heads/main", "refs/tags/" + tag)
    refs = dict(line.split("\t")[::-1] for line in remote.splitlines() if line)
    if refs.get("refs/heads/main") != head:
        raise RuntimeError("local main must exactly match origin/main before release")
    if "refs/tags/" + tag in refs or run(root, "git", "tag", "--list", tag):
        raise RuntimeError("release tag already exists; tags are never overwritten")


def update_candidate(root: Path, spec: dict[str, str]) -> None:
    (root / "VERSION").write_text(spec["version"] + "\n")
    if spec["kind"] == "protocol":
        path = root / PROTOCOL_FILE
        path.write_text(PROTOCOL_RE.sub('const ProtocolVersion = "' + spec["protocol"] + '"', path.read_text()))
        run(root / "engine", "go", "generate", "./internal/daemon/rpc", capture=False)
    check_versions(root)


def release(root: Path, kind: str, publish: bool, verifier: Callable[[Path], None] = verify) -> dict[str, str]:
    spec = plan(root, kind)
    clean(root)
    if run(root, "git", "branch", "--show-current") != "main":
        raise RuntimeError("execute releases from reviewed main, not a topic branch")
    head = run(root, "git", "rev-parse", "HEAD")
    remote_preflight(root, head, spec["tag"])
    common = Path(run(root, "git", "rev-parse", "--git-common-dir"))
    if not common.is_absolute():
        common = root / common
    lock = common / "oberth-release.lock"
    fd = os.open(lock, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    os.close(fd)
    try:
        with tempfile.TemporaryDirectory(prefix="oberth-release-") as tmp:
            candidate = Path(tmp) / "candidate"
            run(root, "git", "worktree", "add", "--detach", str(candidate), head)
            try:
                update_candidate(candidate, spec)
                verifier(candidate)
                allowed = {"VERSION"}
                if kind == "protocol":
                    allowed |= {str(PROTOCOL_FILE), str(SCHEMA_FILE)}
                changed = set(run(candidate, "git", "diff", "--name-only", "HEAD").splitlines())
                if not changed.issubset(allowed) or run(candidate, "git", "ls-files", "--others", "--exclude-standard"):
                    raise RuntimeError("verification changed unexpected tracked or untracked inputs")
                run(candidate, "git", "diff", "--check")
                run(candidate, "git", "add", "--", *sorted(allowed))
                run(candidate, "git", "-c", "user.name=SheathedSharp", "-c", "user.email=116537753+SheathedSharp@users.noreply.github.com",
                    "commit", "-m", "chore(release): " + spec["tag"])
                commit = run(candidate, "git", "rev-parse", "HEAD")
                clean(root)
                if run(root, "git", "rev-parse", "HEAD") != head:
                    raise RuntimeError("main moved during verification; no tag or push performed")
                remote_preflight(root, head, spec["tag"])
                run(root, "git", "merge", "--ff-only", commit)
                clean(root)
                run(root, "git", "-c", "user.name=SheathedSharp", "-c", "user.email=116537753+SheathedSharp@users.noreply.github.com",
                    "tag", "-a", spec["tag"], commit, "-m", "Release " + spec["tag"])
                spec["commit"] = commit
                spec["state"] = "tagged-locally"
                if publish:
                    try:
                        run(root, "git", "push", "--atomic", "origin", commit + ":refs/heads/main", "refs/tags/" + spec["tag"], capture=False)
                    except RuntimeError as error:
                        raise RuntimeError(f"local commit and {spec['tag']} retained; atomic push failed. Verify remote refs before retrying; never force or recreate the tag") from error
                    spec["state"] = "pushed-awaiting-release-workflow"
                return spec
            finally:
                # Only this invocation's disposable candidate is removed.
                run(root, "git", "worktree", "remove", "--force", str(candidate))
    finally:
        with contextlib.suppress(FileNotFoundError):
            lock.unlink()


def validate_tag(root: Path, tag: str) -> None:
    check_versions(root)
    if tag != "v" + (root / "VERSION").read_text().strip():
        raise ValueError("tag and VERSION do not match")
    if run(root, "git", "cat-file", "-t", "refs/tags/" + tag) != "tag":
        raise ValueError("release tag must be annotated")
    if run(root, "git", "rev-parse", "refs/tags/" + tag + "^{commit}") != run(root, "git", "rev-parse", "HEAD"):
        raise ValueError("checkout is not the tagged commit")
    run(root, "git", "merge-base", "--is-ancestor", "HEAD", "origin/main")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--validate-tag", help="validate an existing annotated release tag without publishing")
    parser.add_argument("--kind", choices=("protocol", "feature", "fix"))
    modes = parser.add_mutually_exclusive_group()
    modes.add_argument("--dry-run", action="store_true", help="plan only (the default); no writes, builds, tags or network")
    modes.add_argument("--execute", action="store_true", help="verify, create version commit and annotated local tag")
    modes.add_argument("--publish", action="store_true", help="execute and atomically push main plus tag; CI publishes artifacts")
    modes.add_argument("--verify-only", action="store_true", help="run release gates without changing version or refs")
    args = parser.parse_args()
    try:
        if args.validate_tag:
            validate_tag(ROOT, args.validate_tag)
            return 0
        if args.verify_only:
            verify(ROOT)
            return 0
        if args.kind is None:
            parser.error("--kind is required")
        result = release(ROOT, args.kind, args.publish) if args.execute or args.publish else plan(ROOT, args.kind)
        print(json.dumps(result, indent=2, ensure_ascii=False))
        return 0
    except (OSError, RuntimeError, ValueError) as error:
        print(f"release refused: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
