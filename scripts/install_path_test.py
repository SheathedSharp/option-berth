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
            self.assertIn("no existing shell startup file found", result.stdout)
            self.assertIn('export PATH="$HOME/.local/bin:$PATH"', result.stdout)
            self.assertNotIn("already configured", result.stdout)
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

    def test_one_shell_session_never_gets_the_directory_twice(self):
        # An interactive zsh reads .zshenv and then .zshrc. Writing a bare
        # prepend into both put the same directory on PATH twice, so the
        # installer has to leave a line that is a no-op once PATH already has it.
        pairs = ((".zshenv", ".zshrc"), (".bash_profile", ".bashrc"))
        for first, second in pairs:
            with self.subTest(files=(first, second)), tempfile.TemporaryDirectory() as tmp:
                home = Path(tmp)
                for name in (first, second):
                    (home / name).write_text("# user configuration\n")
                result = self.run_installer(home)
                self.assertEqual(result.returncode, 0, result.stderr)
                env = {"PATH": "/usr/bin:/bin", "HOME": str(home)}
                session = subprocess.run(
                    ["sh", "-c", f'. "$HOME/{first}"; . "$HOME/{second}"; printf %s "$PATH"'],
                    env=env, capture_output=True, text=True, timeout=10,
                )
                self.assertEqual(session.returncode, 0, session.stderr)
                entries = session.stdout.split(":")
                self.assertEqual(
                    entries.count(str(home / ".local" / "bin")), 1,
                    f"{first} and {second} both ran in one session: {session.stdout}",
                )

    def test_custom_directory_is_used_for_detection(self):
        with tempfile.TemporaryDirectory() as tmp:
            home = Path(tmp)
            rc = home / ".zshrc"
            rc.write_text('export PATH="$HOME/.local/bin:$PATH"\n')
            custom = home / "tools" / "bin"
            env = dict(os.environ, HOME=str(home))
            result = subprocess.run(
                ["sh", str(SCRIPT), str(custom)],
                env=env, capture_output=True, text=True, errors="replace", timeout=10,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            text = rc.read_text()
            self.assertIn('export PATH="$HOME/.local/bin:$PATH"', text)
            self.assertIn('export PATH="$HOME/tools/bin:$PATH"', text)

    def test_comment_mention_is_not_a_path_edit(self):
        # The old code-berth installer left a comment naming the directory.
        # Seeing that comment must not suppress the real export line.
        for comment in (
            "# code-berth — the CLI `make install` puts in $HOME/.local/bin\n",
            "  # old installer marker: $HOME/.local/bin\n",
        ):
            with self.subTest(comment=comment), tempfile.TemporaryDirectory() as tmp:
                home = Path(tmp)
                rc = home / ".zshrc"
                rc.write_text(comment)
                result = self.run_installer(home)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn('export PATH="$HOME/.local/bin:$PATH"', rc.read_text())
                self.assertNotIn("already configured", result.stdout)


if __name__ == "__main__":
    unittest.main()
