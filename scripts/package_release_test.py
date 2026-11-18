"""A resumed packaging run reuses only the archive it can verify against this build."""
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
import zipfile
from unittest import mock

import package_release

ROOT = package_release.ROOT
VERSION = (ROOT / "VERSION").read_text().strip()
COMMIT = "a" * 40


class FixtureToolchain:
    """Build archive content without a Go toolchain, and record every command.

    The compiler, the module cache and the network stay out of these tests: the
    fixture answers the same commands `package()` issues, so what is being
    exercised is the packaging script's own decisions.
    """

    def __init__(self, module: Path, commit: str = COMMIT) -> None:
        self.module = module
        self.commit = commit
        self.commands: list[tuple[str, ...]] = []

    def __call__(self, root, env, *args):
        self.commands.append(args)
        if args[:3] == ("git", "rev-parse", "HEAD"):
            return self.commit
        if args[:2] == ("git", "show"):
            return "1700000000"
        if args[:2] == ("go", "build"):
            Path(args[args.index("-o") + 1]).write_bytes(b"fixture binary\n")
            return ""
        if args[:2] == ("go", "list"):
            return json.dumps({"Module": {"Path": "example.invalid/fixture", "Version": "v1.0.0", "Dir": str(self.module)}})
        if args == ("go", "version"):
            return "fixture toolchain"
        raise AssertionError("unexpected external command: " + " ".join(args))

    @property
    def builds(self) -> list[tuple[str, ...]]:
        return [command for command in self.commands if command[:2] == ("go", "build")]


class ResumeTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="berth-package-resume-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.output = self.root / "dist"
        self.module = self.root / "fixture-module"
        self.module.mkdir()
        (self.module / "LICENSE").write_text("Fixture copyright\n")

    def build(self, system="linux", arch="amd64", commit=COMMIT, resume=False):
        tools = FixtureToolchain(self.module, commit)
        with mock.patch.object(package_release, "command", tools):
            artifacts = package_release.package(ROOT, system, arch, self.output, resume=resume)
        return artifacts, tools

    def seeded(self, system="linux", arch="amd64"):
        """One archive built the way a first attempt builds it."""
        artifacts, _ = self.build(system, arch)
        return artifacts[0]

    def resume(self, system="linux", arch="amd64", commit=COMMIT):
        with mock.patch.object(package_release, "command", FixtureToolchain(self.module, commit)):
            return package_release.package(ROOT, system, arch, self.output, resume=True)

    def test_resume_reuses_a_verified_archive_instead_of_rebuilding_it(self):
        archive = self.seeded()
        before = archive.read_bytes()
        resumed, = self.resume()
        self.assertEqual(resumed, archive)
        self.assertEqual(resumed.read_bytes(), before)

    def test_resume_reads_only_the_build_identity_it_verifies(self):
        self.seeded()
        tools = FixtureToolchain(self.module)
        with mock.patch.object(package_release, "command", tools):
            package_release.package(ROOT, "linux", "amd64", self.output, resume=True)
        self.assertEqual(tools.builds, [], "a verified archive is never rebuilt")
        self.assertEqual(tools.commands, [("git", "rev-parse", "HEAD")],
                         "resume must not reach for a compiler, a module list or a timestamp")

    def test_resume_builds_when_the_output_holds_no_archive_for_the_target(self):
        artifacts, tools = self.build(resume=True)
        self.assertEqual([path.name for path in artifacts], [f"option-berth-v{VERSION}-linux-amd64.tar.gz"])
        self.assertEqual(len(tools.builds), 1)

    def test_resume_verifies_windows_archives_too(self):
        archive = self.seeded("windows", "arm64")
        with zipfile.ZipFile(archive) as source:
            self.assertIn("build-manifest.json", "".join(source.namelist()))
        resumed, = self.resume("windows", "arm64")
        self.assertEqual(resumed.read_bytes(), archive.read_bytes())

    def test_resume_refuses_an_archive_built_from_another_commit(self):
        archive = self.seeded()
        before = archive.read_bytes()
        with self.assertRaisesRegex(ValueError, "commit"):
            self.resume(commit="b" * 40)
        self.assertEqual(archive.read_bytes(), before, "a mismatched archive is never overwritten")

    def test_resume_refuses_an_archive_whose_checksum_does_not_match(self):
        archive = self.seeded()
        sidecar = archive.with_name(archive.name + ".sha256")
        sidecar.write_text("0" * 64 + "  " + archive.name + "\n")
        with self.assertRaisesRegex(ValueError, "SHA256 mismatch"):
            self.resume()
        self.assertTrue(archive.is_file(), "a failed resume leaves the set for a person to inspect")

    def test_resume_refuses_an_archive_without_its_checksum(self):
        archive = self.seeded()
        archive.with_name(archive.name + ".sha256").unlink()
        with self.assertRaisesRegex(ValueError, "incomplete release set"):
            self.resume()

    def test_resume_refuses_an_archive_without_a_build_manifest(self):
        stem = f"option-berth-v{VERSION}-linux-amd64"
        archive = self.output / (stem + ".tar.gz")
        self.output.mkdir(parents=True)
        stage = self.root / "stage" / stem
        stage.mkdir(parents=True)
        (stage / "oberth").write_bytes(b"unidentified\n")
        with tarfile.open(archive, "w:gz") as target:
            target.add(stage, arcname=stem)
        sidecar = archive.with_name(archive.name + ".sha256")
        sidecar.write_text(package_release.checksum(archive) + "  " + archive.name + "\n")
        with self.assertRaisesRegex(ValueError, "build-manifest.json"):
            self.resume()

    def test_packaging_without_resume_still_refuses_to_overwrite(self):
        self.seeded()
        with self.assertRaises(FileExistsError):
            self.build()


if __name__ == "__main__":
    unittest.main()
