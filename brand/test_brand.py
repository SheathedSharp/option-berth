"""品牌几何与导出契约：python3 -m unittest discover -s brand -p 'test_*.py'。"""
import importlib.util
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
import xml.etree.ElementTree as ET

HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("brand_build", HERE / "build-option-berth.py")
BUILD = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(BUILD)


class BrandTests(unittest.TestCase):
    def test_closed_bounded_geometry(self):
        paths, _ = BUILD.read_geometry()
        self.assertEqual(set(paths), set(BUILD.NAMES))
        for name, commands in paths.items():
            with self.subTest(name=name):
                self.assertEqual(commands[0][0], "move")
                self.assertEqual(commands[-1][0], "close")
                self.assertEqual(sum(op == "move" for op, _ in commands), 1)
                self.assertEqual(sum(op == "close" for op, _ in commands), 1)
                for _, values in commands:
                    self.assertTrue(all(0 <= value <= 24 for value in values))

    def test_token_center_and_compact_weight(self):
        paths, _ = BUILD.read_geometry()
        bounds = {}
        for variant in ("standard", "compact"):
            token = paths[variant + "Token"]
            coordinates = [values for _, values in token if values]
            xs = [x for values in coordinates for x in values[::2]]
            ys = [y for values in coordinates for y in values[1::2]]
            self.assertAlmostEqual((min(xs) + max(xs)) / 2, 12, places=5)
            self.assertAlmostEqual((min(ys) + max(ys)) / 2, 5.7, places=5)
            bounds[variant] = max(ys) - min(ys)
        self.assertAlmostEqual(bounds["standard"], 3.2)
        self.assertAlmostEqual(bounds["compact"], 3.6)

    def test_palette_and_icon_layout(self):
        _, palette = BUILD.read_geometry()
        self.assertEqual(palette["ink"], "#23262B")
        self.assertEqual(palette["accent"], "#A8543E")
        self.assertEqual(palette["darkAccent"], "#DB9378")
        self.assertEqual(palette["iconInset"] * 1024, 64)
        self.assertEqual(palette["iconRadius"] * 1024, 202)
        self.assertEqual(palette["iconMarkScale"] * 1024, 664)

    def test_generated_assets_are_current(self):
        assets = BUILD.build_assets()
        self.assertEqual(len(assets), 13)
        for name, content in assets.items():
            with self.subTest(name=name):
                self.assertEqual((HERE / name).read_text(encoding="utf-8"), content)

    def test_svgs_are_vector_only_and_accessible(self):
        forbidden = {"image", "script", "foreignObject", "filter", "linearGradient", "radialGradient", "text"}
        for name, content in BUILD.build_assets().items():
            root = ET.fromstring(content)
            with self.subTest(name=name):
                self.assertEqual(root.get("aria-label"), "option-berth")
                for node in root.iter():
                    self.assertNotIn(node.tag.rsplit("}", 1)[-1], forbidden)
                    self.assertNotIn("stroke", node.attrib)
                    if node.tag.endswith("}path"):
                        self.assertTrue(node.get("d", "").strip().upper().endswith("Z"))

    def test_variants_change_paint_not_shape(self):
        assets = BUILD.build_assets()
        def outlines(name):
            root = ET.fromstring(assets[f"option-berth-mark-{name}.svg"])
            return [node.get("d") for node in root.findall(f"{{{BUILD.NS}}}path")]
        standard = outlines("light")
        self.assertEqual(len(standard), 2)
        for name in ("dark", "mono", "empty-light", "empty-dark"):
            self.assertEqual(outlines(name), standard)
        self.assertEqual(outlines("small-light"), outlines("small-dark"))
        self.assertNotEqual(outlines("small-light"), standard)

    def test_check_is_read_only_and_detects_drift(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            command = [sys.executable, str(HERE / "build-option-berth.py"), "--output", directory]
            subprocess.run(command, check=True, capture_output=True)
            before = {path.name: path.read_bytes() for path in root.iterdir()}
            result = subprocess.run(command + ["--check"], capture_output=True)
            self.assertEqual(result.returncode, 0)
            self.assertEqual({p.name: p.read_bytes() for p in root.iterdir()}, before)
            target = root / "option-berth-mark-light.svg"
            target.write_text("outdated", encoding="utf-8")
            result = subprocess.run(command + ["--check"], capture_output=True)
            self.assertEqual(result.returncode, 1)
            self.assertEqual(target.read_text(encoding="utf-8"), "outdated")
            target.unlink()
            self.assertEqual(subprocess.run(command + ["--check"], capture_output=True).returncode, 1)

    def test_malformed_geometry_fails_loudly(self):
        source = BUILD.SOURCE.read_text(encoding="utf-8")
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "Geometry.swift"
            for malformed in (source.replace(".close,", ".unknown,", 1),
                              source.replace(".close,", "", 1),
                              source.replace("0x23262B", "0xZZZZZZ", 1)):
                path.write_text(malformed, encoding="utf-8")
                with self.assertRaises(ValueError):
                    BUILD.read_geometry(path)


if __name__ == "__main__":
    unittest.main()
