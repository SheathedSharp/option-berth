"""Release mutations run only against local disposable Git repositories."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
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
        self.configure_fixture_git(self.remote)
        self.git("init", "-b", "main")
        self.configure_fixture_git(self.root)
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

    def configure_fixture_git(self, directory):
        # Git's maintenance/receive-pack defaults may launch detached writers
        # after a successful commit/fetch/push. These tiny throwaway repositories
        # need no maintenance, and strict cleanup must own their entire lifetime.
        # This is local fixture configuration only, never the user's repository.
        self.assertTrue(Path(directory).resolve().is_relative_to(Path(self.tmp.name).resolve()))
        for key, value in (("maintenance.auto", "false"), ("receive.autogc", "false"), ("gc.auto", "0")):
            subprocess.run(["git", "-C", str(directory), "config", "--local", key, value],
                           check=True, capture_output=True)

    def git(self, *args):
        return subprocess.check_output(["git", *args], cwd=self.root, text=True, stderr=subprocess.DEVNULL).strip()

    def test_fixture_git_configuration_is_local_and_complete(self):
        for directory in (self.root, self.remote):
            for key, expected in (("maintenance.auto", "false"), ("receive.autogc", "false"), ("gc.auto", "0")):
                with self.subTest(repository=directory.name, key=key):
                    value = subprocess.check_output(["git", "-C", str(directory), "config", "--local", "--get", key], text=True).strip()
                    self.assertEqual(value, expected)
        with self.assertRaises(AssertionError):
            self.configure_fixture_git(Path(self.tmp.name).parent)

    def test_local_release_modes_are_gone(self):
        # --kind/--publish/--execute are rejected as unknown arguments, and a
        # bare invocation explains that local modes no longer exist.
        for argv in (["--kind", "fix", "--publish"], ["--kind", "fix", "--execute"]):
            with self.subTest(argv=argv):
                result = subprocess.run([sys.executable, str(Path(release.__file__)), *argv],
                                        text=True, capture_output=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("unrecognized arguments", result.stderr)
        bare = subprocess.run([sys.executable, str(Path(release.__file__))],
                              text=True, capture_output=True)
        self.assertNotEqual(bare.returncode, 0)
        self.assertIn("local release/publish modes were removed", bare.stderr)

    def packaging_script(self):
        workflow = (release.ROOT / ".github/workflows/release.yml").read_text()
        block = workflow.split("      - name: Build archives with per-target notices\n", 1)[1]
        block = block.split("      - ", 1)[0]
        script = block.split("        run: |\n", 1)[1]
        lines = script.splitlines()
        self.assertTrue(lines and all(not line.strip() or line.startswith("          ") for line in lines))
        return "\n".join(line[10:] for line in lines) + "\n"

    def test_packaging_argv_is_exact_for_every_target(self):
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
