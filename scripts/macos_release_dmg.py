#!/usr/bin/env python3
"""Build and verify an installable DMG without installing or replacing any app."""
from __future__ import annotations

import json
import os
from pathlib import Path
import plistlib
import re
import sys
import tempfile

from macos_release_signing import SigningConfiguration, Runner, code_files, run


def sign_disk_image(image: Path, configuration: SigningConfiguration, runner: Runner = run) -> str:
    """The outer container has its own gates; an accepted app is not an accepted DMG."""
    configuration.validate()
    identity = "-" if configuration.mode == "adhoc" else configuration.identity
    extra = [] if configuration.mode == "adhoc" else ["--timestamp"]
    runner("codesign", "--force", "--sign", identity, *extra, str(image))
    runner("codesign", "--verify", "--strict", str(image))
    if configuration.mode == "adhoc":
        return "adhoc"
    metadata = runner("codesign", "--display", "--verbose=4", str(image), diagnostic=True)
    lines = set(metadata.splitlines())
    if f"TeamIdentifier={configuration.team_id}" not in lines or not any(
            line.startswith("Authority=Developer ID Application:") for line in lines):
        raise RuntimeError("disk image signing team does not match the requested Developer ID")
    output = runner("xcrun", "notarytool", "submit", str(image), "--keychain-profile", configuration.notary_profile,
                    "--wait", "--timeout", "20m", "--output-format", "json", timeout=25 * 60)
    try:
        response = json.loads(output)
    except (ValueError, TypeError) as error:
        raise RuntimeError("disk image notarization returned invalid metadata") from error
    if not isinstance(response, dict) or response.get("status") != "Accepted":
        raise RuntimeError("disk image notarization was not accepted")
    runner("xcrun", "stapler", "staple", str(image), timeout=180)
    runner("xcrun", "stapler", "validate", str(image))
    runner("codesign", "--verify", "--strict", str(image))
    runner("spctl", "--assess", "--type", "open", "--context", "context:primary-signature", "--verbose=2", str(image))
    return "notarized"


def app_identity(app: Path) -> tuple[str, str]:
    if app.name != "OptionBerth.app":
        raise ValueError("the disk image requires OptionBerth.app")
    code_files(app)  # Reject missing code and symlinks escaping the bundle.
    with (app / "Contents/Info.plist").open("rb") as stream:
        metadata = plistlib.load(stream)
    version, commit = metadata.get("CFBundleShortVersionString", ""), metadata.get("BerthBuildCommit", "")
    if not isinstance(version, str) or not re.fullmatch(r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)", version):
        raise ValueError("application version is not a stable release")
    if not isinstance(commit, str) or not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise ValueError("application build identity is missing")
    engine = app / "Contents/MacOS/oberth"
    if engine.is_symlink() or not engine.is_file() or not os.access(engine, os.X_OK):
        raise ValueError("the application must contain its executable engine")
    for notice in ("Licenses/option-berth-LICENSE.txt", "Licenses/target-runtime-notices.txt", "Fonts/LICENSE-Monaspace.txt"):
        if not (app / "Contents/Resources" / notice).is_file():
            raise ValueError("the application is missing a required license notice")
    return version, commit


def verify_disk_image(image: Path, scratch: Path, expected: tuple[str, str], runner: Runner = run) -> None:
    """Mount read-only, copy to a private installation fixture, then verify that copy."""
    runner("hdiutil", "verify", str(image))
    mount = scratch / "mount"
    mount.mkdir()
    attached = False
    try:
        output = runner("hdiutil", "attach", "-readonly", "-nobrowse", "-noautoopen", "-mountpoint", str(mount), "-plist", str(image))
        attached = True
        try:
            document = plistlib.loads(output.encode("utf-8"))
        except (ValueError, TypeError, plistlib.InvalidFileException) as error:
            raise RuntimeError("hdiutil did not identify the mounted image") from error
        entities = document.get("system-entities", []) if isinstance(document, dict) else []
        if not any(isinstance(item, dict) and item.get("mount-point") == str(mount) for item in entities):
            raise RuntimeError("hdiutil mounted outside the requested private directory")
        link = mount / "Applications"
        if not link.is_symlink() or os.readlink(link) != "/Applications":
            raise RuntimeError("disk image is missing the Applications drop target")
        mounted_app = mount / "OptionBerth.app"
        if app_identity(mounted_app) != expected:
            raise RuntimeError("mounted application identity changed")
        installed = scratch / "copied" / "OptionBerth.app"
        installed.parent.mkdir()
        runner("ditto", str(mounted_app), str(installed))
        if app_identity(installed) != expected:
            raise RuntimeError("copied application identity changed")
        runner("codesign", "--verify", "--deep", "--strict", str(installed))
        home = scratch / "home"
        home.mkdir()
        data = runner("/usr/bin/env", f"HOME={home}", f"CFFIXED_USER_HOME={home}", f"BERTH_HOME={home / 'state'}",
                      "BERTH_NO_AUTOSTART=1", str(installed / "Contents/MacOS/oberth"), "version", "--json")
        try:
            engine = json.loads(data)
        except (ValueError, TypeError) as error:
            raise RuntimeError("copied engine did not report a valid build identity") from error
        version, commit = expected
        if not isinstance(engine, dict) or engine.get("version") != "v" + version or engine.get("commit") != commit[:7] or engine.get("platform") != "darwin/arm64":
            raise RuntimeError("copied engine does not match the application version, commit and target")
    finally:
        # A timed-out attach can already have mounted the volume. Never force a
        # device selected from an unrelated mount list; this directory is ours.
        if attached or os.path.ismount(mount):
            try:
                runner("hdiutil", "detach", str(mount), timeout=30)
            except (OSError, RuntimeError, TimeoutError):
                runner("hdiutil", "detach", "-force", str(mount), timeout=30)


def create_disk_image(app: Path, destination: Path, configuration: SigningConfiguration | None = None,
                      runner: Runner = run) -> str:
    """Publish the final pathname only after all gates and owned-mount cleanup pass."""
    configuration = configuration or SigningConfiguration()
    configuration.validate()
    if sys.platform != "darwin":
        raise RuntimeError("DMG creation requires a macOS runner")
    expected = app_identity(app)
    if destination.suffix != ".dmg" or os.path.lexists(destination):
        raise ValueError("DMG destination must be new; existing assets are immutable")
    destination.parent.mkdir(parents=True, exist_ok=True)
    # Same filesystem permits an atomic no-clobber hard link for the final name.
    with tempfile.TemporaryDirectory(prefix=".oberth-dmg-", dir=destination.parent) as temporary:
        scratch = Path(temporary)
        stage = scratch / "volume"
        stage.mkdir()
        runner("ditto", str(app), str(stage / "OptionBerth.app"))
        (stage / "Applications").symlink_to("/Applications")
        image = scratch / "candidate.dmg"
        runner("hdiutil", "create", "-volname", "OptionBerth", "-srcfolder", str(stage), "-format", "UDZO", "-fs", "HFS+", str(image))
        trust = sign_disk_image(image, configuration, runner)
        verify_disk_image(image, scratch, expected, runner)
        os.link(image, destination)  # Fails rather than replacing a concurrent file/symlink.
    return trust
