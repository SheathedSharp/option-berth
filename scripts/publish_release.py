#!/usr/bin/env python3
"""Publish only the complete, round-trip-verified asset set from tag CI."""
from __future__ import annotations
import argparse
import hashlib
from pathlib import Path
import re
import subprocess
import tempfile

REPOSITORY = "SheathedSharp/option-berth"


def expected_assets(tag: str) -> set[str]:
    if not re.fullmatch(r"v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)", tag):
        raise ValueError("invalid version tag")
    archives = {f"option-berth-{tag}-{system}-{arch}." + ("zip" if system == "windows" else "tar.gz")
                for system in ("darwin", "linux", "windows") for arch in ("amd64", "arm64")}
    archives.add(f"OptionBerth-{tag}-macos-arm64-adhoc.zip")
    return archives | {name + ".sha256" for name in archives}


def verify_assets(directory: Path, tag: str, aggregate: bool = False) -> list[Path]:
    expected = expected_assets(tag) | ({"SHA256SUMS"} if aggregate else set())
    actual = {path.name for path in directory.iterdir()}
    if actual != expected:
        raise ValueError("incomplete or unexpected release assets")
    for name in sorted(expected_assets(tag)):
        path = directory / name
        if path.is_symlink() or not path.is_file():
            raise ValueError("release assets must be regular files")
        if not name.endswith(".sha256"):
            with path.open("rb") as stream:
                digest = hashlib.file_digest(stream, "sha256").hexdigest()
            if (directory / (name + ".sha256")).read_text() != digest + "  " + name + "\n":
                raise ValueError("checksum mismatch: " + name)
    if aggregate:
        if (directory / "SHA256SUMS").is_symlink() or not (directory / "SHA256SUMS").is_file():
            raise ValueError("aggregate checksum must be a regular file")
        expected_sum = "".join((directory / name).read_text() for name in sorted(expected_assets(tag)) if name.endswith(".sha256"))
        if (directory / "SHA256SUMS").read_text() != expected_sum:
            raise ValueError("aggregate checksum mismatch")
    return sorted(directory / name for name in expected)


def gh(*args: str) -> None:
    subprocess.run(["gh", *args, "--repo", REPOSITORY], check=True)


def publish(directory: Path, tag: str) -> None:
    verify_assets(directory, tag)
    previous = subprocess.run(["gh", "release", "view", tag, "--repo", REPOSITORY], capture_output=True)
    if previous.returncode == 0:
        raise RuntimeError("release already exists; inspect the existing draft before any manual recovery")
    (directory / "SHA256SUMS").write_text("".join(path.read_text() for path in sorted(directory.glob("*.sha256"))))
    files = verify_assets(directory, tag, aggregate=True)
    # Snapshot the verified inputs before network writes. A remote set can be
    # internally consistent yet not be the build this invocation uploaded.
    local_hashes = {}
    for path in files:
        with path.open("rb") as stream:
            local_hashes[path.name] = hashlib.file_digest(stream, "sha256").hexdigest()
    notes = ("Protocol / feature / fix versioning. See the repository release guide.\n\n"
             "The macOS app includes the matching engine and has an ad-hoc signature only: "
             "it is not Developer ID signed or notarized. Windows ARM64 and some CLI archives are "
             "cross-built; build availability is not a claim of full native lifecycle coverage. "
             "Verify SHA256SUMS before use. Third-party notices are included in every archive.")
    # The release remains a draft until every asset survives a download/hash check.
    gh("release", "create", tag, "--verify-tag", "--draft", "--generate-notes", "--title", "option-berth " + tag, "--notes", notes)
    gh("release", "upload", tag, *map(str, files))
    with tempfile.TemporaryDirectory(prefix="berth-release-download-") as tmp:
        gh("release", "download", tag, "--dir", tmp)
        downloaded = verify_assets(Path(tmp), tag, aggregate=True)
        for path in downloaded:
            with path.open("rb") as stream:
                digest = hashlib.file_digest(stream, "sha256").hexdigest()
            if digest != local_hashes[path.name]:
                raise ValueError("download does not match local release inputs: " + path.name)
    gh("release", "edit", tag, "--draft=false")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--assets", type=Path, required=True)
    args = parser.parse_args()
    try:
        import release
        release.validate_tag(Path(__file__).resolve().parent.parent, args.tag)
        publish(args.assets.resolve(), args.tag)
    except (OSError, ValueError, RuntimeError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"publication failed or outcome is unknown; inspect the release before retrying: {error}\n")
