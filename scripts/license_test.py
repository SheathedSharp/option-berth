"""Keep maintainer credit distinct from the notices required for reused work."""
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parent.parent

class LicenseTests(unittest.TestCase):
    def test_maintainer_and_original_copyright_are_preserved(self):
        for name in ("LICENSE", "engine/LICENSE"):
            text = (ROOT / name).read_text()
            self.assertIn("Copyright (c) 2026 SheathedSharp", text)
            self.assertIn("Copyright (c) 2026 RasKrebs", text)
            self.assertIn("The above copyright notice and this permission notice", text)
            self.assertIn('THE SOFTWARE IS PROVIDED "AS IS"', text)

    def test_fonts_keep_their_own_license(self):
        text = (ROOT / "client/macos/Fonts/LICENSE-Monaspace.txt").read_text()
        self.assertIn("SIL OPEN FONT LICENSE", text.upper())
        self.assertNotIn("Copyright (c) 2026 SheathedSharp", text)
