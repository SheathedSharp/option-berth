#!/usr/bin/env python3
"""Build a per-user Windows MSI from the released Windows amd64 archive.

The MSI installs into %LOCALAPPDATA%\\option-berth, appends its bin directory to
the user PATH, and removes both on uninstall. User state in %USERPROFILE%\\.option-berth
is not touched. The archive is the source of truth and must have its .sha256 sidecar.

wixl (msitools) does not support arm64 in the version used by CI; Windows arm64
keeps the zip + install.ps1 path instead of a misleading cross-architecture MSI.
"""
from __future__ import annotations

import argparse
import hashlib
from pathlib import Path
import shutil
import subprocess
import tempfile
from xml.sax.saxutils import escape, quoteattr

ROOT = Path(__file__).resolve().parent.parent
UPGRADE_CODE = "2222ABC8-68C6-4E44-8B8A-6B964F6E9044"
COMPONENT_GUIDS = {
    "Oberth": "F5329347-80B3-4675-9AFC-F17D60016111",
    "Jev": "00BA2482-A984-4C7F-B6C6-1E56F01186D6",
    "Notices": "58A79179-4A21-4E61-8D4B-5ADA001A68A5",
}


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
    import zipfile

    root = destination.resolve()
    with zipfile.ZipFile(archive) as zipped:
        for info in zipped.infolist():
            target = (destination / info.filename).resolve()
            if target != root and root not in target.parents:
                raise ValueError(f"archive member escapes destination: {info.filename}")
        zipped.extractall(destination)
    children = [path for path in destination.iterdir() if path.is_dir()]
    if len(children) != 1:
        raise ValueError("archive must contain exactly one top-level directory")
    return children[0]


def file_element(identifier: str, path: Path, key_path: bool = False) -> str:
    key = ' KeyPath="yes"' if key_path else ""
    return f'            <File Id="{identifier}" Source={quoteattr(str(path))}{key}/>'


def msixml(root: Path, version: str, permanent: str = "yes") -> str:
    oberth = root / "oberth.exe"
    jev = root / "jev-attention.exe"
    license_file = root / "LICENSE"
    manifest = root / "build-manifest.json"
    for path in (oberth, jev, license_file, manifest):
        if not path.is_file():
            raise ValueError(f"archive is missing {path.name}")
    notices = sorted(path for path in (root / "licenses").glob("*") if path.is_file())
    notice_files = "\n".join(file_element(f"Notice{index}", path) for index, path in enumerate(notices, 1))
    return f"""<?xml version="1.0" encoding="utf-8"?>
<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Product Id="*" Name="option-berth" Language="1033" Version="{escape(version)}" Manufacturer="SheathedSharp" UpgradeCode="{UPGRADE_CODE}">
    <Package InstallerVersion="200" Compressed="yes" InstallScope="perUser"/>
    <MediaTemplate EmbedCab="yes"/>
    <MajorUpgrade DowngradeErrorMessage="A newer version of option-berth is already installed."/>
    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="LocalAppDataFolder">
        <Directory Id="INSTALLDIR" Name="option-berth">
          <Directory Id="BINDIR" Name="bin">
            <Component Id="Oberth" Guid="{COMPONENT_GUIDS['Oberth']}">
{file_element('FileOberth', oberth, True)}
              <!-- wixl versions disagree on how Permanent maps to the "=-" prefix,
                   so build() retries the other value and verifies the Environment table.
                   "=-PATH" means set on install and remove only that entry on uninstall. -->
              <Environment Id="Path" Name="PATH" Value="[BINDIR]" Part="last" Action="set" System="no" Permanent="{permanent}"/>
            </Component>
            <Component Id="Jev" Guid="{COMPONENT_GUIDS['Jev']}">
{file_element('FileJev', jev, True)}
            </Component>
          </Directory>
          <Directory Id="NOTICESDIR" Name="licenses">
            <Component Id="Notices" Guid="{COMPONENT_GUIDS['Notices']}">
{file_element('FileLicense', license_file, True)}
{file_element('FileManifest', manifest)}
{notice_files}
            </Component>
          </Directory>
        </Directory>
      </Directory>
    </Directory>
    <Feature Id="DefaultFeature" Title="option-berth" Level="1">
      <ComponentRef Id="Oberth"/>
      <ComponentRef Id="Jev"/>
      <ComponentRef Id="Notices"/>
    </Feature>
  </Product>
</Wix>
"""


def verify_msi(path: Path) -> None:
    msiinfo = shutil.which("msiinfo")
    if not msiinfo:
        return
    files = subprocess.run([msiinfo, "export", str(path), "File"], capture_output=True, text=True, timeout=20)
    if files.returncode:
        raise RuntimeError("msiinfo could not read the File table")
    for name in ("oberth.exe", "jev-attention.exe", "LICENSE", "build-manifest.json"):
        if name not in files.stdout:
            raise RuntimeError(f"MSI File table is missing {name}")
    environment = subprocess.run([msiinfo, "export", str(path), "Environment"], capture_output=True, text=True, timeout=20)
    if environment.returncode:
        raise RuntimeError("msiinfo could not read the Environment table")
    rows = [line for line in environment.stdout.splitlines() if "\t" in line and "PATH" in line]
    if not any("\t=-PATH\t" in row and "[~];[BINDIR]" in row for row in rows):
        raise RuntimeError("MSI PATH entry is not set-on-install/remove-on-uninstall")


def build(archive: Path, output: Path, version: str) -> Path:
    wixl = shutil.which("wixl")
    if not wixl:
        raise RuntimeError("wixl (msitools) is required to build the MSI")
    verify_archive(archive)
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="berth-msi-") as temporary:
        root = extract(archive, Path(temporary))
        source = Path(temporary) / "option-berth.wxs"
        package = output / f"option-berth-{version}-windows-amd64.msi"
        last_error = None
        for permanent in ("yes", "no"):
            source.write_text(msixml(root, version, permanent))
            package.unlink(missing_ok=True)
            result = subprocess.run([wixl, "-a", "x64", "-o", str(package), str(source)], capture_output=True, text=True)
            if result.returncode:
                raise RuntimeError(f"wixl failed: {result.stderr.strip()}")
            try:
                verify_msi(package)
                break
            except RuntimeError as error:
                last_error = error
        else:
            raise RuntimeError(f"could not produce a reversible PATH entry: {last_error}")
    package.with_name(package.name + ".sha256").write_text(digest(package) + "  " + package.name + "\n")
    return package


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dist", type=Path, default=ROOT / "dist")
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    version = (ROOT / "VERSION").read_text().strip()
    archive = args.dist / f"option-berth-v{version}-windows-amd64.zip"
    if not archive.is_file():
        raise SystemExit(f"missing release archive: {archive.name}")
    package = build(archive, (args.output or args.dist).resolve(), version)
    print(package.name)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
