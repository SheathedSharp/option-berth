"""Check the two public entry pages and their local assets without network I/O."""
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parent.parent

class ReadmeTests(unittest.TestCase):
    def test_bilingual_entry_pages_share_logo_and_current_links(self):
        for name, other in (("README.md", "README.zh.md"), ("README.zh.md", "README.md")):
            text = (ROOT / name).read_text()
            self.assertIn(other, text)
            self.assertIn("option-berth-lockup-dark.svg", text)
            self.assertIn("option-berth-lockup-light.svg", text)
            self.assertIn("SheathedSharp", text)
            # The entry page routes to the home of each contract instead of restating it.
            for target in ("docs/product.", "docs/cli.", "docs/install.",
                           "docs/integrations.", "docs/workplace.",
                           "docs/agent/architecture.md", "docs/agent/releasing.md"):
                self.assertIn(target, text, (name, target))
            for match in re.finditer(r'\]\(([^)]+)\)|(?:src|srcset|href)="([^"]+)"', text):
                target = next(value for value in match.groups() if value)
                if target.startswith(("http:", "https:", "mailto:", "#")):
                    continue
                self.assertTrue((ROOT / target.split("#")[0]).exists(), (name, target))
