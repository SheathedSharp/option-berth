#!/usr/bin/env python3
"""Explicit macOS signing gates. No credential import, installation or publishing."""
from __future__ import annotations

from dataclasses import dataclass
import json
from pathlib import Path
import re
import subprocess
import sys
import tempfile
from typing import Callable


@dataclass(frozen=True)
class SigningConfiguration:
    mode: str = "adhoc"
    identity: str | None = None
    team_id: str | None = None
    notary_profile: str | None = None

    def validate(self) -> None:
        if self.mode == "adhoc":
            if self.identity or self.team_id or self.notary_profile:
                raise ValueError("ad-hoc mode refuses unused Developer ID options")
            return
        if self.mode != "developer-id":
            raise ValueError("unsupported macOS signing mode")
        identity = self.identity or ""
        valid_identity = bool(re.fullmatch(r"[0-9A-Fa-f]{40}", identity)) or (
            identity.startswith("Developer ID Application: ") and 26 < len(identity) <= 200
            and not any(ord(char) < 32 for char in identity))
        if not valid_identity or not re.fullmatch(r"[A-Z0-9]{10}", self.team_id or ""):
            raise ValueError("Developer ID requires an explicit certificate identity and Team ID")
        if not re.fullmatch(r"[A-Za-z0-9_.-]{1,128}", self.notary_profile or ""):
            raise ValueError("Developer ID requires an existing notarytool Keychain profile; credentials are never imported")


Runner = Callable[..., str]


def run(*arguments: str, timeout: int = 120, diagnostic: bool = False) -> str:
    """Do not echo subprocess stderr or arguments that may mention account data."""
    try:
        result = subprocess.run(arguments, text=True, capture_output=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        raise RuntimeError(f"{Path(arguments[0]).name} timed out; no trusted package produced") from None
    if result.returncode:
        raise RuntimeError(f"{Path(arguments[0]).name} failed ({result.returncode}); no trusted package produced")
    return result.stderr if diagnostic else result.stdout


def code_files(app: Path) -> list[Path]:
    if app.is_symlink() or not app.is_dir() or not (app / "Contents/Info.plist").is_file():
        raise ValueError("signing requires an explicit staged application bundle")
    resolved = app.resolve()
    result = []
    magic = {b"\xfe\xed\xfa\xce", b"\xce\xfa\xed\xfe", b"\xfe\xed\xfa\xcf", b"\xcf\xfa\xed\xfe",
             b"\xca\xfe\xba\xbe", b"\xbe\xba\xfe\xca", b"\xca\xfe\xba\xbf", b"\xbf\xba\xfe\xca"}
    for path in app.rglob("*"):
        if path.is_symlink():
            if not path.resolve().is_relative_to(resolved):
                raise ValueError("application contains an external symlink")
            continue
        if path.is_file():
            with path.open("rb") as stream:
                if stream.read(4) in magic:
                    result.append(path)
    if not result:
        raise ValueError("application contains no Mach-O code")
    return sorted(result, key=lambda item: (-len(item.parts), str(item)))


def sign_application(app: Path, configuration: SigningConfiguration, runner: Runner = run) -> str:
    """Return the artifact trust suffix only after all corresponding gates pass."""
    configuration.validate()
    if sys.platform != "darwin":
        raise RuntimeError("macOS application signing requires a macOS runner")
    binaries = code_files(app)
    identity = "-" if configuration.mode == "adhoc" else configuration.identity
    extra = [] if configuration.mode == "adhoc" else ["--options", "runtime", "--timestamp"]
    # Sign nested code before the bundle, rather than treating --deep as an
    # implicit signing policy. --deep is used only for verification below.
    for binary in binaries:
        runner("codesign", "--force", "--sign", identity, *extra, str(binary))
    runner("codesign", "--force", "--sign", identity, *extra, str(app))
    runner("codesign", "--verify", "--deep", "--strict", str(app))
    if configuration.mode == "adhoc":
        return "adhoc"
    metadata = runner("codesign", "--display", "--verbose=4", str(app), diagnostic=True)
    lines = set(metadata.splitlines())
    if f"TeamIdentifier={configuration.team_id}" not in lines or not any(
            line.startswith("Authority=Developer ID Application:") for line in lines):
        raise RuntimeError("signed identity does not match the expected Developer ID team")
    with tempfile.TemporaryDirectory(prefix="oberth-notary-") as temporary:
        archive = Path(temporary) / "submission.zip"
        runner("ditto", "-c", "-k", "--sequesterRsrc", "--keepParent", str(app), str(archive))
        output = runner("xcrun", "notarytool", "submit", str(archive), "--keychain-profile", configuration.notary_profile,
                        "--wait", "--timeout", "20m", "--output-format", "json", timeout=25 * 60)
        try:
            response = json.loads(output)
        except (ValueError, TypeError) as error:
            raise RuntimeError("notarytool did not return a valid acceptance result") from error
        if not isinstance(response, dict) or response.get("status") != "Accepted":
            raise RuntimeError("notarization was not accepted; no trusted package produced")
    runner("xcrun", "stapler", "staple", str(app), timeout=180)
    runner("xcrun", "stapler", "validate", str(app))
    runner("codesign", "--verify", "--deep", "--strict", str(app))
    runner("spctl", "--assess", "--type", "execute", "--verbose=2", str(app))
    return "notarized"
