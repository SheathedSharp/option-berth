import hashlib
import importlib.util
from pathlib import Path
import tempfile
import unittest
import shutil
import subprocess
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("berth_publish", Path(__file__).with_name("publish_release.py"))
release = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release)


class PublishAssetsTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.tag = "v1.2.3"
        for name in release.expected_assets(self.tag):
            if not name.endswith(".sha256"):
                (self.root / name).write_bytes(b"synthetic archive fixture")
                digest = hashlib.sha256(b"synthetic archive fixture").hexdigest()
                (self.root / (name + ".sha256")).write_text(digest + "  " + name + "\n")

    def test_complete_assets_verify_without_network(self):
        self.assertEqual(len(release.verify_assets(self.root, self.tag)), 14)
        text = "".join(path.read_text() for path in sorted(self.root.glob("*.sha256")))
        (self.root / "SHA256SUMS").write_text(text)
        self.assertEqual(len(release.verify_assets(self.root, self.tag, True)), 15)

    def test_missing_extra_corrupt_and_path_injection_are_rejected(self):
        archive = next(path for path in self.root.iterdir() if path.name.endswith(".zip"))
        data = archive.read_bytes()
        archive.write_bytes(b"corrupted")
        with self.assertRaisesRegex(ValueError, "checksum"):
            release.verify_assets(self.root, self.tag)
        archive.write_bytes(data)
        extra = self.root / "private-note"
        extra.write_text("must not upload")
        with self.assertRaisesRegex(ValueError, "unexpected"):
            release.verify_assets(self.root, self.tag)
        extra.unlink()
        archive.unlink()
        with self.assertRaisesRegex(ValueError, "incomplete"):
            release.verify_assets(self.root, self.tag)
        for tag in ("../main", "v1.2", "v01.2.3", "v1.2.3;echo"):
            with self.assertRaises(ValueError):
                release.expected_assets(tag)

    def test_roundtrip_must_match_local_assets_not_just_remote_checksums(self):
        self._roundtrip(replace_archive=True)

    def test_publish_occurs_only_after_matching_roundtrip(self):
        self._roundtrip(replace_archive=False)

    def _roundtrip(self, replace_archive):
        published = []
        def fake_gh(*args):
            if args[:2] == ("release", "download"):
                target = Path(args[args.index("--dir") + 1])
                for path in self.root.iterdir():
                    shutil.copyfile(path, target / path.name)
                if replace_archive:
                    archive = next(target.glob("*.zip"))
                    archive.write_bytes(b"different but internally consistent remote build")
                    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
                    (target / (archive.name + ".sha256")).write_text(digest + "  " + archive.name + "\n")
                    (target / "SHA256SUMS").write_text("".join(p.read_text() for p in sorted(target.glob("*.sha256"))))
            if args[:2] == ("release", "edit"):
                published.append(args)
        with patch.object(release, "gh", side_effect=fake_gh), patch.object(release.subprocess, "run", return_value=subprocess.CompletedProcess([], 1)):
            if replace_archive:
                with self.assertRaisesRegex(ValueError, "local"):
                    release.publish(self.root, self.tag)
                self.assertEqual(published, [])
            else:
                release.publish(self.root, self.tag)
                self.assertEqual(len(published), 1)
