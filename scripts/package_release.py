#!/usr/bin/env python3
"""Build release archives with exact build identity, runtime notices and checksums."""
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
    packages = json_stream(command(root / "engine", env, "go", "list", "-deps", "-json", ".", "./cmd/jev-attention"))
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


def package(root: Path, system: str, arch: str, output: Path, include_app: bool = False) -> list[Path]:
    if (system, arch) not in TARGETS:
        raise ValueError("unsupported release target")
    if include_app and (system, arch) != ("darwin", "arm64"):
        raise ValueError("the current native app target is darwin/arm64")
    env = dict(os.environ, GOOS=system, GOARCH=arch, CGO_ENABLED="0", GOMAXPROCS="4")
    version = (root / "VERSION").read_text().strip()
    # Share validation with the release entry point without executing a release.
    import release
    release.check_versions(root)
    commit = command(root, env, "git", "rev-parse", "HEAD")
    stamp = int(command(root, env, "git", "show", "-s", "--format=%ct", "HEAD"))
    built = datetime.datetime.fromtimestamp(stamp, datetime.timezone.utc).isoformat().replace("+00:00", "Z")
    flags = "-s -w " + " ".join("-X github.com/sheathedsharp/option-berth/internal/buildinfo." + key + "=" + value
                                  for key, value in (("Version", "v" + version), ("Commit", commit[:7]), ("Date", built)))
    output.mkdir(parents=True, exist_ok=True)
    stem = f"option-berth-v{version}-{system}-{arch}"
    created = []
    with tempfile.TemporaryDirectory(prefix="berth-package-") as tmp:
        stage = Path(tmp) / stem
        stage.mkdir()
        suffix = ".exe" if system == "windows" else ""
        for name, source in (("oberth", "."), ("jev-attention", "./cmd/jev-attention")):
            command(root / "engine", env, "go", "build", "-trimpath", "-ldflags", flags, "-o", str(stage / (name + suffix)), source)
        shutil.copyfile(root / "LICENSE", stage / "LICENSE")
        notices = stage / "licenses"
        notices.mkdir()
        shutil.copyfile(root / "engine/LICENSE", notices / "engine-LICENSE")
        shutil.copyfile(root / "third_party/go-runtime-licenses.txt", notices / "retained-runtime-notices.txt")
        text, modules = runtime_notices(root, env)
        (notices / "target-runtime-notices.txt").write_text(text)
        manifest = {"project": "option-berth", "version": version, "commit": commit,
                    "target": system + "/" + arch, "protocol": release.protocol(root),
                    "go": command(root, env, "go", "version"), "runtime_modules": modules}
        (stage / "build-manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
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
        if include_app:
            source = root / "client/macos/build/OptionBerth.app"
            if not source.is_dir():
                raise RuntimeError("build the native app explicitly before packaging it")
            with (source / "Contents/Info.plist").open("rb") as stream:
                metadata = plistlib.load(stream)
                if metadata["CFBundleShortVersionString"] != version or metadata.get("BerthBuildCommit") != commit:
                    raise RuntimeError("application version does not match VERSION")
            app = Path(tmp) / "OptionBerth.app"
            shutil.copytree(source, app, symlinks=True)
            shutil.copy2(stage / "oberth", app / "Contents/MacOS/oberth")
            app_notices = app / "Contents/Resources/Licenses"
            shutil.copytree(notices, app_notices, dirs_exist_ok=True)
            shutil.copyfile(root / "LICENSE", app_notices / "option-berth-LICENSE.txt")
            # Ad-hoc signatures support local integrity checks, not Developer ID trust.
            command(root, env, "codesign", "--force", "--sign", "-", str(app / "Contents/MacOS/oberth"))
            command(root, env, "codesign", "--force", "--deep", "--sign", "-", str(app))
            command(root, env, "codesign", "--verify", "--deep", "--strict", str(app))
            app_archive = output / f"OptionBerth-v{version}-macos-arm64-adhoc.zip"
            if app_archive.exists():
                raise FileExistsError("refusing to overwrite an application archive")
            command(root, env, "ditto", "-c", "-k", "--sequesterRsrc", "--keepParent", str(app), str(app_archive))
            checksum_sidecar(app_archive)
            created.append(app_archive)
    return created


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--os", choices=("darwin", "linux", "windows"), required=True)
    parser.add_argument("--arch", choices=("amd64", "arm64"), required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--include-app", action="store_true")
    args = parser.parse_args()
    try:
        for artifact in package(ROOT, args.os, args.arch, args.output.resolve(), args.include_app):
            print(artifact.name)
    except (OSError, ValueError, RuntimeError) as error:
        parser.exit(1, f"packaging refused: {error}\n")
