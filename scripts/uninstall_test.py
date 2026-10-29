"""Exercise uninstall.sh against a synthetic install without touching the caller's home."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
UNINSTALLER = ROOT / "uninstall.sh"


class UninstallTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="berth-uninstall-test-")
        self.addCleanup(self.temporary.cleanup)
        self.home = Path(self.temporary.name) / "home"
        self.home.mkdir()
        self.bin = self.home / ".local" / "bin"
        self.bin.mkdir(parents=True)
        for name in ("oberth", "jev-attention"):
            binary = self.bin / name
            binary.write_text("#!/bin/sh\nexit 0\n")
            binary.chmod(0o755)
        self.data = self.home / ".local" / "share" / "option-berth"
        (self.data / "licenses").mkdir(parents=True)
        (self.data / "LICENSE").write_text("licence\n")
        (self.data / "build-manifest.json").write_text("{}\n")
        (self.data / "licenses" / "engine-LICENSE").write_text("licence\n")
        self.state = self.home / ".option-berth"
        self.state.mkdir()
        (self.state / "option-berth.db").write_text("state\n")

    def run_uninstaller(self, *args, extra_env=None):
        env = dict(os.environ, HOME=str(self.home))
        env.update(
            BERTH_INSTALL_DIR=str(self.bin),
            BERTH_INSTALL_DATA_DIR=str(self.data),
        )
        env.pop("BERTH_HOME", None)
        env.update(extra_env or {})
        return subprocess.run(
            ["sh", str(UNINSTALLER), *args],
            env=env, capture_output=True, text=True, errors="replace", timeout=10,
        )

    def write_shell_config(self):
        rc = self.home / ".zshrc"
        rc.write_text(
            "# user configuration\n"
            "export EDITOR=vi\n"
            "# option-berth CLI (installed in $HOME/.local/bin)\n"
            'export PATH="$HOME/.local/bin:$PATH"\n'
            "# option-berth — the CLI `mage install` puts in $HOME/.local/bin\n"
            'export PATH="$HOME/.local/bin:$PATH"\n'
            'export PATH="$HOME/tools/bin:$PATH"\n'
        )
        return rc

    def test_removes_binaries_notices_and_installer_path_lines(self):
        rc = self.write_shell_config()
        result = self.run_uninstaller()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse((self.bin / "oberth").exists())
        self.assertFalse((self.bin / "jev-attention").exists())
        self.assertFalse(self.data.exists())
        self.assertTrue(self.state.is_dir())
        text = rc.read_text()
        self.assertIn("# user configuration", text)
        self.assertIn("export EDITOR=vi", text)
        self.assertIn('export PATH="$HOME/tools/bin:$PATH"', text)
        self.assertNotIn("option-berth", text)
        self.assertIn("removed    oberth jev-attention", result.stdout)
        self.assertIn("state      kept", result.stdout)

    def test_keep_path_leaves_shell_files_alone(self):
        rc = self.write_shell_config()
        before = rc.read_bytes()
        result = self.run_uninstaller("--keep-path")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(rc.read_bytes(), before)
        self.assertIn("PATH       left alone (--keep-path)", result.stdout)

    def test_purge_state_removes_only_the_state_directory(self):
        result = self.run_uninstaller("--purge-state")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(self.state.exists())
        self.assertIn("state      removed", result.stdout)

    def test_purge_state_uses_berth_home_override(self):
        custom = Path(self.temporary.name) / "custom-state"
        custom.mkdir()
        (custom / "option-berth.db").write_text("state\n")
        result = self.run_uninstaller("--purge-state", extra_env={"BERTH_HOME": str(custom)})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(custom.exists())
        self.assertTrue(self.state.is_dir())

    def test_purge_state_refuses_relative_override(self):
        result = self.run_uninstaller("--purge-state", extra_env={"BERTH_HOME": "relative-state"})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("non-absolute", result.stderr)
        self.assertTrue(self.state.is_dir())

    def test_custom_data_directory_keeps_unrelated_files(self):
        extra = self.data / "keep.txt"
        extra.write_text("caller-owned\n")
        result = self.run_uninstaller()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(self.data.is_dir())
        self.assertEqual(extra.read_text(), "caller-owned\n")
        self.assertFalse((self.data / "LICENSE").exists())
        self.assertFalse((self.data / "licenses").exists())

    def test_no_installer_line_reports_nothing_changed(self):
        rc = self.home / ".zshrc"
        rc.write_text("# user configuration\n")
        result = self.run_uninstaller()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(rc.read_text(), "# user configuration\n")
        self.assertIn("no installer line found", result.stdout)


if __name__ == "__main__":
    unittest.main()
