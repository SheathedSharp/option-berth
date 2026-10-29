#!/usr/bin/env python3
"""Build .deb and .rpm packages from one released Linux archive.

The archive is the source of truth: the script verifies its .sha256 sidecar,
extracts exactly the files the release ships, and installs the same oberth,
jev-attention, licence and build manifest into the package.
"""
from __future__ import annotations

import argparse
import hashlib
import io
import os
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
    line = sidecar.read_text().strip()
    expected = line.split()
    if len(expected) != 2 or expected[1] != archive.name:
        raise ValueError(f"malformed checksum sidecar: {sidecar.name}")
    if expected[0] != digest(archive):
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


def tar_bytes(entries: list[tuple[str, bytes, int]]) -> bytes:
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w:gz", format=tarfile.GNU_FORMAT) as tar:
        for name, data, mode in entries:
            info = tarfile.TarInfo(name)
            info.size = len(data)
            info.mode = mode
            info.mtime = 0
            info.uid = info.gid = 0
            info.uname = info.gname = "root"
            tar.addfile(info, io.BytesIO(data))
    return buffer.getvalue()


def ar_bytes(entries: list[tuple[str, bytes]]) -> bytes:
    output = bytearray(b"!<arch>\n")
    for name, data in entries:
        if len(name) > 16:
            raise ValueError(f"ar member name too long: {name}")
        header = f"{name:<16}{0:<12}{0:<6}{0:<6}{0o100644:<8o}{len(data):<10}`\n"
        if len(header) != 60:
            raise AssertionError("ar header must be 60 bytes")
        output += header.encode("ascii") + data
        if len(data) % 2:
            output += b"\n"
    return bytes(output)


def control_text(version: str, arch: str, installed_size: int) -> str:
    return (
        "Package: option-berth\n"
        f"Version: {version}\n"
        f"Architecture: {arch}\n"
        f"Maintainer: {MAINTAINER}\n"
        f"Installed-Size: {installed_size}\n"
        "Depends: ca-certificates\n"
        "Section: devel\n"
        "Priority: optional\n"
        "Homepage: https://github.com/SheathedSharp/option-berth\n"
        "Description: Service facts and lifecycle for local Git worktrees\n"
        " option-berth reads a worktree manifest and its runtime, then starts or\n"
        " stops the declared services. The CLI is the product interface; the macOS\n"
        " client and coding-agent skill are separate repositories.\n"
    )


def data_entries(root: Path) -> list[tuple[str, bytes, int]]:
    entries = []
    for relative, mode in (
        ("oberth", 0o755),
        ("jev-attention", 0o755),
        ("LICENSE", 0o644),
        ("build-manifest.json", 0o644),
    ):
        source = root / relative
        if not source.is_file():
            raise ValueError(f"archive is missing {relative}")
        entries.append((f"./usr/{'bin' if relative in ('oberth', 'jev-attention') else 'share/doc/option-berth'}/{relative}", source.read_bytes(), mode))
    licences = sorted((root / "licenses").glob("*")) if (root / "licenses").is_dir() else []
    for source in licences:
        entries.append((f"./usr/share/doc/option-berth/licenses/{source.name}", source.read_bytes(), 0o644))
    entries.append(("./usr/share/doc/option-berth/copyright", (root / "LICENSE").read_bytes(), 0o644))
    return entries


def build_deb(root: Path, version: str, arch: str, output: Path) -> Path:
    entries = data_entries(root)
    installed_size = sum(len(data) for _, data, _ in entries) // 1024
    md5s = "".join(
        hashlib.md5(data).hexdigest() + "  " + name.removeprefix("./") + "\n"
        for name, data, _ in entries
        if name.startswith("./usr/")
    )
    control = tar_bytes([
        ("./control", control_text(version, arch, installed_size).encode("utf-8"), 0o644),
        ("./md5sums", md5s.encode("utf-8"), 0o644),
    ])
    data = tar_bytes(entries)
    package = output / f"option-berth_{version}_{arch}.deb"
    package.write_bytes(ar_bytes([
        ("debian-binary", b"2.0\n"),
        ("control.tar.gz", control),
        ("data.tar.gz", data),
    ]))
    return package


def spec_text(version: str, arch: str, source: Path) -> str:
    return f"""Name: option-berth
Version: {version}
Release: 1
Summary: Service facts and lifecycle for local Git worktrees
License: MIT
URL: https://github.com/SheathedSharp/option-berth
Requires: ca-certificates
AutoReqProv: no
%global debug_package %{{nil}}
%global __strip /bin/true
BuildArch: {arch}

%description
option-berth reads a worktree manifest and its runtime, then starts or stops
the declared services. The CLI is the product interface; the macOS client and
coding-agent skill are separate repositories.

%install
rm -rf %{{buildroot}}
mkdir -p %{{buildroot}}/usr/bin %{{buildroot}}/usr/share/doc/option-berth/licenses
install -m 0755 {source}/oberth %{{buildroot}}/usr/bin/oberth
install -m 0755 {source}/jev-attention %{{buildroot}}/usr/bin/jev-attention
install -m 0644 {source}/LICENSE %{{buildroot}}/usr/share/doc/option-berth/LICENSE
install -m 0644 {source}/LICENSE %{{buildroot}}/usr/share/doc/option-berth/copyright
install -m 0644 {source}/build-manifest.json %{{buildroot}}/usr/share/doc/option-berth/build-manifest.json
cp -a {source}/licenses/. %{{buildroot}}/usr/share/doc/option-berth/licenses/

%files
/usr/bin/oberth
/usr/bin/jev-attention
%dir /usr/share/doc/option-berth
/usr/share/doc/option-berth/LICENSE
/usr/share/doc/option-berth/copyright
/usr/share/doc/option-berth/build-manifest.json
%dir /usr/share/doc/option-berth/licenses
/usr/share/doc/option-berth/licenses/*

%changelog
* Thu Jan 01 1970 SheathedSharp <z404878860@163.com> - {version}-1
- Release {version}
"""


def build_rpm(root: Path, version: str, arch: str, output: Path) -> Path:
    rpmbuild = shutil.which("rpmbuild")
    if not rpmbuild:
        raise RuntimeError("rpmbuild is required to build the .rpm package")
    rpm_arch = RPM_ARCH[arch]
    with tempfile.TemporaryDirectory(prefix="berth-rpm-") as temporary:
        top = Path(temporary)
        for name in ("BUILD", "BUILDROOT", "RPMS", "SOURCES", "SPECS", "SRPMS"):
            (top / name).mkdir()
        spec = top / "SPECS" / "option-berth.spec"
        spec.write_text(spec_text(version, rpm_arch, root))
        subprocess.run(
            [rpmbuild, "-bb", "--target", rpm_arch, "--define", f"_topdir {top}", str(spec)],
            check=True, capture_output=True,
        )
        built = sorted((top / "RPMS").rglob("option-berth-*.rpm"))
        if len(built) != 1:
            raise RuntimeError("rpmbuild did not produce exactly one package")
        package = output / f"option-berth-{version}-1.{rpm_arch}.rpm"
        shutil.copyfile(built[0], package)
        return package


def checksum_sidecar(path: Path) -> None:
    path.with_name(path.name + ".sha256").write_text(digest(path) + "  " + path.name + "\n")


def package(dist: Path, arch: str, output: Path, deb_only: bool = False) -> list[Path]:
    version = (ROOT / "VERSION").read_text().strip()
    archive = dist / f"option-berth-v{version}-linux-{arch}.tar.gz"
    if not archive.is_file():
        raise ValueError(f"missing release archive: {archive.name}")
    verify_archive(archive)
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="berth-linux-package-") as temporary:
        root = extract(archive, Path(temporary))
        packages = [build_deb(root, version, arch, output)]
        if not deb_only:
            packages.append(build_rpm(root, version, arch, output))
    for path in packages:
        checksum_sidecar(path)
    return packages


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--arch", choices=("amd64", "arm64"), required=True)
    parser.add_argument("--dist", type=Path, default=ROOT / "dist")
    parser.add_argument("--output", type=Path)
    parser.add_argument("--deb-only", action="store_true", help="skip the rpmbuild step")
    args = parser.parse_args()
    output = (args.output or args.dist).resolve()
    for path in package(args.dist.resolve(), args.arch, output, args.deb_only):
        print(path.name)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
