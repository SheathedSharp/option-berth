#!/usr/bin/env python3
"""Publish only the complete, round-trip-verified asset set from tag CI."""
from __future__ import annotations
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tempfile
from typing import NamedTuple

REPOSITORY = "SheathedSharp/option-berth"
AGGREGATE = "SHA256SUMS"
NOTES = ("## 使用前 / Before use\n\n"
         "版本按协议 / 功能 / 修复步进。终端与外部 coding agent 使用同一份 worktree 上下文；"
         "agent 需自行安装登录，后续对话与审批留在其原生界面。\n\n"
         "Windows 构建包不等于完整生命周期支持。Linux 同时提供 .deb 与 .rpm。"
         "请先核对 SHA256SUMS，许可随包附带。\n\n"
         "coding agent 的 skill 是独立产品，见 README 里的链接。\n\n"
         "Protocol / feature / fix versioning. See the repository release guide.\n\n"
         "Linux .deb/.rpm packages are published beside the CLI archives. "
         "Windows ARM64 and some CLI archives are cross-built; build availability is not a claim "
         "of full native lifecycle coverage. Verify SHA256SUMS before use. Third-party notices are "
         "included in every archive. The coding-agent skill is a separate product; "
         "see the link in the README.")


def expected_assets(tag: str) -> set[str]:
    if not re.fullmatch(r"v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)", tag):
        raise ValueError("invalid version tag")
    archives = {f"option-berth-{tag}-{system}-{arch}." + ("zip" if system == "windows" else "tar.gz")
                for system in ("darwin", "linux", "windows") for arch in ("amd64", "arm64")}
    version = tag[1:]
    linux_packages = set()
    for arch, rpm_arch in (("amd64", "x86_64"), ("arm64", "aarch64")):
        linux_packages.add(f"option-berth_{version}_{arch}.deb")
        linux_packages.add(f"option-berth-{version}-1.{rpm_arch}.rpm")
    assets = archives | linux_packages
    return assets | {name + ".sha256" for name in assets}


def sha256(path: Path) -> str:
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def verify_assets(directory: Path, tag: str, aggregate: bool = False) -> list[Path]:
    expected = expected_assets(tag) | ({AGGREGATE} if aggregate else set())
    actual = {path.name for path in directory.iterdir()}
    if actual != expected:
        raise ValueError("incomplete or unexpected release assets")
    for name in sorted(expected_assets(tag)):
        path = directory / name
        if path.is_symlink() or not path.is_file():
            raise ValueError("release assets must be regular files")
        if not name.endswith(".sha256"):
            if (directory / (name + ".sha256")).read_text() != sha256(path) + "  " + name + "\n":
                raise ValueError("checksum mismatch: " + name)
    if aggregate:
        if (directory / AGGREGATE).is_symlink() or not (directory / AGGREGATE).is_file():
            raise ValueError("aggregate checksum must be a regular file")
        expected_sum = "".join((directory / name).read_text() for name in sorted(expected_assets(tag)) if name.endswith(".sha256"))
        if (directory / AGGREGATE).read_text() != expected_sum:
            raise ValueError("aggregate checksum mismatch")
    return sorted(directory / name for name in expected)


def snapshot(files: list[Path]) -> dict[str, str]:
    return {path.name: sha256(path) for path in files}


def gh(*args: str) -> subprocess.CompletedProcess:
    """Run one `gh` command; the only place this script talks to GitHub.

    Output is captured so that a failure carries `gh`'s own message into the
    operator report instead of a bare exit status.
    """
    result = subprocess.run(["gh", *args, "--repo", REPOSITORY], capture_output=True, text=True)
    if result.returncode != 0:
        detail = (result.stderr or result.stdout).strip() or f"exit {result.returncode}"
        raise RuntimeError("gh " + " ".join(args[:3]) + ": " + detail)
    return result


class Release(NamedTuple):
    """The remote state this script may act on: a public release, or a draft."""
    is_draft: bool
    asset_names: frozenset[str]


def read_release(tag: str) -> Release | None:
    """The remote release for this tag, or None when the tag has no release."""
    try:
        stdout = gh("release", "view", tag, "--json", "isDraft,assets").stdout
    except RuntimeError:
        # `gh` reports a missing release as a failed read. Absence only leads to
        # a create, which decides again before anything is written.
        return None
    try:
        state = json.loads(stdout)
        is_draft = state["isDraft"]
        names = frozenset(asset["name"] for asset in state["assets"])
        if not isinstance(is_draft, bool) or not all(isinstance(name, str) for name in names):
            raise TypeError
    except (KeyError, TypeError, ValueError):
        raise ValueError("unreadable release state for " + tag) from None
    return Release(is_draft=is_draft, asset_names=names)


def create_draft(tag: str) -> Release:
    """Create the empty draft, or adopt the one a racing invocation just made."""
    try:
        gh("release", "create", tag, "--verify-tag", "--draft", "--generate-notes",
           "--title", "option-berth " + tag, "--notes", NOTES)
    except RuntimeError:
        # The release can appear between the state read and this create. The
        # winner's state decides whether this invocation may continue at all.
        concurrent = read_release(tag)
        if concurrent is None:
            raise
        return concurrent
    # A new draft holds no assets; the round-trip check below is what proves
    # the promoted set is exactly this build.
    return Release(is_draft=True, asset_names=frozenset())


def verify_draft_assets(tag: str, local_hashes: dict[str, str], existing: frozenset[str]) -> None:
    """Every asset already in the draft must be byte-identical to this build."""
    with tempfile.TemporaryDirectory(prefix="berth-release-draft-") as tmp:
        gh("release", "download", tag, "--dir", tmp)
        downloaded = {path.name: path for path in Path(tmp).iterdir()}
        for name in sorted(existing):
            path = downloaded.get(name)
            if path is None or path.is_symlink() or not path.is_file():
                raise ValueError("draft asset is not a downloadable file: " + name)
            if sha256(path) != local_hashes[name]:
                raise ValueError("draft asset does not match this build: " + name)


def reconcile_draft(directory: Path, tag: str, files: list[Path],
                    local_hashes: dict[str, str], existing: frozenset[str]) -> None:
    """Verify what the draft already holds, then upload only what is missing.

    An asset is never overwritten or deleted: a name this build did not produce,
    or content that differs from the local input, stops the publication and
    leaves the draft for a person to inspect.
    """
    expected = {path.name for path in files}
    unexpected = sorted(existing - expected)
    if unexpected:
        raise ValueError("draft release holds assets outside this release: " + ", ".join(unexpected))
    if existing:
        verify_draft_assets(tag, local_hashes, existing)
    missing = sorted(expected - existing)
    if missing:
        gh("release", "upload", tag, *map(str, (directory / name for name in missing)))


def verify_download(tag: str, local_hashes: dict[str, str]) -> None:
    """The complete set must survive a fresh download and match the local inputs."""
    with tempfile.TemporaryDirectory(prefix="berth-release-download-") as tmp:
        gh("release", "download", tag, "--dir", tmp)
        for path in verify_assets(Path(tmp), tag, aggregate=True):
            if sha256(path) != local_hashes[path.name]:
                raise ValueError("download does not match local release inputs: " + path.name)


def publish(directory: Path, tag: str) -> None:
    verify_assets(directory, tag)
    (directory / AGGREGATE).write_text("".join(path.read_text() for path in sorted(directory.glob("*.sha256"))))
    files = verify_assets(directory, tag, aggregate=True)
    # Snapshot the verified inputs before network writes. A remote set can be
    # internally consistent yet not be the build this invocation uploaded.
    local_hashes = snapshot(files)
    remote = read_release(tag)
    if remote is None:
        remote = create_draft(tag)
    if not remote.is_draft:
        raise RuntimeError("release " + tag + " is already published; a published release is never modified")
    # The release remains a draft until every asset survives a download/hash check.
    reconcile_draft(directory, tag, files, local_hashes, remote.asset_names)
    verify_download(tag, local_hashes)
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
