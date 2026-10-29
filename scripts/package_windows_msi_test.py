"""Build a fixture MSI with wixl and check the install/uninstall contract."""
import hashlib
import importlib.util
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
import zipfile

SCRIPT = Path(__file__).with_name("package_windows_msi.py")
SPEC = importlib.util.spec_from_file_location("package_windows_msi", SCRIPT)
msi = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(msi)
VERSION = (Path(__file__).resolve().parent.parent / "VERSION").read_text().strip()
HAS_WIXL = shutil.which("wixl") and shutil.which("msiinfo")


def make_zip(directory: Path) -> Path:
    stem = f"option-berth-v{VERSION}-windows-amd64"
    stage = directory / "stage" / stem
    (stage / "licenses").mkdir(parents=True)
    (stage / "oberth.exe").write_bytes(b"fixture oberth\n")
    (stage / "jev-attention.exe").write_bytes(b"fixture jev\n")
    (stage / "LICENSE").write_text("fixture licence\n")
    (stage / "build-manifest.json").write_text("{}\n")
    (stage / "licenses" / "engine-LICENSE").write_text("fixture engine licence\n")
    archive = directory / f"{stem}.zip"
    with zipfile.ZipFile(archive, "w") as zipped:
        for path in stage.rglob("*"):
            if path.is_file():
                zipped.write(path, f"{stem}/{path.relative_to(stage).as_posix()}")
    archive.with_name(archive.name + ".sha256").write_text(
        hashlib.sha256(archive.read_bytes()).hexdigest() + "  " + archive.name + "\n"
    )
    return archive


@unittest.skipUnless(HAS_WIXL, "wixl/msiinfo are not installed")
class WindowsMsiTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="berth-msi-test-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.dist = self.root / "dist"
        self.dist.mkdir()
        self.output = self.root / "out"
        self.archive = make_zip(self.dist)

    def test_msi_installs_binaries_notices_and_reversible_path_entry(self):
        package = msi.build(self.archive, self.output, VERSION)
        self.assertEqual(package.name, f"option-berth-{VERSION}-windows-amd64.msi")
        self.assertTrue(package.with_name(package.name + ".sha256").is_file())
        files = subprocess.run(["msiinfo", "export", str(package), "File"], capture_output=True, text=True, check=True)
        for name in ("oberth.exe", "jev-attention.exe", "LICENSE", "build-manifest.json", "engine-LICENSE"):
            self.assertIn(name, files.stdout)
        environment = subprocess.run(
            ["msiinfo", "export", str(package), "Environment"], capture_output=True, text=True, check=True
        )
        row = next(line for line in environment.stdout.splitlines() if "PATH" in line)
        self.assertIn("=-PATH", row)
        self.assertIn("[~];[BINDIR]", row)

    def test_checksum_mismatch_refuses_before_building(self):
        self.archive.with_name(self.archive.name + ".sha256").write_text(
            "0" * 64 + "  " + self.archive.name + "\n"
        )
        with self.assertRaisesRegex(ValueError, "SHA256 mismatch"):
            msi.build(self.archive, self.output, VERSION)
        self.assertFalse(self.output.exists())

    def test_missing_binary_is_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = msi.extract(self.archive, Path(temporary))
            (root / "oberth.exe").unlink()
            with self.assertRaisesRegex(ValueError, "oberth.exe"):
                msi.msixml(root, VERSION)


if __name__ == "__main__":
    unittest.main()
