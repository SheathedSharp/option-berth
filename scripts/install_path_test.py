"""Exercise the real PATH installer without touching the caller's shell files."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("install-path.sh")


class InstallPathTests(unittest.TestCase):
    def run_installer(self, home, opt_out=None):
        env = dict(os.environ, HOME=str(home))
        env.pop("NO_MODIFY_PATH", None)
        if opt_out is not None:
            env["NO_MODIFY_PATH"] = opt_out
        return subprocess.run(
            ["sh", str(SCRIPT), str(home / ".local" / "bin")],
            env=env, capture_output=True, text=True, errors="replace", timeout=10,
        )

    def test_opt_out_succeeds_without_modifying_existing_files(self):
        for value in ("1", "yes"):
            with self.subTest(value=value), tempfile.TemporaryDirectory() as tmp:
                home = Path(tmp)
                before = b"# user configuration\nexport EDITOR=vi\n"
                for name in (".zshrc", ".profile"):
                    (home / name).write_bytes(before)
                result = self.run_installer(home, value)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn(f"NO_MODIFY_PATH={value}", result.stdout)
                self.assertIn('export PATH="$HOME/.local/bin:$PATH"', result.stdout)
                self.assertEqual(sorted(p.name for p in home.iterdir()), [".profile", ".zshrc"])
                for name in (".zshrc", ".profile"):
                    self.assertEqual((home / name).read_bytes(), before)

    def test_opt_out_does_not_create_startup_files(self):
        with tempfile.TemporaryDirectory() as tmp:
            home = Path(tmp)
            result = self.run_installer(home, "1")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(list(home.iterdir()), [])

    def test_no_startup_files_reports_manual_step_not_success(self):
        with tempfile.TemporaryDirectory() as tmp:
            home = Path(tmp)
            result = self.run_installer(home)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("没有找到", result.stdout)
            self.assertIn('export PATH="$HOME/.local/bin:$PATH"', result.stdout)
            self.assertNotIn("已经在", result.stdout)
            self.assertEqual(list(home.iterdir()), [])

    def test_install_is_idempotent(self):
        with tempfile.TemporaryDirectory(prefix="berth path ") as tmp:
            home = Path(tmp)
            rc = home / ".zshrc"
            rc.write_text("# user configuration\n")
            first = self.run_installer(home, "0")
            self.assertEqual(first.returncode, 0, first.stderr)
            installed = rc.read_bytes()
            second = self.run_installer(home)
            self.assertEqual(second.returncode, 0, second.stderr)
            self.assertEqual(rc.read_bytes(), installed)
            self.assertEqual(installed.count(b"export PATH="), 1)


if __name__ == "__main__":
    unittest.main()
