"""Guard the explicit verification-only entry; it must never publish main."""
from pathlib import Path
import re
import unittest

class ReleasePreflightTests(unittest.TestCase):
    def test_dispatch_only_extends_read_only_gates(self):
        source = (Path(__file__).resolve().parent.parent / ".github/workflows/release.yml").read_text()
        self.assertIn("  workflow_dispatch:", source)
        self.assertIn('run: test "$SOURCE_REF" = refs/heads/main', source)
        jobs = dict(re.findall(r"^  ([a-z]+):\n(.*?)(?=^  [a-z]+:\n|\Z)", source[source.index("jobs:"):], re.M | re.S))
        for name in ("verify", "windows"):
            self.assertIn("github.event_name == 'workflow_dispatch' && github.ref == 'refs/heads/main'", jobs[name])
        for name in ("package", "publish"):
            self.assertIn("if: startsWith(github.ref, 'refs/tags/v')", jobs[name])
            self.assertNotIn("workflow_dispatch", jobs[name])
        self.assertIn("python3 scripts/release.py --verify-only", jobs["verify"])
        self.assertIn("runner: [ubuntu-24.04, macos-15]", jobs["verify"])
        self.assertIn("contents: read", source.split("jobs:")[0])
        self.assertNotIn("contents: write", jobs["verify"])

if __name__ == "__main__":
    unittest.main()
