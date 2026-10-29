#!/usr/bin/env python3
"""Build two static pages with standard-library tooling and allowlisted assets."""
from pathlib import Path
from string import Template
import html
import json
import shutil

ROOT = Path(__file__).resolve().parent
SITE = "https://sheathedsharp.github.io/option-berth/"
ASSETS = ("icon.svg", "mark.svg", "mark-dark.svg", "boot.js", "site.css", "site.js")


def build() -> Path:
    template = Template((ROOT / "templates/index.html").read_text())
    locales = json.loads((ROOT / "locales.json").read_text())
    if set(locales) != {"zh-CN", "en"} or set(locales["zh-CN"]) != set(locales["en"]):
        raise ValueError("Both locales must provide the same complete content")
    for name in ASSETS:
        source = ROOT / "assets" / name
        if source.is_symlink() or not source.is_file():
            raise ValueError(f"Missing or unsafe public asset: {name}")
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
        target = dist / "en" / "index.html" if english else dist / "index.html"
        target.parent.mkdir(exist_ok=True)
        target.write_text(document)
    (dist / ".nojekyll").write_text("")
    (dist / "robots.txt").write_text("User-agent: *\nAllow: /\nSitemap: " + SITE + "sitemap.xml\n")
    (dist / "sitemap.xml").write_text('<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>' + SITE + '</loc></url><url><loc>' + SITE + 'en/</loc></url></urlset>\n')
    print("Built zh-CN and en engine pages")
    return dist


if __name__ == "__main__":
    build()
