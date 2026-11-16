"""Keep the project license, the third-party notices and their delivery path in agreement."""
from pathlib import Path
import unittest
import json
import tarfile
import tempfile
import zipfile
from unittest import mock

import package_release

ROOT = Path(__file__).resolve().parent.parent

MIT_GRANT = "The above copyright notice and this permission notice"
MIT_DISCLAIMER = 'THE SOFTWARE IS PROVIDED "AS IS"'


class LicenseTests(unittest.TestCase):
    def test_project_licenses_list_only_project_copyright(self):
        for name in ("LICENSE", "engine/LICENSE"):
            text = (ROOT / name).read_text()
            self.assertIn("Copyright (c) 2026 SheathedSharp", text)
            self.assertIn("third_party/README.md", text)
            self.assertIn(MIT_GRANT, text)
            self.assertIn(MIT_DISCLAIMER, text)

    def test_third_party_notices_cover_the_shipped_assets(self):
        self.assertFalse((ROOT / "THIRD_PARTY_NOTICES.md").exists())
        text = (ROOT / "third_party/README.md").read_text()
        for section in ("GSAP", "SIL Open Font License", "Go 依赖", "https://gsap.com/standard-license"):
            self.assertIn(section, text)

    def test_release_archives_ship_the_notice(self):
        # Run the real staging/archive path, replacing only external commands.
        # No compiler, installed CLI, service, network or release is executed.
        for system in ("darwin", "windows"):
            with self.subTest(system=system), tempfile.TemporaryDirectory() as tmp:
                output = Path(tmp) / "output"
                module = Path(tmp) / "fixture-module"
                module.mkdir()
                original = "Fixture copyright\n" + MIT_GRANT + "\n" + MIT_DISCLAIMER + "\n"
                (module / "LICENSE").write_text(original)

                def command(root, env, *args):
                    if args[:3] == ("git", "rev-parse", "HEAD"):
                        return "a" * 40
                    if args[:2] == ("git", "show"):
                        return "1700000000"
                    if args[:2] == ("go", "build"):
                        Path(args[args.index("-o") + 1]).write_bytes(b"test fixture, not executable")
                        return ""
                    if args[:2] == ("go", "list"):
                        return json.dumps({"Module": {"Path": "example.invalid/fixture", "Version": "v1.0.0", "Dir": str(module)}})
                    if args == ("go", "version"):
                        return "fixture toolchain"
                    self.fail(f"unexpected external command: {args[0]}")

                with mock.patch.object(package_release, "command", side_effect=command):
                    archive, = package_release.package(ROOT, system, "arm64", output)
                if system == "windows":
                    with zipfile.ZipFile(archive) as source:
                        files = {name.split("/", 1)[1]: source.read(name) for name in source.namelist()}
                else:
                    with tarfile.open(archive) as source:
                        files = {m.name.split("/", 1)[1]: source.extractfile(m).read() for m in source.getmembers() if m.isfile()}
                guide = (ROOT / "third_party/README.md").read_text()
                notice = files["licenses/third-party-notices.md"].decode()
                self.assertEqual(notice, guide.split("\n## 源码资料\n", 1)[0] + "\n")
                self.assertNotIn("## 固定版本与来源", notice)
                self.assertIn("GSAP", notice)
                self.assertNotIn("Sonar", notice)
                self.assertEqual(files["licenses/engine-LICENSE"], (ROOT / "engine/LICENSE").read_bytes())
                self.assertEqual(files["licenses/retained-runtime-notices.txt"], (ROOT / "third_party/go-runtime-licenses.txt").read_bytes())
                self.assertIn(original, files["licenses/target-runtime-notices.txt"].decode())
                checksums = files["SHA256SUMS"].decode()
                for name in ("licenses/third-party-notices.md", "licenses/target-runtime-notices.txt", "licenses/retained-runtime-notices.txt"):
                    self.assertIn(package_release.hashlib.sha256(files[name]).hexdigest() + "  " + name + "\n", checksums)
