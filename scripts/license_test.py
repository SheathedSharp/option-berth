"""Keep the project license, the third-party notices and their delivery path in agreement."""
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parent.parent

MIT_GRANT = "The above copyright notice and this permission notice"
MIT_DISCLAIMER = 'THE SOFTWARE IS PROVIDED "AS IS"'


class LicenseTests(unittest.TestCase):
    def test_project_licenses_list_only_project_copyright(self):
        for name in ("LICENSE", "engine/LICENSE"):
            text = (ROOT / name).read_text()
            self.assertIn("Copyright (c) 2026 SheathedSharp", text)
            self.assertIn("THIRD_PARTY_NOTICES.md", text)
            self.assertIn(MIT_GRANT, text)
            self.assertIn(MIT_DISCLAIMER, text)

    def test_third_party_notices_cover_the_shipped_assets(self):
        text = (ROOT / "THIRD_PARTY_NOTICES.md").read_text()
        for section in ("GSAP", "SIL Open Font License", "Go 依赖"):
            self.assertIn(section, text)

    def test_release_archives_ship_the_notice(self):
        # The notice is only kept if every distributed copy carries it; a source
        # check fails here before a release can silently drop the file.
        source = (ROOT / "scripts/package_release.py").read_text()
        self.assertIn('"THIRD_PARTY_NOTICES.md"', source)
        self.assertIn('"third-party-notices.md"', source)
