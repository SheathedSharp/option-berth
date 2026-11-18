"""Build Linux packages from a fixture release archive and inspect the result."""
import hashlib
import importlib.util
import io
from pathlib import Path
import shutil
import struct
import subprocess
import tarfile
import tempfile
import unittest
from unittest import mock

SCRIPT = Path(__file__).with_name("package_linux.py")
SPEC = importlib.util.spec_from_file_location("package_linux", SCRIPT)
linux = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(linux)
VERSION = (Path(__file__).resolve().parent.parent / "VERSION").read_text().strip()
HAS_NFPM = shutil.which("nfpm")


def make_archive(directory: Path, manifest: str = '{"version": "1.0.0"}\n') -> Path:
    """A fixture release archive; `manifest` identifies which build produced it."""
    stem = f"option-berth-v{VERSION}-linux-amd64"
    stage = directory / "stage" / stem
    (stage / "licenses").mkdir(parents=True, exist_ok=True)
    path = stage / "oberth"
    path.write_bytes(b"fixture oberth\n")
    path.chmod(0o755)
    (stage / "LICENSE").write_text("fixture licence\n")
    (stage / "build-manifest.json").write_text(manifest)
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


@unittest.skipUnless(HAS_NFPM, "nfpm is not installed")
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
        packages = linux.package(self.dist, "amd64", self.output, packagers=("deb",))
        self.assertEqual([path.name for path in packages], [f"option-berth_{VERSION}_amd64.deb"])
        package = packages[0]
        self.assertTrue(package.with_name(package.name + ".sha256").is_file())
        entries = ar_entries(package)
        self.assertEqual(entries["debian-binary"], b"2.0\n")
        control = tar_members(entries["control.tar.gz"])
        self.assertIn(b"Package: option-berth", control["./control"])
        self.assertIn(f"Version: {VERSION}-1".encode(), control["./control"])
        self.assertIn(b"Architecture: amd64", control["./control"])
        self.assertIn(b"Depends: ca-certificates", control["./control"])
        self.assertIn(b"usr/bin/oberth", control["./md5sums"])
        data = tar_members(entries["data.tar.gz"])
        self.assertEqual(data["./usr/bin/oberth"], b"fixture oberth\n")
        self.assertEqual(data["./usr/share/doc/option-berth/LICENSE"], b"fixture licence\n")
        self.assertEqual(data["./usr/share/doc/option-berth/licenses/engine-LICENSE"], b"fixture engine licence\n")
        self.assertIn(b'"version"', data["./usr/share/doc/option-berth/build-manifest.json"])

    @unittest.skipUnless(shutil.which("dpkg-deb"), "dpkg-deb is not installed")
    def test_dpkg_reads_the_package(self):
        package = linux.package(self.dist, "amd64", self.output, packagers=("deb",))[0]
        result = subprocess.run(["dpkg-deb", "--info", str(package)], capture_output=True, text=True, timeout=20)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Package: option-berth", result.stdout)
        fields = subprocess.run(["dpkg-deb", "--field", str(package), "Version"], capture_output=True, text=True, timeout=20)
        self.assertEqual(fields.stdout.strip(), f"{VERSION}-1")

    def test_rpm_contains_the_same_files(self):
        package = linux.package(self.dist, "amd64", self.output, packagers=("rpm",))[0]
        self.assertEqual(package.name, f"option-berth-{VERSION}-1.x86_64.rpm")
        self.assertTrue(package.with_name(package.name + ".sha256").is_file())
        if shutil.which("rpm"):
            listing = subprocess.run(["rpm", "-qpl", str(package)], capture_output=True, text=True, timeout=20)
            self.assertEqual(listing.returncode, 0, listing.stderr)
            self.assertIn("/usr/bin/oberth", listing.stdout)
            self.assertIn("/usr/share/doc/option-berth/licenses/engine-LICENSE", listing.stdout)

    def test_checksum_mismatch_refuses_before_packaging(self):
        self.archive.with_name(self.archive.name + ".sha256").write_text(
            "0" * 64 + "  " + self.archive.name + "\n"
        )
        with self.assertRaisesRegex(ValueError, "SHA256 mismatch"):
            linux.package(self.dist, "amd64", self.output, packagers=("deb",))
        self.assertFalse(self.output.exists())

    def test_missing_archive_is_rejected(self):
        self.archive.unlink()
        with self.assertRaisesRegex(ValueError, "missing release archive"):
            linux.package(self.dist, "amd64", self.output, packagers=("deb",))

    def packaged(self, packagers=("deb", "rpm"), resume=False):
        """Build through nfpm, recording which packagers it was asked for."""
        asked = []
        real_run = subprocess.run

        def run(command, *arguments, **keywords):
            if "--packager" in command:
                asked.append(command[command.index("--packager") + 1])
            return real_run(command, *arguments, **keywords)

        with mock.patch.object(linux.subprocess, "run", side_effect=run):
            packages = linux.package(self.dist, "amd64", self.output, packagers, resume=resume)
        return packages, asked

    def test_resume_reuses_verified_packages_without_running_nfpm(self):
        packages, asked = self.packaged()
        self.assertEqual(asked, ["deb", "rpm"])
        with mock.patch.object(linux.shutil, "which", return_value=None):
            resumed, asked = self.packaged(resume=True)
        self.assertEqual(asked, [], "a resumed run must not need nfpm at all")
        self.assertEqual([path.name for path in resumed], [path.name for path in packages])
        self.assertEqual([path.read_bytes() for path in resumed], [path.read_bytes() for path in packages])

    def test_resume_builds_only_the_package_that_is_missing(self):
        deb, = linux.package(self.dist, "amd64", self.output, ("deb",))
        before = deb.read_bytes()
        resumed, asked = self.packaged(resume=True)
        self.assertEqual([path.name for path in resumed],
                         [deb.name, f"option-berth-{VERSION}-1.x86_64.rpm"])
        self.assertEqual(asked, ["rpm"], "the verified package is not packaged again")
        self.assertEqual(deb.read_bytes(), before)

    def test_resume_refuses_packages_built_from_another_archive(self):
        packages, _ = self.packaged()
        before = [path.read_bytes() for path in packages]
        make_archive(self.dist, manifest='{"version": "9.9.9"}\n')
        with self.assertRaisesRegex(ValueError, "not built from this archive"):
            linux.package(self.dist, "amd64", self.output, ("deb", "rpm"), resume=True)
        self.assertEqual([path.read_bytes() for path in packages], before,
                         "a mismatched package set is left for a person to inspect")

    def test_resume_refuses_a_package_whose_checksum_does_not_match(self):
        deb, = linux.package(self.dist, "amd64", self.output, ("deb",))
        deb.with_name(deb.name + ".sha256").write_text("0" * 64 + "  " + deb.name + "\n")
        with self.assertRaisesRegex(ValueError, "SHA256 mismatch"):
            linux.package(self.dist, "amd64", self.output, ("deb",), resume=True)

    def test_resume_refuses_a_package_without_its_checksum(self):
        deb, = linux.package(self.dist, "amd64", self.output, ("deb",))
        deb.with_name(deb.name + ".sha256").unlink()
        with self.assertRaisesRegex(ValueError, "incomplete package set"):
            linux.package(self.dist, "amd64", self.output, ("deb",), resume=True)


class PackageReaderTests(unittest.TestCase):
    """The readers refuse unfamiliar bytes instead of guessing at them."""

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="berth-linux-reader-test-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)

    def rpm(self, digest: str, algorithms: tuple[int, ...], files: tuple[str, ...] = ("oberth", "build-manifest.json")) -> Path:
        """A minimal rpm carrying one digest per file and its algorithms tag.

        nfpm 2.46 records that tag once per file and 2.48 once in total, so the
        fixture states the shape instead of depending on a local nfpm version.
        """
        store, index = b"", b""

        def entry(tag, kind, value, count):
            nonlocal store, index
            index += struct.pack(">IIII", tag, kind, len(store), count)
            store += value

        entry(1117, 8, b"".join(name.encode() + b"\0" for name in files), len(files))
        entry(1035, 8, b"".join((digest if name == "build-manifest.json" else "0" * len(digest)).encode() + b"\0" for name in files), len(files))
        entry(5011, 4, b"".join(struct.pack(">I", value) for value in algorithms), len(algorithms))
        header = linux.RPM_HEADER + struct.pack(">III", 0, 3, len(store)) + index + store
        path = self.root / "option-berth-1.0.0-1.x86_64.rpm"
        path.write_bytes(linux.RPM_LEAD + bytes(92) + linux.RPM_HEADER + struct.pack(">III", 0, 0, 0) + header)
        return path

    def test_the_algorithm_tag_may_hold_one_value_per_file(self):
        digest = hashlib.sha256(b"fixture manifest\n").hexdigest()
        self.assertEqual(linux.rpm_manifest_digest(self.rpm(digest, (8, 8))), ("sha256", digest))

    def test_the_algorithm_tag_may_hold_a_single_value(self):
        digest = hashlib.sha256(b"fixture manifest\n").hexdigest()
        self.assertEqual(linux.rpm_manifest_digest(self.rpm(digest, (8,))), ("sha256", digest))

    def test_another_digest_algorithm_is_read_with_itself(self):
        digest = hashlib.md5(b"fixture manifest\n").hexdigest()
        self.assertEqual(linux.rpm_manifest_digest(self.rpm(digest, (1,))), ("md5", digest))

    def test_mixed_or_unknown_digest_algorithms_are_refused(self):
        digest = hashlib.sha256(b"fixture manifest\n").hexdigest()
        for algorithms, message in (((8, 2), "one file digest algorithm"), ((11,), "unknown file digest algorithm")):
            with self.subTest(algorithms=algorithms), self.assertRaisesRegex(ValueError, message):
                linux.rpm_manifest_digest(self.rpm(digest, algorithms))

    def test_a_file_that_is_not_a_deb_is_refused(self):
        path = self.root / "option-berth_1.0.0_amd64.deb"
        path.write_bytes(b"not a deb\n")
        with self.assertRaisesRegex(ValueError, "not a .deb archive"):
            linux.deb_manifest_digest(path)

    def test_a_file_that_is_not_an_rpm_is_refused(self):
        path = self.root / "option-berth-1.0.0-1.x86_64.rpm"
        path.write_bytes(b"not an rpm\n")
        with self.assertRaisesRegex(ValueError, "not an rpm package"):
            linux.rpm_manifest_digest(path)

    def test_an_rpm_lead_without_a_signature_header_is_refused(self):
        path = self.root / "truncated.rpm"
        path.write_bytes(linux.RPM_LEAD + bytes(linux.RPM_LEAD_SIZE - len(linux.RPM_LEAD)))
        with self.assertRaisesRegex(ValueError, "malformed rpm signature header"):
            linux.rpm_manifest_digest(path)


if __name__ == "__main__":
    unittest.main()
