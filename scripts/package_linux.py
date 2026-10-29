#!/usr/bin/env python3
"""Build .deb and .rpm packages from one released Linux archive with nfpm.

The archive is the source of truth: the script verifies its .sha256 sidecar,
extracts exactly the files the release ships, and installs the same oberth,
jev-attention, licence and build manifest into both packages. nfpm writes the
package metadata, so cross-architecture packages do not require rpmbuild.
"""
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parent.parent
MAINTAINER = "SheathedSharp <z404878860@163.com>"
RPM_ARCH = {"amd64": "x86_64", "arm64": "aarch64"}


def digest(path: Path) -> str:
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def verify_archive(archive: Path) -> None:
    sidecar = archive.with_name(archive.name + ".sha256")
    if not sidecar.is_file():
        raise ValueError(f"missing checksum sidecar: {sidecar.name}")
    parts = sidecar.read_text().strip().split()
    if len(parts) != 2 or parts[1] != archive.name:
        raise ValueError(f"malformed checksum sidecar: {sidecar.name}")
    if parts[0] != digest(archive):
        raise ValueError(f"SHA256 mismatch for {archive.name}")


def extract(archive: Path, destination: Path) -> Path:
    root = destination.resolve()
    with tarfile.open(archive, "r:gz") as tar:
        for member in tar.getmembers():
            target = (destination / member.name).resolve()
            if target != root and root not in target.parents:
                raise ValueError(f"archive member escapes destination: {member.name}")
        try:
            tar.extractall(destination, filter="data")
        except TypeError:
            tar.extractall(destination)
    children = [path for path in destination.iterdir() if path.is_dir()]
    if len(children) != 1:
        raise ValueError("archive must contain exactly one top-level directory")
    return children[0]


def nfpm_config(root: Path, version: str, arch: str) -> str:
    def quoted(path: Path) -> str:
        return json.dumps(str(path))

    return f"""name: option-berth
arch: {arch}
platform: linux
version: {version}
release: "1"
section: devel
priority: optional
maintainer: {MAINTAINER}
description: Service facts and lifecycle for local Git worktrees
homepage: https://github.com/SheathedSharp/option-berth
license: MIT
depends:
  - ca-certificates
contents:
  - src: {quoted(root / "oberth")}
    dst: /usr/bin/oberth
    file_info:
      mode: 0755
  - src: {quoted(root / "jev-attention")}
    dst: /usr/bin/jev-attention
    file_info:
      mode: 0755
  - src: {quoted(root / "LICENSE")}
    dst: /usr/share/doc/option-berth/LICENSE
  - src: {quoted(root / "LICENSE")}
    dst: /usr/share/doc/option-berth/copyright
  - src: {quoted(root / "build-manifest.json")}
    dst: /usr/share/doc/option-berth/build-manifest.json
  - src: {quoted(root / "licenses")}
    dst: /usr/share/doc/option-berth/licenses
"""


def checksum_sidecar(path: Path) -> None:
    path.with_name(path.name + ".sha256").write_text(digest(path) + "  " + path.name + "\n")


def build(root: Path, version: str, arch: str, output: Path, packagers: tuple[str, ...]) -> list[Path]:
    nfpm = shutil.which("nfpm")
    if not nfpm:
        raise RuntimeError("nfpm is required to build the Linux packages")
    output.mkdir(parents=True, exist_ok=True)
    names = {
        "deb": f"option-berth_{version}_{arch}.deb",
        "rpm": f"option-berth-{version}-1.{RPM_ARCH[arch]}.rpm",
    }
    packages = []
    with tempfile.TemporaryDirectory(prefix="berth-nfpm-") as temporary:
        config = Path(temporary) / "nfpm.yaml"
        config.write_text(nfpm_config(root, version, arch))
        for kind in packagers:
            target = output / names[kind]
            result = subprocess.run(
                [nfpm, "package", "--config", str(config), "--packager", kind, "--target", str(target)],
                capture_output=True, text=True,
            )
            if result.returncode:
                raise RuntimeError(f"nfpm {kind} failed: {result.stderr.strip() or result.stdout.strip()}")
            checksum_sidecar(target)
            packages.append(target)
    return packages


def package(dist: Path, arch: str, output: Path, packagers: tuple[str, ...] = ("deb", "rpm")) -> list[Path]:
    version = (ROOT / "VERSION").read_text().strip()
    archive = dist / f"option-berth-v{version}-linux-{arch}.tar.gz"
    if not archive.is_file():
        raise ValueError(f"missing release archive: {archive.name}")
    verify_archive(archive)
    with tempfile.TemporaryDirectory(prefix="berth-linux-package-") as temporary:
        root = extract(archive, Path(temporary))
        return build(root, version, arch, output, packagers)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--arch", choices=("amd64", "arm64"), required=True)
    parser.add_argument("--dist", type=Path, default=ROOT / "dist")
    parser.add_argument("--output", type=Path)
    parser.add_argument("--deb-only", action="store_true", help="skip the rpm package")
    args = parser.parse_args()
    output = (args.output or args.dist).resolve()
    packagers = ("deb",) if args.deb_only else ("deb", "rpm")
    for path in package(args.dist.resolve(), args.arch, output, packagers):
        print(path.name)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
