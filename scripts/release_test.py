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

    def test_macos_release_checks_real_agent_planner_when_gui_exists(self):
        (self.root / "client/macos/AgentTests").mkdir(parents=True)
        calls = []
        def record(root, *args, **kwargs):
            calls.append((root, args, kwargs))
            return ""
        with patch.object(release.sys, "platform", "darwin"), patch.object(release, "run", side_effect=record):
            release.verify(self.root)
        agent = [call for call in calls if "AgentChecks" in call[1]]
        self.assertEqual(len(agent), 1)
        binary = agent[0][2]["environment"]["BERTH_AGENT_TEST_BINARY"]
        self.assertTrue(Path(binary).is_absolute())
        builds = [call for call in calls if call[1] == ("go", "build", "-o", binary, ".")]
        self.assertEqual(len(builds), 1)
        self.assertEqual(builds[0][0], self.root / "engine")
        self.assertLess(calls.index(builds[0]), calls.index(agent[0]))

    def test_release_workflow_pins_event_sha_and_keeps_full_tag_objects(self):
        workflow = (release.ROOT / ".github/workflows/release.yml").read_text()
        checkouts = workflow.split("- uses: actions/checkout@")[1:]
        self.assertEqual(len(checkouts), 4)
        for block in checkouts:
            settings = block.split("      - ", 1)[0]
            self.assertIn("ref: ${{ github.sha }}", settings)
            self.assertIn("fetch-depth: 0", settings)
            self.assertIn("persist-credentials: false", settings)

    def test_checkout_fallback_can_peel_a_local_tag_but_sha_only_preserves_it(self):
        tag = "v1.2.3"
        self.git("tag", "-a", tag, "-m", "annotated fixture")
        annotation = self.git("rev-parse", "refs/tags/" + tag)
        self.git("push", "origin", "refs/tags/" + tag)
        def checkout(name):
            directory = Path(self.tmp.name) / name
            directory.mkdir()
            def command(*args):
                return subprocess.check_output(["git", *args], cwd=directory, text=True,
                                               stderr=subprocess.DEVNULL).strip()
            command("init")
            command("remote", "add", "origin", str(self.remote))
            command("fetch", "origin", "+refs/heads/*:refs/remotes/origin/*", "+refs/tags/*:refs/tags/*")
            command("checkout", "--detach", self.head)
            self.assertEqual(command("cat-file", "-t", "refs/tags/" + tag), "tag")
            return directory, command
        old, old_git = checkout("old-checkout")
        # Reproduce actions/checkout's ref+peeled-commit fallback from the failed
        # real release. This is only a disposable local clone, never origin.
        old_git("fetch", "--no-tags", "origin", "+" + self.head + ":refs/tags/" + tag)
        with self.assertRaisesRegex(ValueError, "must be annotated"):
            release.validate_tag(old, tag)
        current, current_git = checkout("sha-only-checkout")
        current_git("fetch", "--no-tags", "origin", self.head)
        release.validate_tag(current, tag)
        self.assertEqual(current_git("rev-parse", "refs/tags/" + tag), annotation)
        self.assertIn(annotation, self.git("ls-remote", "origin", "refs/tags/" + tag))


class PackagingWorkflowTests(unittest.TestCase):
    def packaging_script(self):
        workflow = (release.ROOT / ".github/workflows/release.yml").read_text()
        block = workflow.split("      - name: Build archives with per-target notices\n", 1)[1]
        block = block.split("      - ", 1)[0]
        script = block.split("        run: |\n", 1)[1]
        lines = script.splitlines()
        self.assertTrue(lines and all(not line.strip() or line.startswith("          ") for line in lines))
        return "\n".join(line[10:] for line in lines) + "\n"

    def test_all_six_targets_use_exact_argv_on_system_bash(self):
        # Execute the actual workflow block, not a parallel shell implementation.
        # /bin/bash is 3.2 on macOS; no build, network, credentials or model call.
        script = self.packaging_script()
        with tempfile.TemporaryDirectory(prefix="berth-package-argv-") as temporary:
            root = Path(temporary)
            tools = root / "tools with spaces"
            tools.mkdir()
            capture = tools / "python3"
            capture.write_text("#!/bin/sh\nprintf '%s\\n' \"$@\"\n")
            capture.chmod(0o700)
            for system in ("darwin", "linux", "windows"):
                for arch in ("amd64", "arm64"):
                    with self.subTest(system=system, arch=arch):
                        env = {"HOME": str(root), "PATH": str(tools) + ":/usr/bin:/bin",
                               "TARGET_OS": system, "TARGET_ARCH": arch}
                        result = subprocess.run(["/bin/bash", "--noprofile", "--norc", "-c", script],
                                                cwd=root, env=env, text=True, capture_output=True, timeout=10)
                        self.assertEqual(result.returncode, 0, result.stderr)
                        expected = ["scripts/package_release.py", "--os", system, "--arch", arch, "--output", "dist"]
                        if (system, arch) == ("darwin", "arm64"):
                            expected.append("--include-app")
                        self.assertEqual(result.stdout.splitlines(), expected)

    def test_missing_target_is_still_rejected_before_packaging(self):
        script = self.packaging_script()
        with tempfile.TemporaryDirectory(prefix="berth-package-unset-") as temporary:
            root = Path(temporary)
            capture = root / "python3"
            capture.write_text("#!/bin/sh\nprintf 'PACKAGER-MUST-NOT-RUN'\n")
            capture.chmod(0o700)
            for missing in ("TARGET_OS", "TARGET_ARCH"):
                with self.subTest(missing=missing):
                    env = {"HOME": str(root), "PATH": str(root) + ":/usr/bin:/bin", "TARGET_OS": "darwin", "TARGET_ARCH": "amd64"}
                    del env[missing]
                    result = subprocess.run(["/bin/bash", "--noprofile", "--norc", "-c", script],
                                            cwd=root, env=env, text=True, capture_output=True, timeout=10)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn(missing, result.stderr)
                    self.assertEqual(result.stdout, "")
