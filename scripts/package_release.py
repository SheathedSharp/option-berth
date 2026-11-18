#!/usr/bin/env python3
"""Build release archives with exact build identity, runtime notices and checksums.

With `--resume`, an archive already in `--output` is reused when its checksum
and its recorded build identity match this build; anything else is an error,
never an overwrite.
"""
from __future__ import annotations

import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import plistlib
import shutil
import subprocess
import tarfile
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parent.parent
TARGETS = {(system, arch) for system in ("darwin", "linux", "windows") for arch in ("amd64", "arm64")}
# What a release archive pins. A resumed run may reuse an archive only when all
# four still describe the build this invocation would produce.
IDENTITY = ("version", "commit", "target", "protocol")
MANIFEST = "build-manifest.json"


def command(root: Path, env: dict[str, str], *args: str) -> str:
    result = subprocess.run(args, cwd=root, env=env, text=True, capture_output=True)
    if result.returncode:
        raise RuntimeError(f"{args[0]} failed: {result.stderr}")
    return result.stdout.strip()


def json_stream(text: str):
    decoder = json.JSONDecoder()
    while text.strip():
        text = text.lstrip()
        value, end = decoder.raw_decode(text)
        yield value
        text = text[end:]


def runtime_notices(root: Path, env: dict[str, str]) -> tuple[str, list[dict[str, str]]]:
    packages = json_stream(command(root / "engine", env, "go", "list", "-deps", "-json", "./cmd/oberth"))
    modules = {}
    for package in packages:
        module = package.get("Module")
        if module and not module.get("Main"):
            if module.get("Replace"):
                raise RuntimeError("release refuses replaced runtime modules; review provenance first")
            modules[module["Path"]] = module
    sections, inventory = [], []
    for name, module in sorted(modules.items()):
        directory = Path(module["Dir"])
        notices = sorted(p for p in directory.iterdir() if p.is_file() and p.name.upper().startswith(("LICENSE", "LICENCE", "COPYING", "NOTICE", "COPYRIGHT")))
        if not notices:
            raise RuntimeError(f"no root license/notice found for runtime module {name}")
        version = module.get("Version", "unknown")
        inventory.append({"module": name, "version": version})
        for notice in notices:
            sections.append(f"===== {name} {version} / {notice.name} =====\n" + notice.read_text(errors="strict"))
    return "\n\n".join(sections) + "\n", inventory


def checksum(path: Path) -> str:
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def checksum_sidecar(path: Path) -> None:
    path.with_name(path.name + ".sha256").write_text(checksum(path) + "  " + path.name + "\n")


def verify_sidecar(path: Path) -> None:
    sidecar = path.with_name(path.name + ".sha256")
    if not sidecar.is_file():
        raise ValueError(f"missing checksum sidecar: {sidecar.name}")
    parts = sidecar.read_text().strip().split()
    if len(parts) != 2 or parts[1] != path.name:
        raise ValueError(f"malformed checksum sidecar: {sidecar.name}")
    if parts[0] != checksum(path):
        raise ValueError(f"SHA256 mismatch for {path.name}")


def build_identity(root: Path, system: str, arch: str) -> dict[str, str]:
    """The inputs a release archive records, computed without building anything."""
    # Share validation with the release entry point without executing a release.
    import release
    release.check_versions(root)
    version = (root / "VERSION").read_text().strip()
    return {"version": version,
            "commit": command(root, dict(os.environ), "git", "rev-parse", "HEAD"),
            "target": system + "/" + arch,
            "protocol": release.protocol(root)}


def archive_manifest(archive: Path, stem: str) -> dict:
    """The build manifest recorded inside a release archive."""
    member = stem + "/" + MANIFEST
    try:
        if archive.name.endswith(".zip"):
            with zipfile.ZipFile(archive) as source:
                return json.loads(source.read(member))
        with tarfile.open(archive, "r:gz") as source:
            extracted = source.extractfile(member)
            if extracted is None:
                raise KeyError(member)
            return json.loads(extracted.read())
    except KeyError:
        raise ValueError(f"{archive.name} has no {member}") from None


def resumable(output: Path, stem: str, system: str, identity: dict[str, str]) -> Path | None:
    """The archive already in `output` when it verifies, or None to build one.

    A set that exists but does not verify stops the run: resume never rebuilds
    over, deletes or silently replaces what a person may be inspecting.
    """
    archive = output / (stem + (".zip" if system == "windows" else ".tar.gz"))
    sidecar = archive.with_name(archive.name + ".sha256")
    if not archive.is_file() and not sidecar.is_file():
        return None
    if not archive.is_file() or not sidecar.is_file():
        raise ValueError(f"resume found an incomplete release set for {stem}: "
                         "the archive and its .sha256 sidecar must both be present")
    verify_sidecar(archive)
    manifest = archive_manifest(archive, stem)
    for key in IDENTITY:
        if manifest.get(key) != identity[key]:
            raise ValueError(f"{archive.name} records {key} {manifest.get(key)!r}, "
                             f"not the {identity[key]!r} this run builds")
    return archive


def package(root: Path, system: str, arch: str, output: Path, resume: bool = False) -> list[Path]:
    if (system, arch) not in TARGETS:
        raise ValueError("unsupported release target")
    env = dict(os.environ, GOOS=system, GOARCH=arch, CGO_ENABLED="0", GOMAXPROCS="4")
    identity = build_identity(root, system, arch)
    version, commit = identity["version"], identity["commit"]
    output.mkdir(parents=True, exist_ok=True)
    stem = f"option-berth-v{version}-{system}-{arch}"
    if resume:
        existing = resumable(output, stem, system, identity)
        if existing is not None:
            return [existing]
    stamp = int(command(root, env, "git", "show", "-s", "--format=%ct", "HEAD"))
    built = datetime.datetime.fromtimestamp(stamp, datetime.timezone.utc).isoformat().replace("+00:00", "Z")
    flags = "-s -w " + " ".join("-X github.com/sheathedsharp/option-berth/engine/internal/buildinfo." + key + "=" + value
                                  for key, value in (("Version", "v" + version), ("Commit", commit[:7]), ("Date", built)))
    created = []
    with tempfile.TemporaryDirectory(prefix="berth-package-") as tmp:
        stage = Path(tmp) / stem
        stage.mkdir()
        suffix = ".exe" if system == "windows" else ""
        for name, source in (("oberth", "./cmd/oberth"),):
            command(root / "engine", env, "go", "build", "-trimpath", "-ldflags", flags, "-o", str(stage / (name + suffix)), source)
        shutil.copyfile(root / "LICENSE", stage / "LICENSE")
        shutil.copyfile(root / "scripts" / "install-path.sh", stage / "install-path.sh")
        notices = stage / "licenses"
        notices.mkdir()
        shutil.copyfile(root / "engine/LICENSE", notices / "engine-LICENSE")
        attribution, boundary, _ = (root / "third_party/README.md").read_text(encoding="utf-8").partition("\n## 源码资料\n")
        if not boundary:
            raise ValueError("third-party guide is missing the source-material boundary")
        (notices / "third-party-notices.md").write_text(attribution + "\n", encoding="utf-8")
        shutil.copyfile(root / "third_party/go-runtime-licenses.txt", notices / "retained-runtime-notices.txt")
        text, modules = runtime_notices(root, env)
        (notices / "target-runtime-notices.txt").write_text(text)
        manifest = {"project": "option-berth", **identity,
                    "go": command(root, env, "go", "version"), "runtime_modules": modules}
        (stage / MANIFEST).write_text(json.dumps(manifest, indent=2) + "\n")
        files = sorted(p for p in stage.rglob("*") if p.is_file())
        (stage / "SHA256SUMS").write_text("".join(checksum(p) + "  " + p.relative_to(stage).as_posix() + "\n" for p in files))
        archive = output / (stem + (".zip" if system == "windows" else ".tar.gz"))
        if archive.exists():
            raise FileExistsError("refusing to overwrite an existing release archive")
        if system == "windows":
            with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED) as target:
                for path in sorted(p for p in stage.rglob("*") if p.is_file()):
                    target.write(path, str(Path(stem) / path.relative_to(stage)))
        else:
            with tarfile.open(archive, "w:gz") as target:
                def normalize(info):
                    info.uid = info.gid = 0
                    info.uname = info.gname = ""
                    info.mtime = stamp
                    return info
                target.add(stage, arcname=stem, filter=normalize)
        checksum_sidecar(archive)
        created.append(archive)
    return created


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--os", choices=("darwin", "linux", "windows"), required=True)
    parser.add_argument("--arch", choices=("amd64", "arm64"), required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--resume", action="store_true",
                        help="reuse the archive in --output when it verifies against this build")
    args = parser.parse_args()
    try:
        for artifact in package(ROOT, args.os, args.arch, args.output.resolve(), resume=args.resume):
            print(artifact.name)
    except (OSError, ValueError, RuntimeError) as error:
        parser.exit(1, f"packaging refused: {error}\n")
