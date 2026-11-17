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
UNINSTALLER = ROOT / "uninstall.sh"
HELPER = ROOT / "scripts" / "install-path.sh"
TAG = "v9.9.9"
# OLD_TAG is what an upgrade starts from, so the binary a stopped unit was running
# and the binary that replaces it are different files.
OLD_TAG = "v9.8.0"


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


# FIXTURE_BINARY is the oberth the archive contains. It answers the three questions the
# installer asks a CLI — version, whether this process can see listeners, and whether a
# runtime service is running — and appends every invocation to $BERTH_TEST_LOG, so a test
# can order the installer's own steps against the supervisor's.
FIXTURE_BINARY = """#!/bin/sh
if [ -n "${BERTH_TEST_LOG:-}" ]; then printf 'oberth %s\\n' "$*" >>"$BERTH_TEST_LOG"; fi
if [ "$1" = version ]; then
	echo 'option-berth {tag} (fixture)'
fi
if [ "$1" = doctor ] && [ "${BERTH_FIXTURE_DOCTOR_OK:-1}" != 1 ]; then
	exit 1
fi
if [ "$1" = runtime ] && [ "$3" = stop ]; then
	echo '{"running": false}'
fi
if [ "$1" = runtime ] && [ "$3" = run ] && [ "$4" = --help ]; then
	echo '{persistent_help}'
fi
exit 0
"""


def build_release(root: Path, helper=True, tag=TAG, supports_persistent=True):
    system, arch = target()
    if not system or not arch:
        raise unittest.SkipTest(f"no release fixture for {platform.system()}/{platform.machine()}")
    stem = f"option-berth-{tag}-{system}-{arch}"
    stage = root / "stage" / stem
    stage.mkdir(parents=True)
    oberth = stage / "oberth"
    persistent_help = "      --persistent   never exit for being idle" if supports_persistent else ""
    oberth.write_text(FIXTURE_BINARY.replace("{tag}", tag).replace("{persistent_help}", persistent_help))
    oberth.chmod(0o755)
    if helper:
        shutil.copyfile(HELPER, stage / "install-path.sh")
    (stage / "LICENSE").write_text("fixture licence\n")
    (stage / "licenses").mkdir()
    (stage / "licenses" / "engine-LICENSE").write_text("fixture engine licence\n")
    (stage / "build-manifest.json").write_text("{}\n")
    archive = root / tag / f"{stem}.tar.gz"
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
        self.shims = root / "shims"
        self.shims.mkdir()
        self.log = root / "calls.log"
        self.archive = None
        self.server = None
        self.write_shims()

    def write_shims(self, body="exit 0"):
        """Put a fake launchctl and systemctl first on PATH.

        Both are written whatever the platform: the scripts pick a supervisor by
        `uname`, and a test that reached the real one would be a test that changed
        this machine. Every invocation is appended to $BERTH_TEST_LOG, so a test can
        assert the exact command and the order it ran in. body is the shell the fake
        runs after logging; the default accepts everything.
        """
        for name in ("launchctl", "systemctl"):
            shim = self.shims / name
            shim.write_text(
                "#!/bin/sh\n"
                f'line="{name} $*"\n'
                'if [ -n "${BERTH_TEST_BIN:-}" ] && [ -f "$BERTH_TEST_BIN" ]; then\n'
                '  if command -v sha256sum >/dev/null 2>&1; then\n'
                '    digest=$(sha256sum "$BERTH_TEST_BIN" | cut -d" " -f1)\n'
                "  else\n"
                '    digest=$(shasum -a 256 "$BERTH_TEST_BIN" | cut -d" " -f1)\n'
                "  fi\n"
                '  line="$line bin=$digest"\n'
                "fi\n"
                'printf \'%s\\n\' "$line" >>"$BERTH_TEST_LOG"\n'
                f"{body}\n"
            )
            shim.chmod(0o755)

    def calls(self, since=0):
        """The command lines the fake supervisor and the fixture CLI logged."""
        if not self.log.exists():
            return []
        return self.log.read_text().splitlines()[since:]

    def install_env(self):
        env = dict(os.environ)
        env.update(
            HOME=str(self.home),
            # The supervisor comes from the shim directory; everything else (curl, tar,
            # uname, id, dirname) still has to resolve, so the rest of PATH stays.
            PATH=str(self.shims) + os.pathsep + os.environ.get("PATH", ""),
            BERTH_TEST_LOG=str(self.log),
            XDG_CONFIG_HOME="",
        )
        return env

    def unit_path(self):
        if platform.system() == "Darwin":
            return self.home / "Library" / "LaunchAgents" / "com.sheathedsharp.option-berth.runtime.plist"
        return self.home / ".config" / "systemd" / "user" / "option-berth-runtime.service"

    @staticmethod
    def digest(path):
        return hashlib.sha256(Path(path).read_bytes()).hexdigest()

    def start(self, latest_available=True, helper=True, raw_helper=False, fail_download=False,
              checksum_overrides=None, supports_persistent=True):
        self.archive = build_release(self.download, helper=helper, supports_persistent=supports_persistent)
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
        env = self.install_env()
        env.update(
            BERTH_INSTALL_BASE_URL=f"http://127.0.0.1:{port}/download",
            BERTH_INSTALL_LATEST_URL=f"http://127.0.0.1:{port}/latest",
            BERTH_INSTALL_RAW_URL=f"http://127.0.0.1:{port}/raw",
            BERTH_INSTALL_MIRRORS="",
            BERTH_INSTALL_DIR=str(self.home / ".local" / "bin"),
        )
        env.pop("NO_MODIFY_PATH", None)
        env.pop("BERTH_INSTALL_NO_SERVICE", None)
        if version:
            env["BERTH_INSTALL_VERSION"] = version
        else:
            env.pop("BERTH_INSTALL_VERSION", None)
        env.update(extra_env or {})
        return subprocess.run(
            ["sh", str(INSTALLER), *args],
            env=env, capture_output=True, text=True, errors="replace", timeout=30,
        )

    def uninstall(self, *args, extra_env=None):
        env = self.install_env()
        env.update(BERTH_INSTALL_DIR=str(self.home / ".local" / "bin"))
        env.update(extra_env or {})
        return subprocess.run(
            ["sh", str(UNINSTALLER), *args],
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


    # ------------------------------------------------------ user-level unit ---
    # install.sh registers the runtime service with launchd or systemd --user. None of
    # these tests reach the real supervisor: the fake one lives first on PATH.

    def test_default_install_registers_the_unit(self):
        self.start()
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stderr)
        unit = self.unit_path()
        self.assertTrue(unit.is_file(), f"no unit at {unit}\nstdout:{result.stdout}\nstderr:{result.stderr}")
        text = unit.read_text()
        self.assertIn(str(self.installed("oberth")), text)
        self.assertIn("--persistent", text)
        self.assertIn("BERTH_RUNTIME_SERVICE_DETACHED", text)

        calls = self.calls()
        if platform.system() == "Darwin":
            self.assertIn("RunAtLoad", text)
            self.assertIn("<key>KeepAlive</key>", text)
            self.assertIn("SuccessfulExit", text)
            self.assertTrue(any(line.startswith("launchctl bootout ") for line in calls), calls)
            self.assertTrue(
                any(line.startswith(f"launchctl bootstrap gui/{os.getuid()} ") and str(unit) in line
                    for line in calls),
                f"the agent was never loaded: {calls}",
            )
        else:
            self.assertIn("Restart=on-failure", text)
            self.assertIn("RestartSec=2", text)
            self.assertIn("WantedBy=default.target", text)
            self.assertIn("systemctl --user enable --now option-berth-runtime.service", calls)

    def test_unit_records_the_runtime_environment(self):
        self.start()
        berth_home = self.root / "berth-home"
        result = self.install(extra_env={"BERTH_HOME": str(berth_home)})
        self.assertEqual(result.returncode, 0, result.stderr)
        text = self.unit_path().read_text()
        self.assertIn(str(berth_home), text)
        self.assertIn("BERTH_RUNTIME_SERVICE_DETACHED", text)
        if platform.system() == "Darwin":
            # launchd opens these files itself, so the directory has to exist first.
            self.assertIn(str(berth_home / "runtime-service.log"), text)
            self.assertTrue(berth_home.is_dir())

    def test_no_service_flag_writes_no_unit(self):
        self.start()
        result = self.install("--no-service")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(self.unit_path().exists())
        self.assertFalse(
            [line for line in self.calls() if line.split(" ")[0] in ("launchctl", "systemctl")],
            "the installer asked a supervisor to do something under --no-service",
        )

    def test_no_service_environment_variable_writes_no_unit(self):
        self.start()
        result = self.install(extra_env={"BERTH_INSTALL_NO_SERVICE": "1"})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(self.unit_path().exists())

    def test_no_service_leaves_an_existing_unit_running_but_says_so(self):
        self.start()
        self.assertEqual(self.install().returncode, 0)
        self.assertTrue(self.unit_path().is_file())
        before = len(self.calls())

        result = self.install("--no-service")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(self.unit_path().is_file())
        self.assertIn("left as it was", result.stdout)
        self.assertFalse([line for line in self.calls(before) if line.startswith("launchctl bootout")])

    def test_a_refusing_supervisor_only_warns(self):
        self.start()
        self.write_shims("echo 'refused' >&2\nexit 1")
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(self.unit_path().is_file(), "the unit file is left for the next login")
        self.assertIn("not started", result.stderr)
        self.assertIn("installed  ", result.stdout)

    def test_observation_failure_skips_registration(self):
        self.start()
        result = self.install(extra_env={"BERTH_FIXTURE_DOCTOR_OK": "0"})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(self.unit_path().exists())
        self.assertIn("not registered", result.stderr)
        self.assertTrue(self.installed("oberth").is_file())

    def test_a_release_without_persistent_skips_registration(self):
        # --version can pin a build that predates `run --persistent`. A unit that
        # passes the flag would crashloop under KeepAlive / Restart=on-failure.
        self.start(supports_persistent=False)
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(self.unit_path().exists(), "an old release must not get a persistent unit")
        self.assertEqual(
            [line for line in self.calls() if line.split(" ")[0] in ("launchctl", "systemctl")],
            [],
            "the installer asked a supervisor to serve a release it cannot run",
        )
        self.assertIn("predates the persistent runtime service", result.stderr)

    def test_a_downgrade_removes_a_stale_unit(self):
        self.start()
        first = self.install()
        self.assertEqual(first.returncode, 0, first.stderr)
        self.assertTrue(self.unit_path().is_file())

        build_release(self.download, tag=OLD_TAG, supports_persistent=False)
        before = len(self.calls())
        second = self.install(version=OLD_TAG)
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertFalse(self.unit_path().exists(), "the incompatible unit was left behind")
        removal = self.calls(before)
        if platform.system() == "Darwin":
            self.assertTrue(any(line.startswith("launchctl bootout ") for line in removal), removal)
        else:
            self.assertIn("systemctl --user disable --now option-berth-runtime.service", removal)
        self.assertIn("removed the old unit", second.stderr)

    def test_upgrade_stops_the_unit_before_replacing_the_binary(self):
        self.start()
        build_release(self.download, tag=OLD_TAG)
        first = self.install(version=OLD_TAG)
        self.assertEqual(first.returncode, 0, first.stderr)
        self.assertTrue(self.unit_path().is_file())
        old_digest = self.digest(self.installed("oberth"))
        before = len(self.calls())

        # BERTH_TEST_BIN makes the fake supervisor record which binary was on disk when
        # it was called, which is what "before replacing" has to mean.
        second = self.install(extra_env={"BERTH_TEST_BIN": str(self.installed("oberth"))})
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertNotEqual(old_digest, self.digest(self.installed("oberth")),
                            "the upgrade installed the same bytes, so nothing was replaced")

        upgrade = self.calls(before)
        stop = [line for line in upgrade
                if line.startswith(("launchctl bootout", "systemctl --user stop"))]
        self.assertTrue(stop, f"the previous unit was never stopped: {upgrade}")
        self.assertIn(f"bin={old_digest}", stop[0],
                      "the unit was stopped after the new binary was already in place")
        start = [index for index, line in enumerate(upgrade)
                 if "bootstrap" in line or "enable --now" in line]
        self.assertTrue(start, f"the new unit was never started: {upgrade}")
        self.assertLess(upgrade.index(stop[0]), start[-1])

    def test_uninstall_removes_the_unit(self):
        self.start()
        installer = self.install()
        self.assertEqual(installer.returncode, 0, installer.stderr)
        unit = self.unit_path()
        self.assertTrue(unit.is_file())
        before = len(self.calls())

        result = self.uninstall()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(unit.exists(), "the unit survived the uninstall and would start a missing binary")
        self.assertFalse(self.installed("oberth").exists())
        removal = self.calls(before)
        if platform.system() == "Darwin":
            self.assertTrue(any(line.startswith("launchctl bootout ") for line in removal), removal)
        else:
            self.assertIn("systemctl --user disable --now option-berth-runtime.service", removal)
        # The state directory is still the user's decision, not the uninstaller's.
        self.assertTrue(self.installed("oberth").parent.is_dir())

    def test_uninstall_without_a_supervisor_is_not_a_failure(self):
        self.start()
        self.assertEqual(self.install().returncode, 0)
        self.write_shims("exit 127")
        result = self.uninstall()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(self.unit_path().exists())


if __name__ == "__main__":
    unittest.main()
