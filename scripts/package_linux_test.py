"""Build Linux packages from a fixture release archive and inspect the result."""
import hashlib
import importlib.util
import io
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("package_linux.py")
SPEC = importlib.util.spec_from_file_location("package_linux", SCRIPT)
linux = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(linux)
VERSION = (Path(__file__).resolve().parent.parent / "VERSION").read_text().strip()


def make_archive(directory: Path) -> Path:
    stem = f"option-berth-v{VERSION}-linux-amd64"
    stage = directory / "stage" / stem
    (stage / "licenses").mkdir(parents=True)
    for name in ("oberth", "jev-attention"):
        path = stage / name
        path.write_bytes(f"fixture {name}\n".encode())
        path.chmod(0o755)
    (stage / "LICENSE").write_text("fixture licence\n")
    (stage / "build-manifest.json").write_text('{"version": "1.0.0"}\n')
    (stage / "licenses" / "engine-LICENSE").write_text("fixture engine licence\n")
    archive = directory / f"{stem}.tar.gz"
    with tarfile.open(archive, "w:gz") as tar:
        tar.add(stage, arcname=stem)
    archive.with_name(archive.name + ".sha256").write_text(
        hashlib.sha256(archive.read_bytes()).hexdigest() + "  " + archive.name + "\n"
    )
    return archive


def ar_entries(path: Path) -> dict[str, bytes]:
    data = path.read_bytes()
    if not data.startswith(b"!<arch>\n"):
        raise AssertionError("not an ar archive")
    entries = {}
    offset = 8
    while offset < len(data):
        header = data[offset:offset + 60]
        if len(header) != 60:
            raise AssertionError("truncated ar header")
        name = header[:16].decode("ascii").strip()
        size = int(header[48:58].decode("ascii").strip())
        offset += 60
        entries[name] = data[offset:offset + size]
        offset += size + (size % 2)
    return entries


def tar_members(data: bytes) -> dict[str, bytes]:
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as tar:
        return {member.name: tar.extractfile(member).read() for member in tar if member.isfile()}


class LinuxPackageTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="berth-linux-package-test-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.dist = self.root / "dist"
        self.dist.mkdir()
        self.output = self.root / "out"
        self.archive = make_archive(self.dist)

    def test_deb_contains_binaries_notices_and_control_metadata(self):
        packages = linux.package(self.dist, "amd64", self.output, deb_only=True)
        self.assertEqual([path.name for path in packages], [f"option-berth_{VERSION}_amd64.deb"])
        package = packages[0]
        self.assertTrue(package.with_name(package.name + ".sha256").is_file())
        entries = ar_entries(package)
        self.assertEqual(entries["debian-binary"], b"2.0\n")
        control = tar_members(entries["control.tar.gz"])
        self.assertIn(b"Package: option-berth", control["./control"])
        self.assertIn(f"Version: {VERSION}".encode(), control["./control"])
        self.assertIn(b"Architecture: amd64", control["./control"])
        self.assertIn(b"Depends: ca-certificates", control["./control"])
        self.assertIn(b"usr/bin/oberth", control["./md5sums"])
        data = tar_members(entries["data.tar.gz"])
        self.assertEqual(data["./usr/bin/oberth"], b"fixture oberth\n")
        self.assertEqual(data["./usr/bin/jev-attention"], b"fixture jev-attention\n")
        self.assertEqual(data["./usr/share/doc/option-berth/LICENSE"], b"fixture licence\n")
        self.assertEqual(data["./usr/share/doc/option-berth/licenses/engine-LICENSE"], b"fixture engine licence\n")
        self.assertIn(b'"version"', data["./usr/share/doc/option-berth/build-manifest.json"])

    @unittest.skipUnless(shutil.which("dpkg-deb"), "dpkg-deb is not installed")
    def test_dpkg_reads_the_package(self):
        package = linux.package(self.dist, "amd64", self.output, deb_only=True)[0]
        result = subprocess.run(["dpkg-deb", "--info", str(package)], capture_output=True, text=True, timeout=20)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Package: option-berth", result.stdout)
        fields = subprocess.run(["dpkg-deb", "--field", str(package), "Version"], capture_output=True, text=True, timeout=20)
        self.assertEqual(fields.stdout.strip(), VERSION)

    def test_checksum_mismatch_refuses_before_packaging(self):
        self.archive.with_name(self.archive.name + ".sha256").write_text(
            "0" * 64 + "  " + self.archive.name + "\n"
        )
        with self.assertRaisesRegex(ValueError, "SHA256 mismatch"):
            linux.package(self.dist, "amd64", self.output, deb_only=True)
        self.assertFalse(self.output.exists())

    def test_missing_archive_is_rejected(self):
        self.archive.unlink()
        with self.assertRaisesRegex(ValueError, "missing release archive"):
            linux.package(self.dist, "amd64", self.output, deb_only=True)

    def test_rpm_spec_uses_the_release_files(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for name in ("oberth", "jev-attention", "LICENSE", "build-manifest.json"):
                (root / name).write_text("x")
            (root / "licenses").mkdir()
            (root / "licenses" / "engine-LICENSE").write_text("x")
            spec = linux.spec_text(VERSION, "x86_64", root)
        self.assertIn(f"Version: {VERSION}", spec)
        self.assertIn("BuildArch: x86_64", spec)
        self.assertIn("/usr/bin/oberth", spec)
        self.assertIn("/usr/share/doc/option-berth/licenses/*", spec)
        self.assertIn("Requires: ca-certificates", spec)

    @unittest.skipUnless(shutil.which("rpmbuild") and shutil.which("rpm"), "rpmbuild/rpm are not installed")
    def test_rpm_builds_and_lists_the_same_files(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = linux.extract(self.archive, Path(temporary))
            package = linux.build_rpm(root, VERSION, "amd64", self.output)
        listing = subprocess.run(["rpm", "-qpl", str(package)], capture_output=True, text=True, timeout=20)
        self.assertEqual(listing.returncode, 0, listing.stderr)
        self.assertIn("/usr/bin/oberth", listing.stdout)
        self.assertIn("/usr/share/doc/option-berth/licenses/engine-LICENSE", listing.stdout)


if __name__ == "__main__":
    unittest.main()
