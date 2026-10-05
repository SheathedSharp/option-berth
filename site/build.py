#!/usr/bin/env python3
"""Build two static pages with standard-library tooling and allowlisted assets."""
from pathlib import Path
from string import Template
import hashlib
import html
import json
import re
import shutil
import struct

ROOT = Path(__file__).resolve().parent
SITE = "https://sheathedsharp.github.io/option-berth/"
ASSETS = ("icon.svg", "mark.svg", "site.css", "site.js", "services.png", "git.png", "terminal.png", "guide.png", "history.png", "captures.json")


def build() -> Path:
    template = Template((ROOT / "templates/index.html").read_text())
    locales = json.loads((ROOT / "locales.json").read_text())
    if set(locales) != {"zh-CN", "en"} or set(locales["zh-CN"]) != set(locales["en"]):
        raise ValueError("Both locales must provide the same complete content")
    captures = json.loads((ROOT / "assets/captures.json").read_text())
    sizes = {}
    for name in ASSETS:
        source = ROOT / "assets" / name
        if source.is_symlink() or not source.is_file():
            raise ValueError(f"Missing or unsafe public asset: {name}")
        if name.endswith(".png"):
            data = source.read_bytes()
            if data[:8] != b"\x89PNG\r\n\x1a\n" or len(data) > 3 * 1024 * 1024:
                raise ValueError(f"Invalid or oversized PNG: {name}")
            sizes[name] = struct.unpack(">II", data[16:24])
            evidence = captures["assets"][name]
            if [evidence["width"], evidence["height"]] != list(sizes[name]) or hashlib.sha256(data).hexdigest() != evidence["sha256"]:
                raise ValueError(f"Capture evidence mismatch: {name}")
    dist = ROOT / "dist"
    if dist.exists(): shutil.rmtree(dist)
    (dist / "assets").mkdir(parents=True)
    for name in ASSETS: shutil.copyfile(ROOT / "assets" / name, dist / "assets" / name)
    for lang, translated in locales.items():
        english = lang == "en"
        values = {key: html.escape(value, quote=True) for key, value in translated.items()}
        values.update(lang=lang, site_url=SITE, canonical=SITE + ("en/" if english else ""),
                      asset_path="../assets/" if english else "assets/", home_path="./",
                      language_path="../" if english else "en/", other_lang="zh-CN" if english else "en",
                      language_label="中文" if english else "EN")
        document = template.substitute(values)
        for name, (width, height) in sizes.items():
            pattern = r'(src="[^" ]*/' + re.escape(name) + r'" width=")\d+(" height=")\d+'
            document = re.sub(pattern, lambda m: m[1] + str(width) + m[2] + str(height), document)
        target = dist / "en" / "index.html" if english else dist / "index.html"
        target.parent.mkdir(exist_ok=True)
        target.write_text(document)
    (dist / ".nojekyll").write_text("")
    (dist / "robots.txt").write_text("User-agent: *\nAllow: /\nSitemap: " + SITE + "sitemap.xml\n")
    (dist / "sitemap.xml").write_text('<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>' + SITE + '</loc></url><url><loc>' + SITE + 'en/</loc></url></urlset>\n')
    print("Built zh-CN and en pages with five verified native screenshots")
    return dist


if __name__ == "__main__":
    build()
