"""Install the engine through a local file module proxy, the way `go install` does.

This catches the two things a normal build does not: the module path must match
the repository subdirectory, and cmd/oberth must be the installed package name.
"""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time
import unittest
import zipfile

ROOT = Path(__file__).resolve().parent.parent
ENGINE = ROOT / "engine"
MODULE = "github.com/sheathedsharp/option-berth/engine"
PACKAGE = MODULE + "/cmd/oberth"
ADAPTER = MODULE + "/cmd/jev-attention"


@unittest.skipUnless(shutil.which("go"), "go is not installed")
class GoInstallTests(unittest.TestCase):
    def test_go_install_produces_oberth_and_jev_attention_with_release_version(self):
        version = f"v1.99.0-test.{int(time.time() * 1000)}"
        with tempfile.TemporaryDirectory(prefix="berth-go-install-test-") as temporary:
            root = Path(temporary)
            proxy = root / "proxy"
            module_dir = proxy / MODULE / "@v"
            module_dir.mkdir(parents=True)
            prefix = f"{MODULE}@{version}/"
            with zipfile.ZipFile(module_dir / f"{version}.zip", "w", zipfile.ZIP_DEFLATED) as zipped:
                for path in sorted(ENGINE.rglob("*")):
                    if not path.is_file() or ".git" in path.parts:
                        continue
                    zipped.write(path, prefix + path.relative_to(ENGINE).as_posix())
            (module_dir / f"{version}.mod").write_bytes((ENGINE / "go.mod").read_bytes())
            (module_dir / f"{version}.info").write_text(
                json.dumps({"Version": version, "Time": "2026-10-29T00:00:00Z"}) + "\n"
            )
            (module_dir / "list").write_text(version + "\n")

            binaries = root / "bin"
            (root / "home").mkdir()
            env = dict(os.environ)
            env.update(
                GOPROXY=f"file://{proxy},https://proxy.golang.org,direct",
                GOSUMDB="off",
                GOFLAGS="-mod=mod",
                GOBIN=str(binaries),
                GOTOOLCHAIN="local",
            )
            for package in (PACKAGE, ADAPTER):
                result = subprocess.run(
                    ["go", "install", f"{package}@{version}"],
                    cwd=root, env=env, capture_output=True, text=True, timeout=300,
                )
                self.assertEqual(result.returncode, 0, result.stderr[-4000:])

            oberth = binaries / "oberth"
            self.assertTrue(oberth.is_file())
            self.assertTrue((binaries / "jev-attention").is_file())
            document = json.loads(subprocess.run(
                [str(oberth), "version", "--json"],
                capture_output=True, text=True, timeout=20, env=dict(os.environ, HOME=str(root / "home")),
            ).stdout)
            self.assertEqual(document["version"], version)


if __name__ == "__main__":
    unittest.main()
