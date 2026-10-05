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


def expected_assets(tag: str, macos_trust: str = "adhoc") -> set[str]:
    if macos_trust not in {"adhoc", "notarized"}:
        raise ValueError("invalid macOS trust mode")
    if not re.fullmatch(r"v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)", tag):
        raise ValueError("invalid version tag")
    archives = {f"option-berth-{tag}-{system}-{arch}." + ("zip" if system == "windows" else "tar.gz")
                for system in ("darwin", "linux", "windows") for arch in ("amd64", "arm64")}
    archives.add(f"OptionBerth-{tag}-macos-arm64-{macos_trust}.dmg")
    return archives | {name + ".sha256" for name in archives}


def verify_assets(directory: Path, tag: str, aggregate: bool = False, macos_trust: str = "adhoc") -> list[Path]:
    expected = expected_assets(tag, macos_trust) | ({"SHA256SUMS"} if aggregate else set())
    actual = {path.name for path in directory.iterdir()}
    if actual != expected:
        raise ValueError("incomplete or unexpected release assets")
    for name in sorted(expected_assets(tag, macos_trust)):
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
        expected_sum = "".join((directory / name).read_text() for name in sorted(expected_assets(tag, macos_trust)) if name.endswith(".sha256"))
        if (directory / "SHA256SUMS").read_text() != expected_sum:
            raise ValueError("aggregate checksum mismatch")
    return sorted(directory / name for name in expected)


def gh(*args: str) -> None:
    subprocess.run(["gh", *args, "--repo", REPOSITORY], check=True)


def publish(directory: Path, tag: str, macos_trust: str = "adhoc") -> None:
    verify_assets(directory, tag, macos_trust=macos_trust)
    previous = subprocess.run(["gh", "release", "view", tag, "--repo", REPOSITORY], capture_output=True)
    if previous.returncode == 0:
        raise RuntimeError("release already exists; inspect the existing draft before any manual recovery")
    (directory / "SHA256SUMS").write_text("".join(path.read_text() for path in sorted(directory.glob("*.sha256"))))
    files = verify_assets(directory, tag, aggregate=True, macos_trust=macos_trust)
    # Snapshot the verified inputs before network writes. A remote set can be
    # internally consistent yet not be the build this invocation uploaded.
    local_hashes = {}
    for path in files:
        with path.open("rb") as stream:
            local_hashes[path.name] = hashlib.file_digest(stream, "sha256").hexdigest()
    notes = ("## 使用前 / Before use\n\n"
             "版本按协议 / 功能 / 修复步进。终端与外部 coding agent 使用同一份 worktree 上下文；"
             "agent 需自行安装登录，后续对话与审批留在其原生界面。\n\n"
             "macOS App 内含匹配引擎，但仅 ad-hoc 签名，尚无 Developer ID 或 Apple 公证。"
             "Windows 构建包不等于完整生命周期支持。请先核对 SHA256SUMS，许可随包附带。\n\n"
             "macOS 客户端使用 DMG：将应用拖入 Applications。替换前结束自有会话并退出应用；"
             "这不会替换独立安装的 CLI 或自动重启现有后台。\n\n"
             "Protocol / feature / fix versioning. See the repository release guide.\n\n"
             "Open the DMG and drag the app to Applications. Quit the old app before replacing it; "
             "standalone CLI installations and running daemons are not silently replaced.\n\n"
             "The macOS app includes the matching engine and has an ad-hoc signature only: "
             "it is not Developer ID signed or notarized. Windows ARM64 and some CLI archives are "
             "cross-built; build availability is not a claim of full native lifecycle coverage. "
             "Verify SHA256SUMS before use. Third-party notices are included in every archive.")
    if macos_trust == "notarized":
        notes = notes.replace("macOS App 内含匹配引擎，但仅 ad-hoc 签名，尚无 Developer ID 或 Apple 公证。",
                              "macOS App 内含匹配引擎；notarized 包须由显式 Developer ID/Apple 公证流水线生成。")
        notes = notes.replace("The macOS app includes the matching engine and has an ad-hoc signature only: "
                              "it is not Developer ID signed or notarized.",
                              "The macOS app includes the matching engine; notarized packages must come from the explicit Developer ID/notarization pipeline.")
    # The release remains a draft until every asset survives a download/hash check.
    gh("release", "create", tag, "--verify-tag", "--draft", "--generate-notes", "--title", "option-berth " + tag, "--notes", notes)
    gh("release", "upload", tag, *map(str, files))
    with tempfile.TemporaryDirectory(prefix="berth-release-download-") as tmp:
        gh("release", "download", tag, "--dir", tmp)
        downloaded = verify_assets(Path(tmp), tag, aggregate=True, macos_trust=macos_trust)
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
    parser.add_argument("--macos-trust", choices=("adhoc", "notarized"), default="adhoc")
    args = parser.parse_args()
    try:
        import release
        release.validate_tag(Path(__file__).resolve().parent.parent, args.tag)
        publish(args.assets.resolve(), args.tag, args.macos_trust)
    except (OSError, ValueError, RuntimeError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"publication failed or outcome is unknown; inspect the release before retrying: {error}\n")