"""Check the two public entry pages and their local assets without network I/O."""
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parent.parent

class ReadmeTests(unittest.TestCase):
    def test_bilingual_entry_pages_share_logo_and_current_links(self):
        for name, other in (("README.md", "README.en.md"), ("README.en.md", "README.md")):
            text = (ROOT / name).read_text()
            self.assertIn(other, text)
            self.assertIn("option-berth-lockup-dark.svg", text)
            self.assertIn("option-berth-lockup-light.svg", text)
            self.assertIn("SheathedSharp", text)
            self.assertIn("X1.X2.X3", text)
            for match in re.finditer(r'\]\(([^)]+)\)|(?:src|srcset|href)="([^"]+)"', text):
                target = next(value for value in match.groups() if value)
                if target.startswith(("http:", "https:", "mailto:", "#")):
                    continue
                self.assertTrue((ROOT / target.split("#")[0]).exists(), (name, target))
