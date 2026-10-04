"""Release mutations run only against local disposable Git repositories."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("berth_release", Path(__file__).with_name("release.py"))
release = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release)


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        environment = patch.dict(os.environ, {"GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1"})
        environment.start()
        self.addCleanup(environment.stop)
        self.tmp = tempfile.TemporaryDirectory(prefix="berth-release-test-")
        self.addCleanup(self.tmp.cleanup)
        base = Path(self.tmp.name)
        self.root = base / "repo"
        self.root.mkdir()
        self.remote = base / "remote.git"
        subprocess.run(["git", "init", "--bare", str(self.remote)], check=True, capture_output=True)
        self.git("init", "-b", "main")
        self.git("config", "user.name", "Fixture")
        self.git("config", "user.email", "fixture@example.invalid")
        (self.root / "VERSION").write_text("1.2.3\n")
        types = self.root / release.PROTOCOL_FILE
        types.parent.mkdir(parents=True)
        types.write_text('package rpc\nconst ProtocolVersion = "2.1.0"\n')
        schema = self.root / release.SCHEMA_FILE
        schema.parent.mkdir(parents=True)
        schema.write_text(json.dumps({"protocol_version": "2.1.0"}))
        self.git("add", ".")
        self.git("commit", "-m", "fixture")
        self.git("remote", "add", "origin", str(self.remote))
        self.git("push", "origin", "main")
        self.head = self.git("rev-parse", "HEAD")
        self.allowed = patch.object(release, "ACCEPTED_REMOTES", {str(self.remote)})
        self.allowed.start()
        self.addCleanup(self.allowed.stop)

    def git(self, *args):
        return subprocess.check_output(["git", *args], cwd=self.root, text=True, stderr=subprocess.DEVNULL).strip()

    def test_all_version_steps_and_protocol_separation(self):
        for kind, version, protocol in (("protocol", "2.0.0", "3.0.0"), ("feature", "1.3.0", "2.1.0"), ("fix", "1.2.4", "2.1.0")):
            result = release.plan(self.root, kind)
            self.assertEqual(result["version"], version)
            self.assertEqual(result["protocol"], protocol)
        self.assertEqual(self.git("status", "--porcelain"), "")
        self.assertEqual(self.git("tag", "--list"), "")
        for invalid in ("01.2.3", "v1.2.3", "1.2", "1.2.3-rc1", "-1.2.3"):
            with self.assertRaises(ValueError):
                release.bump(invalid, "fix")

    def test_verified_fix_creates_only_local_commit_and_annotated_tag(self):
        observed = []
        def verify(candidate):
            observed.append((candidate / "VERSION").read_text())
            release.check_versions(candidate)
        result = release.release(self.root, "fix", False, verify)
        self.assertEqual(observed, ["1.2.4\n"])
        self.assertEqual(result["state"], "tagged-locally")
        self.assertEqual(self.git("cat-file", "-t", "v1.2.4"), "tag")
        self.assertEqual(self.git("rev-parse", "v1.2.4^{}"), self.git("rev-parse", "HEAD"))
        remote = self.git("ls-remote", "origin", "refs/heads/main")
        self.assertTrue(remote.startswith(self.head))
        self.assertEqual(self.git("diff", "--name-only", self.head, "HEAD"), "VERSION")

    def test_atomic_publication_to_fixture_remote(self):
        result = release.release(self.root, "feature", True, release.check_versions)
        self.assertEqual(result["state"], "pushed-awaiting-release-workflow")
        self.assertIn(result["commit"], self.git("ls-remote", "origin", "refs/heads/main"))
        self.assertIn("refs/tags/v1.3.0", self.git("ls-remote", "origin", "refs/tags/v1.3.0"))

    def test_failed_verification_leaves_main_and_tags_untouched(self):
        def fail(_):
            raise RuntimeError("fixture failed")
        with self.assertRaisesRegex(RuntimeError, "fixture failed"):
            release.release(self.root, "fix", False, fail)
        self.assertEqual(self.git("rev-parse", "HEAD"), self.head)
        self.assertEqual(self.git("status", "--porcelain"), "")
        self.assertEqual(self.git("tag", "--list"), "")
        self.assertEqual(len(self.git("worktree", "list").splitlines()), 1)

    def test_concurrent_user_edit_is_preserved(self):
        def edit(_):
            (self.root / "user-note").write_text("keep this")
        with self.assertRaisesRegex(RuntimeError, "clean worktree"):
            release.release(self.root, "fix", False, edit)
        self.assertEqual((self.root / "user-note").read_text(), "keep this")
        self.assertEqual(self.git("rev-parse", "HEAD"), self.head)
        self.assertEqual(self.git("tag", "--list"), "")

    def test_dirty_branch_existing_tag_and_protocol_mismatch_refused(self):
        (self.root / "untracked").write_text("user content")
        with self.assertRaises(RuntimeError):
            release.release(self.root, "fix", False, release.check_versions)
        (self.root / "untracked").unlink()
        self.git("switch", "-c", "feature")
        with self.assertRaisesRegex(RuntimeError, "reviewed main"):
            release.release(self.root, "fix", False, release.check_versions)
        self.git("switch", "main")
        self.git("tag", "v1.2.4")
        with self.assertRaisesRegex(RuntimeError, "already exists"):
            release.release(self.root, "fix", False, release.check_versions)
        (self.root / release.SCHEMA_FILE).write_text('{"protocol_version":"99.0.0"}')
        with self.assertRaises(ValueError):
            release.check_versions(self.root)

    def test_protocol_candidate_updates_only_version_contract_files(self):
        spec = release.plan(self.root, "protocol")
        original = release.run
        def regenerate(root, *args, **kwargs):
            if args == ("go", "generate", "./internal/daemon/rpc"):
                (root.parent / release.SCHEMA_FILE).write_text(json.dumps({"protocol_version": release.protocol(root.parent)}))
                return ""
            return original(root, *args, **kwargs)
        with patch.object(release, "run", side_effect=regenerate):
            result = release.release(self.root, "protocol", False, release.check_versions)
        self.assertEqual(result["version"], "2.0.0")
        self.assertEqual(release.protocol(self.root), "3.0.0")
        release.check_versions(self.root)
        self.assertEqual(set(self.git("diff", "--name-only", self.head, "HEAD").splitlines()), {"VERSION", str(release.PROTOCOL_FILE), str(release.SCHEMA_FILE)})

    def test_publish_failure_retains_local_tag_without_remote_mutation(self):
        hook = self.remote / "hooks/pre-receive"
        hook.write_text("#!/bin/sh\nexit 1\n")
        hook.chmod(0o700)
        with self.assertRaisesRegex(RuntimeError, "retained; atomic push failed"):
            release.release(self.root, "fix", True, release.check_versions)
        self.assertEqual(self.git("cat-file", "-t", "v1.2.4"), "tag")
        self.assertTrue(self.git("ls-remote", "origin", "refs/heads/main").startswith(self.head))
        self.assertEqual(self.git("ls-remote", "origin", "refs/tags/v1.2.4"), "")
