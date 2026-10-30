"""The race package manifest is shared by CI and the release gate."""
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("berth_release_race", Path(__file__).with_name("release.py"))
release = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release)


class RacePackagesTests(unittest.TestCase):
    def test_repository_manifest_is_nonempty_unique_and_engine_relative(self):
        packages = release.race_packages(release.ROOT)
        self.assertTrue(packages)
        self.assertEqual(packages, tuple(dict.fromkeys(packages)))
        for package in packages:
            self.assertTrue(package.startswith("./"), package)
            self.assertFalse(any(char.isspace() for char in package), package)

    def test_comments_and_blank_lines_are_ignored(self):
        with tempfile.TemporaryDirectory(prefix="berth-race-") as temporary:
            root = Path(temporary)
            (root / "scripts").mkdir()
            (root / release.RACE_PACKAGES_FILE).write_text("# comment\n\n./a\n./b\n")
            self.assertEqual(release.race_packages(root), ("./a", "./b"))

    def test_verify_uses_the_shared_manifest(self):
        with tempfile.TemporaryDirectory(prefix="berth-race-verify-") as temporary:
            root = Path(temporary)
            (root / "engine").mkdir()
            (root / "scripts").mkdir()
            (root / release.RACE_PACKAGES_FILE).write_text("./a\n./b\n")
            calls = []

            def record(_root, *args, **_kwargs):
                calls.append(args)
                return ""

            with patch.object(release, "check_versions"), patch.object(release, "run", side_effect=record):
                release.verify(root)
            race = [args for args in calls if "-race" in args]
            self.assertEqual(len(race), 1)
            self.assertEqual(race[0][-2:], ("./a", "./b"))


if __name__ == "__main__":
    unittest.main()
