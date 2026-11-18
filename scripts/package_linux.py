#!/usr/bin/env python3
"""Build .deb and .rpm packages from one released Linux archive with nfpm.

The archive is the source of truth: the script verifies its .sha256 sidecar,
extracts exactly the files the release ships, and installs the same oberth binary,
licence and build manifest into both packages. nfpm writes the
package metadata, so cross-architecture packages do not require rpmbuild.

With `--resume`, a package already in the output directory is reused when its
checksum matches and it records the build manifest of this archive; a package
that does not verify is an error, never a rebuild over it.
"""
from __future__ import annotations

import argparse
import hashlib
import io
import json
from pathlib import Path
import shutil
import struct
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parent.parent
MAINTAINER = "SheathedSharp <z404878860@163.com>"
RPM_ARCH = {"amd64": "x86_64", "arm64": "aarch64"}
MANIFEST = "build-manifest.json"
MANIFEST_DEST = "usr/share/doc/option-berth/" + MANIFEST
AR_MAGIC = b"!<arch>\n"
RPM_LEAD = b"\xed\xab\xee\xdb"
RPM_HEADER = b"\x8e\xad\xe8\x01"
RPM_LEAD_SIZE = 96
# rpm header value types this script reads, and the file digest algorithms a
# package may record for the files it installs.
RPM_INT32, RPM_STRING, RPM_STRING_ARRAY = 4, 6, 8
RPM_FILE_DIGESTS_TAG, RPM_BASENAMES_TAG, RPM_FILE_DIGEST_ALGORITHM_TAG = 1035, 1117, 5011
FILE_DIGEST_ALGORITHMS = {1: "md5", 2: "sha1", 8: "sha256"}


def digest(path: Path) -> str:
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def verify_sidecar(path: Path) -> None:
    sidecar = path.with_name(path.name + ".sha256")
    if not sidecar.is_file():
        raise ValueError(f"missing checksum sidecar: {sidecar.name}")
    parts = sidecar.read_text().strip().split()
    if len(parts) != 2 or parts[1] != path.name:
        raise ValueError(f"malformed checksum sidecar: {sidecar.name}")
    if parts[0] != digest(path):
        raise ValueError(f"SHA256 mismatch for {path.name}")


def ar_members(package: Path) -> dict[str, bytes]:
    """The members of a .deb, which is an ar archive of tar streams."""
    data = package.read_bytes()
    if not data.startswith(AR_MAGIC):
        raise ValueError(f"{package.name} is not a .deb archive")
    members, offset = {}, len(AR_MAGIC)
    while offset < len(data):
        header = data[offset:offset + 60]
        if len(header) != 60 or header[58:] != b"`\n":
            raise ValueError(f"{package.name} has a truncated .deb member header")
        try:
            size = int(header[48:58].decode("ascii").strip())
        except ValueError:
            raise ValueError(f"{package.name} has a malformed .deb member header") from None
        name = header[:16].decode("ascii", "replace").strip()
        offset += 60
        members[name] = data[offset:offset + size]
        offset += size + size % 2
    return members


def deb_manifest_digest(package: Path) -> tuple[str, str]:
    """The digest of the build manifest this .deb installs."""
    payload = ar_members(package).get("data.tar.gz")
    if payload is None:
        raise ValueError(f"{package.name} has no data.tar.gz")
    with tarfile.open(fileobj=io.BytesIO(payload), mode="r:gz") as tar:
        try:
            installed = tar.extractfile("./" + MANIFEST_DEST)
        except KeyError:
            installed = None
        if installed is None:
            raise ValueError(f"{package.name} does not install {MANIFEST_DEST}") from None
        return "sha256", hashlib.sha256(installed.read()).hexdigest()


def rpm_strings(store: bytes, offset: int, count: int) -> list[str]:
    values, cursor = [], offset
    for _ in range(count):
        end = store.index(b"\0", cursor)
        values.append(store[cursor:end].decode())
        cursor = end + 1
    return values


def rpm_header(data: bytes, offset: int) -> tuple[dict[int, object], int]:
    """One rpm header's decoded tags, and the offset just past it."""
    if data[offset:offset + 4] != RPM_HEADER:
        raise ValueError("expected an rpm header")
    if len(data) < offset + 16:
        raise ValueError("truncated rpm header")
    count, size = struct.unpack(">II", data[offset + 8:offset + 16])
    store_at = offset + 16 + count * 16
    if store_at + size > len(data):
        raise ValueError("truncated rpm header")
    store = data[store_at:store_at + size]
    tags: dict[int, object] = {}
    try:
        for position in range(count):
            start = offset + 16 + position * 16
            tag, kind, value, amount = struct.unpack(">IIII", data[start:start + 16])
            if kind == RPM_STRING:
                tags[tag] = rpm_strings(store, value, 1)[0]
            elif kind == RPM_STRING_ARRAY:
                tags[tag] = rpm_strings(store, value, amount)
            elif kind == RPM_INT32:
                # A tag may hold one value or one per file: nfpm records the
                # file digest algorithm once (2.48) or once per file (2.46).
                tags[tag] = list(struct.unpack(f">{amount}I", store[value:value + 4 * amount]))
    except (struct.error, ValueError):
        raise ValueError("malformed rpm header") from None
    return tags, store_at + size


def rpm_manifest_digest(package: Path) -> tuple[str, str]:
    """The digest this .rpm records for the build manifest it installs."""
    data = package.read_bytes()
    if data[:4] != RPM_LEAD:
        raise ValueError(f"{package.name} is not an rpm package")
    if len(data) < RPM_LEAD_SIZE:
        raise ValueError(f"{package.name} has a truncated rpm lead")
    try:
        _, signature_end = rpm_header(data, RPM_LEAD_SIZE)
    except ValueError:
        raise ValueError(f"{package.name} has a malformed rpm signature header") from None
    # The main header follows the signature header at the next 8-byte boundary.
    start = next((offset for offset in range(signature_end, min(signature_end + 8, len(data)))
                  if data[offset:offset + 4] == RPM_HEADER), None)
    if start is None:
        raise ValueError(f"{package.name} has no rpm main header")
    tags, _ = rpm_header(data, start)
    # An rpm without the tag predates it and records md5.
    algorithms = tags.get(RPM_FILE_DIGEST_ALGORITHM_TAG, [1])
    basenames = tags.get(RPM_BASENAMES_TAG)
    digests = tags.get(RPM_FILE_DIGESTS_TAG)
    if not isinstance(algorithms, list) or len(set(algorithms)) != 1:
        raise ValueError(f"{package.name} does not name one file digest algorithm")
    algorithm = algorithms[0]
    if algorithm not in FILE_DIGEST_ALGORITHMS:
        raise ValueError(f"{package.name} records an unknown file digest algorithm {algorithm}")
    if not isinstance(basenames, list) or not isinstance(digests, list) or len(basenames) != len(digests):
        raise ValueError(f"{package.name} records no usable file digests")
    if MANIFEST not in basenames:
        raise ValueError(f"{package.name} does not install {MANIFEST}")
    return FILE_DIGEST_ALGORITHMS[algorithm], digests[basenames.index(MANIFEST)]


def verify_package(package: Path, kind: str, manifest: bytes) -> None:
    """An existing package is reused only when it belongs to this archive."""
    verify_sidecar(package)
    algorithm, recorded = deb_manifest_digest(package) if kind == "deb" else rpm_manifest_digest(package)
    if recorded != hashlib.new(algorithm, manifest).hexdigest():
        raise ValueError(f"{package.name} was not built from this archive: its build manifest differs")


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
  - src: {quoted(root / "LICENSE")}
    dst: /usr/share/doc/option-berth/LICENSE
  - src: {quoted(root / "LICENSE")}
    dst: /usr/share/doc/option-berth/copyright
  - src: {quoted(root / MANIFEST)}
    dst: /{MANIFEST_DEST}
  - src: {quoted(root / "licenses")}
    dst: /usr/share/doc/option-berth/licenses
"""


def checksum_sidecar(path: Path) -> None:
    path.with_name(path.name + ".sha256").write_text(digest(path) + "  " + path.name + "\n")


def retained(root: Path, output: Path, names: dict[str, str], packagers: tuple[str, ...]) -> list[str]:
    """The packagers whose existing package verifies against this archive.

    A package set that exists but does not verify stops the run instead of
    being rebuilt over: what is in the output directory stays for inspection.
    """
    manifest = root / MANIFEST
    if not manifest.is_file():
        raise ValueError(f"release archive does not ship {MANIFEST}")
    contents = manifest.read_bytes()
    kept = []
    for kind in packagers:
        target = output / names[kind]
        sidecar = target.with_name(target.name + ".sha256")
        if not target.is_file() and not sidecar.is_file():
            continue
        if not target.is_file() or not sidecar.is_file():
            raise ValueError(f"resume found an incomplete package set for {names[kind]}: "
                             "the package and its .sha256 sidecar must both be present")
        verify_package(target, kind, contents)
        kept.append(kind)
    return kept


def build(root: Path, version: str, arch: str, output: Path, packagers: tuple[str, ...],
          resume: bool = False) -> list[Path]:
    output.mkdir(parents=True, exist_ok=True)
    names = {
        "deb": f"option-berth_{version}_{arch}.deb",
        "rpm": f"option-berth-{version}-1.{RPM_ARCH[arch]}.rpm",
    }
    kept = retained(root, output, names, packagers) if resume else []
    pending = [kind for kind in packagers if kind not in kept]
    if pending:
        nfpm = shutil.which("nfpm")
        if not nfpm:
            raise RuntimeError("nfpm is required to build the Linux packages")
        with tempfile.TemporaryDirectory(prefix="berth-nfpm-") as temporary:
            config = Path(temporary) / "nfpm.yaml"
            config.write_text(nfpm_config(root, version, arch))
            for kind in pending:
                target = output / names[kind]
                result = subprocess.run(
                    [nfpm, "package", "--config", str(config), "--packager", kind, "--target", str(target)],
                    capture_output=True, text=True,
                )
                if result.returncode:
                    raise RuntimeError(f"nfpm {kind} failed: {result.stderr.strip() or result.stdout.strip()}")
                checksum_sidecar(target)
    return [output / names[kind] for kind in packagers]


def package(dist: Path, arch: str, output: Path, packagers: tuple[str, ...] = ("deb", "rpm"),
            resume: bool = False) -> list[Path]:
    version = (ROOT / "VERSION").read_text().strip()
    archive = dist / f"option-berth-v{version}-linux-{arch}.tar.gz"
    if not archive.is_file():
        raise ValueError(f"missing release archive: {archive.name}")
    verify_sidecar(archive)
    with tempfile.TemporaryDirectory(prefix="berth-linux-package-") as temporary:
        root = extract(archive, Path(temporary))
        return build(root, version, arch, output, packagers, resume)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--arch", choices=("amd64", "arm64"), required=True)
    parser.add_argument("--dist", type=Path, default=ROOT / "dist")
    parser.add_argument("--output", type=Path)
    parser.add_argument("--deb-only", action="store_true", help="skip the rpm package")
    parser.add_argument("--resume", action="store_true",
                        help="reuse packages in the output directory when they verify against this archive")
    args = parser.parse_args()
    output = (args.output or args.dist).resolve()
    packagers = ("deb",) if args.deb_only else ("deb", "rpm")
    for path in package(args.dist.resolve(), args.arch, output, packagers, resume=args.resume):
        print(path.name)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
