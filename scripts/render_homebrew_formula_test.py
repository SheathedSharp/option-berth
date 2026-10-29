"""Check the Homebrew formula is rendered from the exact release asset names."""
import importlib.util
from pathlib import Path
import unittest

SCRIPT = Path(__file__).with_name("render_homebrew_formula.py")
SPEC = importlib.util.spec_from_file_location("render_homebrew_formula", SCRIPT)
formula = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(formula)
TEMPLATE = Path(__file__).resolve().parent.parent / "packaging" / "homebrew" / "option-berth.rb.in"
TAG = "v1.2.3"
DIGESTS = {
    "darwin-amd64": "a" * 64,
    "darwin-arm64": "b" * 64,
    "linux-amd64": "c" * 64,
    "linux-arm64": "d" * 64,
}


class FormulaTests(unittest.TestCase):
    def checksums(self):
        return "".join(
            f"{digest}  option-berth-{TAG}-{target}.tar.gz\n"
            for target, digest in DIGESTS.items()
        )

    def test_uses_published_asset_names_and_checksums(self):
        text = formula.render(TEMPLATE.read_text(), TAG, formula.checksums(self.checksums()))
        for target, digest in DIGESTS.items():
            self.assertIn(f"option-berth-{TAG}-{target}.tar.gz", text)
            self.assertIn(f'sha256 "{digest}"', text)
        self.assertNotIn("{{", text)
        self.assertIn('bin.install "oberth", "jev-attention"', text)

    def test_rejects_missing_checksum(self):
        text = self.checksums().replace(f"{'d' * 64}  option-berth-{TAG}-linux-arm64.tar.gz\n", "")
        with self.assertRaisesRegex(ValueError, "linux-arm64"):
            formula.render(TEMPLATE.read_text(), TAG, formula.checksums(text))

    def test_rejects_invalid_tag(self):
        with self.assertRaises(ValueError):
            formula.render(TEMPLATE.read_text(), "v1.2", formula.checksums(self.checksums()))

    def test_rejects_unknown_placeholder(self):
        with self.assertRaisesRegex(ValueError, "UNKNOWN"):
            formula.render(TEMPLATE.read_text() + "{{UNKNOWN}}\n", TAG, formula.checksums(self.checksums()))


if __name__ == "__main__":
    unittest.main()
