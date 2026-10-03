"""Provenance reports must fail closed when Git cannot establish ancestry."""
import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("engine_origin", Path(__file__).with_name("audit-engine-origin.py"))
origin = importlib.util.module_from_spec(spec)
spec.loader.exec_module(origin)


class OriginTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / "repo"
        self.root.mkdir()
        self.git("init", "-q")
        self.git("config", "user.name", "origin test")
        self.git("config", "user.email", "origin-test@example.invalid")
        self.file = self.root / "engine" / "main.go"
        self.file.parent.mkdir()
        self.file.write_text("package main\n")
        self.commit()
        self.fork = self.git("rev-parse", "HEAD")
        self.patch = patch.object(origin, "FORK_COMMIT", self.fork)
        self.patch.start()
        self.addCleanup(self.patch.stop)

    def git(self, *args):
        return subprocess.run(["git", *args], cwd=self.root, check=True,
                              text=True, capture_output=True).stdout.strip()

    def commit(self):
        self.git("add", "engine")
        self.git("commit", "-qm", "fixture")

    def test_complete_history_distinguishes_both_origins(self):
        self.file.write_text("package main\nfunc main() {}\n")
        self.commit()
        origin.validate_history(self.root)
        cache = {}
        self.assertTrue(origin.historical_commit(self.root, self.fork, cache))
        self.assertFalse(origin.historical_commit(self.root, self.git("rev-parse", "HEAD"), cache))
        self.assertEqual(origin.line_origins(self.root, self.file, cache), (1, 1))

    def test_missing_fork_cannot_look_like_a_rewrite(self):
        with patch.object(origin, "FORK_COMMIT", "f" * 40):
            with self.assertRaises(RuntimeError):
                origin.validate_history(self.root)
            with self.assertRaises(RuntimeError):
                origin.historical_commit(self.root, self.fork, {})

    def test_invalid_commit_is_not_post_fork(self):
        with self.assertRaises(RuntimeError):
            origin.historical_commit(self.root, "e" * 40, {})

    def test_uncommitted_blame_is_not_project_origin(self):
        self.file.write_text("package main\nfunc main() {}\n")
        with self.assertRaises(RuntimeError):
            origin.validate_history(self.root)
        with self.assertRaises(RuntimeError):
            origin.line_origins(self.root, self.file, {})

    def test_untracked_file_has_unknown_origin(self):
        path = self.file.with_name("untracked.go")
        path.write_text("package main\n")
        with self.assertRaises(RuntimeError):
            origin.first_commit(self.root, path)

    def test_creation_is_found_after_rename_and_edits(self):
        original = self.git("rev-parse", "HEAD")
        path = self.file.with_name("renamed.go")
        self.git("mv", str(self.file), str(path))
        self.commit()
        path.write_text("package main\nfunc main() {}\n")
        self.commit()
        self.assertEqual(origin.first_commit(self.root, path), original)

    def test_shallow_checkout_is_rejected(self):
        clone = Path(self.temp.name) / "shallow"
        subprocess.run(["git", "clone", "-q", "--depth=1", self.root.as_uri(), str(clone)], check=True)
        with self.assertRaises(RuntimeError):
            origin.validate_history(clone)


if __name__ == "__main__":
    unittest.main()
