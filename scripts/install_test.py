"""Exercise install.sh against a local fixture release, with no real network or user install."""
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tarfile
import tempfile
import threading
import unittest

ROOT = Path(__file__).resolve().parent.parent
INSTALLER = ROOT / "install.sh"
HELPER = ROOT / "scripts" / "install-path.sh"
TAG = "v9.9.9"


def target():
    system = {"Darwin": "darwin", "Linux": "linux"}.get(platform.system())
    machine = platform.machine().lower()
    arch = {"x86_64": "amd64", "amd64": "amd64", "arm64": "arm64", "aarch64": "arm64"}.get(machine)
    return system, arch


def serve(root: Path, latest_available=True, fail_download=False, checksum_overrides=None):
    """Serve a fixture release tree over loopback so the installer uses its real download path.

    ``/mirror/<name>/<original-url>`` stands in for a third-party GitHub proxy; the
    official ``/download/`` route can be made to fail so the fallback is exercised.
    """
    state = {
        "fail_download": fail_download,
        "checksum_overrides": dict(checksum_overrides or {}),
    }

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def reply(self, status, body=b"", headers=None):
            self.send_response(status)
            for name, value in (headers or {}).items():
                self.send_header(name, value)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            if body:
                self.wfile.write(body)

        def serve_download(self, relative, mirror):
            if mirror is None and state["fail_download"]:
                return self.reply(404)
            path = (root / relative).resolve()
            if root.resolve() not in path.parents or not path.is_file():
                return self.reply(404)
            body = state["checksum_overrides"].get((mirror, relative))
            if body is None:
                body = path.read_bytes()
            return self.reply(200, body)

        def serve_raw(self, relative):
            path = (root / "raw" / relative).resolve()
            if (root / "raw").resolve() not in path.parents or not path.is_file():
                return self.reply(404)
            return self.reply(200, path.read_bytes())

        def do_GET(self):
            if self.path == "/latest":
                if not latest_available:
                    return self.reply(404)
                return self.reply(302, headers={"Location": f"/tag/{TAG}"})
            if self.path.startswith("/tag/"):
                return self.reply(200)
            if self.path.startswith("/raw/"):
                return self.serve_raw(self.path[len("/raw/"):])
            if self.path.startswith("/download/"):
                return self.serve_download(self.path[len("/download/"):], None)
            if self.path.startswith("/mirror/"):
                rest = self.path[len("/mirror/"):]
                name, _, original = rest.partition("/")
                if "/releases/download/" in original:
                    return self.serve_download(original.split("/releases/download/", 1)[1], name)
                if "/raw.githubusercontent.com/" in original:
                    return self.serve_raw(original.split("/raw.githubusercontent.com/", 1)[1])
            self.reply(404)

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    server.state = state
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


def build_release(root: Path, helper=True):
    system, arch = target()
    if not system or not arch:
        raise unittest.SkipTest(f"no release fixture for {platform.system()}/{platform.machine()}")
    stem = f"option-berth-{TAG}-{system}-{arch}"
    stage = root / "stage" / stem
    stage.mkdir(parents=True)
    oberth = stage / "oberth"
    oberth.write_text("#!/bin/sh\nif [ \"$1\" = version ]; then echo 'option-berth v9.9.9 (fixture)'; fi\nexit 0\n")
    oberth.chmod(0o755)
    adapter = stage / "jev-attention"
    adapter.write_text("#!/bin/sh\nexit 0\n")
    adapter.chmod(0o755)
    if helper:
        shutil.copyfile(HELPER, stage / "install-path.sh")
    (stage / "LICENSE").write_text("fixture licence\n")
    (stage / "licenses").mkdir()
    (stage / "licenses" / "engine-LICENSE").write_text("fixture engine licence\n")
    (stage / "build-manifest.json").write_text("{}\n")
    archive = root / TAG / f"{stem}.tar.gz"
    archive.parent.mkdir(parents=True)
    with tarfile.open(archive, "w:gz") as tar:
        tar.add(stage, arcname=stem)
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    archive.with_name(archive.name + ".sha256").write_text(f"{digest}  {archive.name}\n")
    return archive


class InstallTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="berth-install-test-")
        self.addCleanup(self.temporary.cleanup)
        root = Path(self.temporary.name)
        self.root = root
        self.home = root / "home"
        self.home.mkdir()
        self.download = root / "download"
        self.download.mkdir()
        self.archive = None
        self.server = None

    def start(self, latest_available=True, helper=True, raw_helper=False, fail_download=False,
              checksum_overrides=None):
        self.archive = build_release(self.download, helper=helper)
        if raw_helper:
            raw = self.download / "raw" / TAG / "scripts"
            raw.mkdir(parents=True)
            shutil.copyfile(HELPER, raw / "install-path.sh")
        self.server = serve(
            self.download, latest_available=latest_available,
            fail_download=fail_download, checksum_overrides=checksum_overrides,
        )
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)

    def mirror(self, name):
        return f"http://127.0.0.1:{self.server.server_address[1]}/mirror/{name}"

    def install(self, *args, version=TAG, extra_env=None):
        port = self.server.server_address[1]
        env = dict(os.environ)
        env.update(
            HOME=str(self.home),
            BERTH_INSTALL_BASE_URL=f"http://127.0.0.1:{port}/download",
            BERTH_INSTALL_LATEST_URL=f"http://127.0.0.1:{port}/latest",
            BERTH_INSTALL_RAW_URL=f"http://127.0.0.1:{port}/raw",
            BERTH_INSTALL_MIRRORS="",
            BERTH_INSTALL_DIR=str(self.home / ".local" / "bin"),
        )
        env.pop("NO_MODIFY_PATH", None)
        if version:
            env["BERTH_INSTALL_VERSION"] = version
        else:
            env.pop("BERTH_INSTALL_VERSION", None)
        env.update(extra_env or {})
        return subprocess.run(
            ["sh", str(INSTALLER), *args],
            env=env, capture_output=True, text=True, errors="replace", timeout=30,
        )

    def installed(self, name):
        return self.home / ".local" / "bin" / name

    def test_installs_verified_archive_and_configures_path(self):
        self.start()
        rc = self.home / ".zshrc"
        rc.write_text("# user configuration\n")
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(os.access(self.installed("oberth"), os.X_OK))
        self.assertTrue(os.access(self.installed("jev-attention"), os.X_OK))
        self.assertIn("option-berth v9.9.9 (fixture)", result.stdout)
        self.assertIn('export PATH="$HOME/.local/bin:$PATH"', rc.read_text())
        self.assertIn("PATH       added to .zshrc", result.stdout)
        data = self.home / ".local" / "share" / "option-berth"
        self.assertEqual((data / "LICENSE").read_text(), "fixture licence\n")
        self.assertEqual((data / "licenses" / "engine-LICENSE").read_text(), "fixture engine licence\n")
        self.assertTrue((data / "build-manifest.json").is_file())

    def test_latest_release_is_resolved_from_redirect(self):
        self.start()
        result = self.install(version=None)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(TAG, result.stdout)
        self.assertTrue(self.installed("oberth").is_file())

    def test_pinned_version_does_not_ask_for_latest(self):
        self.start(latest_available=False)
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(self.installed("oberth").is_file())

    def test_checksum_mismatch_installs_nothing(self):
        self.start()
        rc = self.home / ".zshrc"
        rc.write_text("# user configuration\n")
        self.archive.write_bytes(b"different bytes than the checksum names")
        result = self.install()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("SHA256 mismatch", result.stderr)
        self.assertFalse(self.installed("oberth").exists())
        self.assertEqual(rc.read_text(), "# user configuration\n")

    def test_no_modify_path_leaves_shell_files_alone(self):
        self.start()
        rc = self.home / ".zshrc"
        rc.write_text("# user configuration\n")
        result = self.install("--no-modify-path")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(rc.read_text(), "# user configuration\n")
        self.assertIn("PATH       not modified", result.stdout)
        self.assertTrue(self.installed("oberth").is_file())

    def test_old_archive_without_helper_fetches_the_tagged_helper(self):
        self.start(helper=False, raw_helper=True)
        rc = self.home / ".zshrc"
        rc.write_text("# user configuration\n")
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('export PATH="$HOME/.local/bin:$PATH"', rc.read_text())

    def test_custom_directory_and_pinned_run(self):
        self.start()
        custom = self.root / "tools" / "bin"
        result = self.install("--dir", str(custom), extra_env={"BERTH_INSTALL_DIR": ""})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((custom / "oberth").is_file())

    def test_unknown_argument_fails_before_download(self):
        self.start()
        result = self.install("--nope")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("unknown argument", result.stderr)
        self.assertFalse(self.installed("oberth").exists())

    def test_official_failure_falls_back_to_a_mirror(self):
        self.start(fail_download=True)
        result = self.install(extra_env={"BERTH_INSTALL_MIRRORS": self.mirror("m1")})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("/mirror/m1/", result.stderr)
        self.assertTrue(self.installed("oberth").is_file())

    def test_empty_mirror_list_does_not_fall_back(self):
        self.start(fail_download=True)
        result = self.install(extra_env={"BERTH_INSTALL_MIRRORS": ""})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("could not download", result.stderr)
        self.assertFalse(self.installed("oberth").exists())

    def test_mirror_checksums_must_agree(self):
        self.start(fail_download=True)
        relative = f"{TAG}/{self.archive.name}.sha256"
        forged = b"0" * 64 + b"  " + self.archive.name.encode() + b"\n"
        self.server.state["checksum_overrides"][("m1", relative)] = forged
        result = self.install(extra_env={
            "BERTH_INSTALL_MIRRORS": f"{self.mirror('m1')} {self.mirror('m2')}",
        })
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("checksum sources disagree", result.stderr)
        self.assertFalse(self.installed("oberth").exists())


if __name__ == "__main__":
    unittest.main()
