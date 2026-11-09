#!/usr/bin/env python3
"""Build two static pages with standard-library tooling and allowlisted assets."""
from pathlib import Path
from string import Template
from datetime import datetime
from html.parser import HTMLParser
import base64
import hashlib
import html
import json
import os
import posixpath
import re
import shutil

ROOT = Path(__file__).resolve().parent
# The canonical host is a build input: GitHub Pages remains the default preview,
# while the public site is served from GitHub Pages on its own domain.
SITE = os.environ.get("SITE_URL", "https://oberth.zayju.de/").strip()
if not SITE.startswith(("https://", "http://")):
    raise ValueError("SITE_URL must be an absolute http(s) URL")
if not SITE.endswith("/"):
    SITE += "/"
ASSETS = ("icon.svg", "mark.svg", "mark-dark.svg", "boot.js", "site.css", "site.js", "guide.css", "guide.js")
SNAPSHOT = ROOT / "data" / "eval.json"
SNAPSHOT_SCHEMA = "oberth.site-eval/v1"
ARMS = ("oberth", "native")
GUIDE = ROOT / "guide"
GUIDE_TEMPLATE = Template((ROOT / "templates" / "guide.html").read_text())
CHAPTER_TEMPLATE = Template((ROOT / "templates" / "guide-chapter.html").read_text())
REPO_URL = "https://github.com/SheathedSharp/option-berth"
FOREIGN_HREF = ("http://", "https://", "mailto:", "tel:")
BLOCK_TAGS = {
    "h1", "h2", "h3", "h4", "p", "ul", "ol", "li", "pre", "code", "table", "thead",
    "tbody", "tr", "th", "td", "dl", "dt", "dd", "aside", "figure", "figcaption",
    "blockquote", "details", "summary",
}
VOID_TAGS = {"br", "img", "input", "meta", "link", "hr", "source", "wbr"}


def _number(value: object, name: str) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ValueError(f"measured data: {name} must be a number")
    return float(value)


def _percent(value: object) -> str:
    return f"{_number(value, 'rate'):g}%"


def load_snapshot(path: Path = SNAPSHOT) -> dict:
    """Load the promoted agent-eval snapshot the page renders.

    The page must never show hand-typed results. The snapshot is produced by
    scripts/export_site_eval.py from a finished merged report and committed;
    the build refuses a missing or malformed one instead of shipping numbers
    nobody can trace to a run.
    """
    try:
        snapshot = json.loads(path.read_text(encoding="utf-8"))
    except OSError as exc:
        raise ValueError(
            f"measured data: cannot read {path}; run scripts/export_site_eval.py"
        ) from exc
    except json.JSONDecodeError as exc:
        raise ValueError(f"measured data: {path} is not valid JSON: {exc}") from exc
    if snapshot.get("schema") != SNAPSHOT_SCHEMA:
        raise ValueError(f"measured data: {path} is not {SNAPSHOT_SCHEMA}")
    run_url = str(snapshot.get("run_url") or "")
    if not run_url.startswith("https://github.com/"):
        raise ValueError("measured data: run_url must point at a GitHub Actions run")
    measured_at = str(snapshot.get("measured_at") or "")
    try:
        datetime.fromisoformat(measured_at.replace("Z", "+00:00"))
    except ValueError as exc:
        raise ValueError("measured data: measured_at must be an ISO timestamp") from exc
    metrics = snapshot.get("metrics") or {}
    for arm in ARMS:
        arm_metrics = metrics.get(arm) or {}
        for key in ("completed", "total", "steps_p50", "steps_p95", "duration_p50_s", "oob_total"):
            if key not in arm_metrics:
                raise ValueError(f"measured data: metrics.{arm}.{key} is missing")
        if _number(arm_metrics["completed"], f"metrics.{arm}.completed") > _number(
            arm_metrics["total"], f"metrics.{arm}.total"
        ):
            raise ValueError(f"measured data: {arm} completed more runs than it has")
    return snapshot


def snapshot_values(snapshot: dict) -> dict[str, str]:
    metrics = snapshot["metrics"]
    oberth, native = metrics["oberth"], metrics["native"]
    measured = datetime.fromisoformat(str(snapshot["measured_at"]).replace("Z", "+00:00"))
    scenarios = int(_number(snapshot.get("scenarios"), "scenarios"))
    comparable = int(_number(snapshot.get("comparable_scenarios"), "comparable_scenarios"))
    fewer = int(_number(snapshot.get("scenarios_with_fewer_turns"), "scenarios_with_fewer_turns"))
    if not (0 <= fewer <= comparable <= scenarios):
        raise ValueError("measured data: scenario counts are inconsistent")

    def completion(arm: dict) -> str:
        return f"{int(arm['completed'])}/{int(arm['total'])}"

    def rate(arm: dict) -> str:
        total = _number(arm["total"], "total")
        if total <= 0:
            raise ValueError("measured data: an arm has no runs")
        return _percent(100 * _number(arm["completed"], "completed") / total)

    def seconds(value: object) -> str:
        return f"{_number(value, 'duration'):.0f}s"

    return {
        "proof_run_count": str(int(_number(snapshot.get("runs"), "runs"))),
        "proof_scenario_count": str(scenarios),
        "proof_repeat": str(int(_number(snapshot.get("repeat"), "repeat"))),
        "proof_completion_with": completion(oberth),
        "proof_completion_without": completion(native),
        "proof_rate_with": rate(oberth),
        "proof_rate_without": rate(native),
        "proof_turns_with": f"{_number(oberth['steps_p50'], 'steps_p50'):g}",
        "proof_turns_without": f"{_number(native['steps_p50'], 'steps_p50'):g}",
        "proof_turns_p95_with": f"P95 {_number(oberth['steps_p95'], 'steps_p95'):g}",
        "proof_turns_p95_without": f"P95 {_number(native['steps_p95'], 'steps_p95'):g}",
        "proof_time_with": seconds(oberth["duration_p50_s"]),
        "proof_time_without": seconds(native["duration_p50_s"]),
        "proof_scenarios_fewer": str(fewer),
        "proof_comparable_scenarios": str(comparable),
        "proof_oob_with": str(int(_number(oberth["oob_total"], "oob_total"))),
        "proof_oob_without": str(int(_number(native["oob_total"], "oob_total"))),
        "proof_measured_date": measured.date().isoformat(),
        "proof_run_url": str(snapshot["run_url"]),
    }


def _rel(from_url: str, to_url: str) -> str:
    """Relative directory URL from one page directory to another."""
    start = from_url.rstrip("/") or "."
    target = to_url.rstrip("/") or "."
    rel = posixpath.relpath(target, start)
    return "" if rel == "." else rel + "/"


def _root_prefix(url: str) -> str:
    return "../" * len([part for part in url.strip("/").split("/") if part])


def _guide_url(locale: dict, slug: str) -> str:
    """Directory URL of one chapter in one locale.

    The default locale lives at the guide root; every other locale adds its
    prefix, so `/guide/` is the canonical English text and `/guide/zh/` its
    Chinese counterpart.
    """
    base = "guide/" + locale["prefix"]
    return base if slug == "overview" else base + slug + "/"


def _text(value: dict, locale: dict) -> str:
    """One localised string from a map keyed by locale id."""
    return str(value[locale["id"]])


def load_book() -> dict:
    """Load and validate the guide's single content index.

    The book has one source of order, titles and summaries; the chapters add
    only their own HTML. A missing counterpart, an unknown part or a chapter
    that no longer mirrors its pair fails the build instead of shipping.
    """
    try:
        book = json.loads((GUIDE / "book.json").read_text(encoding="utf-8"))
    except OSError as exc:
        raise ValueError(f"guide book: cannot read {GUIDE / 'book.json'}") from exc
    except json.JSONDecodeError as exc:
        raise ValueError(f"guide book: book.json is not valid JSON: {exc}") from exc

    locales = book.get("locales") or []
    locale_ids = [item.get("id") for item in locales]
    if not locales or len(locale_ids) != len(set(locale_ids)) or None in locale_ids:
        raise ValueError("guide book: locales must be a non-empty list with unique ids")
    prefixes = [str(item.get("prefix") or "") for item in locales]
    if len(prefixes) != len(set(prefixes)):
        raise ValueError("guide book: locale prefixes must be unique")
    if prefixes[0] != "" or book.get("default") != locale_ids[0]:
        raise ValueError("guide book: the first locale is the default and carries no prefix")
    for item in locales:
        for key in ("label", "lang", "og_locale"):
            if not str(item.get(key) or "").strip():
                raise ValueError(f"guide book: locale {item['id']} has no {key}")
        prefix = str(item.get("prefix") or "")
        if prefix and not (prefix.endswith("/") and not prefix.startswith("/")):
            raise ValueError(f"guide book: locale {item['id']} prefix must look like 'zh/'")

    labels = book.get("labels") or {}
    if set(labels) != set(locale_ids):
        raise ValueError("guide book: labels must exist for every locale and no other")
    keys = set(labels[locale_ids[0]])
    for locale_id in locale_ids:
        if set(labels[locale_id]) != keys:
            raise ValueError("guide book: every locale must carry the same label keys")
        for key, value in labels[locale_id].items():
            if not str(value).strip():
                raise ValueError(f"guide book: labels.{locale_id}.{key} is empty")

    parts = book.get("parts") or []
    chapters = book.get("chapters") or []
    if not parts or not chapters:
        raise ValueError("guide book: parts and chapters may not be empty")
    part_ids = [part.get("id") for part in parts]
    if len(part_ids) != len(set(part_ids)) or None in part_ids:
        raise ValueError("guide book: part ids must be unique")
    for part in parts:
        if set(part.get("title") or {}) != set(locale_ids):
            raise ValueError(f"guide book: part {part.get('id')} has no title in every locale")
    slugs = [chapter.get("slug") for chapter in chapters]
    if len(slugs) != len(set(slugs)) or None in slugs:
        raise ValueError("guide book: chapter slugs must be unique")
    if "overview" not in slugs:
        raise ValueError("guide book: the overview chapter is required")
    for chapter in chapters:
        slug = chapter["slug"]
        if chapter.get("part") not in part_ids:
            raise ValueError(f"guide book: {slug} names an unknown part")
        side = chapter.get("locales") or {}
        if set(side) != set(locale_ids):
            raise ValueError(f"guide book: {slug} must be written in every locale and no other")
        for locale_id in locale_ids:
            for key in ("nav", "title", "lede", "summary"):
                if not str(side[locale_id].get(key) or "").strip():
                    raise ValueError(f"guide book: {slug}.locales.{locale_id}.{key} is empty")
            fragment = GUIDE / f"{slug}.{locale_id}.html"
            if not fragment.is_file():
                raise ValueError(f"guide book: missing chapter fragment {fragment.name}")
        for item in chapter.get("refs") or []:
            if not str(item.get("href") or "").startswith("https://"):
                raise ValueError(f"guide book: {slug} has a non-https contract link")
            if set(item.get("label") or {}) != set(locale_ids):
                raise ValueError(f"guide book: {slug} has a contract link without every locale")
    return book


class _FragmentScan(HTMLParser):
    """Structure, headings, ids and code blocks of one chapter fragment."""

    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.headings = []
        self.ids = []
        self.code_blocks = []
        self.counts = {"h1": 0, "h2": 0, "h3": 0, "pre": 0, "tr": 0, "li": 0, "dt": 0}
        self.heading_levels = []
        self._heading = None
        self._heading_id = None
        self._heading_text = []
        self._pre = 0
        self._pre_text = []
        self.sections = []
        self._section = None

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if attrs.get("id"):
            self.ids.append(attrs["id"])
        if tag in ("h1", "h2", "h3", "h4"):
            self._heading = int(tag[1])
            self._heading_id = attrs.get("id")
            self._heading_text = []
            self.heading_levels.append(self._heading)
            if tag == "h2":
                self._section = {"id": attrs.get("id") or "", "title": "", "text": []}
                self.sections.append(self._section)
            if tag in self.counts:
                self.counts[tag] += 1
        if tag == "pre":
            self._pre += 1
            self._pre_text = []
            self.counts["pre"] += 1
        if tag == "tr":
            self.counts["tr"] += 1
        if tag == "li":
            self.counts["li"] += 1
        if tag == "dt":
            self.counts["dt"] += 1

    def handle_endtag(self, tag):
        if tag in ("h1", "h2", "h3", "h4") and self._heading is not None:
            text = " ".join("".join(self._heading_text).split())
            self.headings.append((self._heading, self._heading_id, text))
            if self._heading == 2 and self._section is not None:
                self._section["title"] = text
            self._heading = None
        if tag == "pre" and self._pre:
            self._pre -= 1
            self.code_blocks.append("".join(self._pre_text))

    def handle_data(self, data):
        if self._heading is not None:
            self._heading_text.append(data)
        if self._pre:
            self._pre_text.append(data)
        if self._section is not None and self._heading is None:
            self._section["text"].append(data)


def _scan(fragment: str) -> _FragmentScan:
    scan = _FragmentScan()
    scan.feed(fragment)
    scan.close()
    return scan


def _check_fragment(scan: _FragmentScan, slug: str, locale: str) -> None:
    if len(scan.ids) != len(set(scan.ids)):
        raise ValueError(f"guide book: {slug}.{locale}.html repeats an id")
    h2s = [(heading_id, text) for level, heading_id, text in scan.headings if level == 2]
    if not h2s:
        raise ValueError(f"guide book: {slug}.{locale}.html has no h2")
    for level, heading_id, text in scan.headings:
        if level not in (2, 3):
            continue
        if not heading_id:
            raise ValueError(f"guide book: {slug}.{locale}.html has an h{level} without an id")
        if not text:
            raise ValueError(f"guide book: {slug}.{locale}.html has an empty h{level}")
    if any(level == 1 for level, _, _ in scan.headings):
        raise ValueError(
            f"guide book: {slug}.{locale}.html carries an h1; the page title comes from book.json"
        )


def _structure(scan: _FragmentScan) -> dict:
    return {
        "counts": scan.counts,
        "heading_levels": scan.heading_levels,
        "code": scan.code_blocks,
    }


class _TextExtract(HTMLParser):
    """Plain text of a fragment for llms-full.txt, with links kept as URLs."""

    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.out = []
        self.pre = 0
        self.links = []
        self.strong = 0
        self.em = 0
        self.skip = 0

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if self.skip:
            if tag not in VOID_TAGS:
                self.skip += 1
            return
        if "data-llms-ignore" in attrs:
            self.skip = 1
            return
        if tag == "pre":
            self.pre += 1
            self.out.append("\n```\n")
        elif tag == "code" and not self.pre:
            self.out.append("`")
        elif tag in ("h1", "h2", "h3", "h4"):
            self.out.append("\n" + "#" * int(tag[1]) + " ")
        elif tag == "p":
            self.out.append("\n\n")
        elif tag in ("ul", "ol"):
            self.out.append("\n")
        elif tag == "li":
            self.out.append("\n- ")
        elif tag == "tr":
            self.out.append("\n")
        elif tag in ("th", "td"):
            self.out.append(" | ")
        elif tag == "br":
            self.out.append("\n")
        elif tag == "a":
            self.links.append(attrs.get("href", ""))
            self.out.append("[")
        elif tag == "strong":
            self.strong += 1
            self.out.append("**")
        elif tag == "em":
            self.em += 1
            self.out.append("*")
        elif tag == "img":
            self.out.append(attrs.get("alt", ""))
        elif tag == "dt":
            self.out.append("\n")
        elif tag == "dd":
            self.out.append(": ")
        elif tag == "figcaption":
            self.out.append("\n")
        elif tag in ("table", "aside", "figure", "blockquote", "details", "summary", "div", "section"):
            self.out.append("\n")

    def handle_endtag(self, tag):
        if self.skip:
            if tag not in VOID_TAGS:
                self.skip -= 1
            return
        if tag == "pre":
            self.pre = max(0, self.pre - 1)
            self.out.append("\n```\n")
        elif tag == "code" and not self.pre:
            self.out.append("`")
        elif tag == "a":
            href = self.links.pop() if self.links else ""
            self.out.append(f"]({href})" if href else "]")
        elif tag == "strong" and self.strong:
            self.strong -= 1
            self.out.append("**")
        elif tag == "em" and self.em:
            self.em -= 1
            self.out.append("*")
        elif tag in ("h1", "h2", "h3", "h4", "p", "li", "tr", "table", "ul", "ol", "dl", "dt",
                     "dd", "aside", "figure", "figcaption", "blockquote", "details",
                     "summary", "div", "section"):
            self.out.append("\n")

    def handle_data(self, data):
        if self.skip:
            return
        if not self.pre and not data.strip():
            return
        self.out.append(data)

    def text(self) -> str:
        raw = "".join(self.out).replace(" ", " ")
        # Keep the columns inside code blocks; normalize prose only.
        segments = raw.split("```")
        for index, segment in enumerate(segments):
            if index % 2:
                continue
            segment = re.sub(r"[ \t]+\n", "\n", segment)
            segment = re.sub(r"[ \t]{2,}", " ", segment)
            segment = re.sub(r"\n +", "\n", segment)
            segment = re.sub(r"\n{3,}", "\n\n", segment)
            segments[index] = segment
        return "```".join(segments).strip()


def _resolve(fragment: str, locale: dict, url: str) -> str:
    guide = _rel(url, _guide_url(locale, "overview"))
    return fragment.replace("{{guide}}", guide).replace("{{root}}", _root_prefix(url))


def _esc(value: object) -> str:
    return html.escape(str(value), quote=True)


def _page_toc(scan: _FragmentScan) -> str:
    out = []
    for level, heading_id, text in scan.headings:
        if level not in (2, 3):
            continue
        item_class = ' class="toc-sub"' if level == 3 else ""
        out.append(f'<li{item_class}><a href="#{_esc(heading_id)}">{_esc(text)}</a></li>')
    return "\n".join(out)


def _refs(chapter: dict, locale: dict) -> str:
    return "\n".join(
        f'<li><a href="{_esc(item["href"])}">{_esc(str(item["label"][locale["id"]]))}</a></li>'
        for item in chapter.get("refs") or []
    )


def _chapter_nav(book: dict, locale: dict, chapter: dict, index: int, labels: dict) -> tuple[str, str]:
    url = _guide_url(locale, chapter["slug"])
    previous_html = ""
    next_html = ""
    if index > 0:
        previous = book["chapters"][index - 1]
        href = _rel(url, _guide_url(locale, previous["slug"])) or "./"
        title = previous["locales"][locale["id"]]["title"]
        previous_html = (
            f'<a class="pager-link pager-prev" href="{href}"><span>{_esc(labels["previous"])}</span>'
            f'<strong>{_esc(title)}</strong></a>'
        )
    if index + 1 < len(book["chapters"]):
        following = book["chapters"][index + 1]
        href = _rel(url, _guide_url(locale, following["slug"])) or "./"
        title = following["locales"][locale["id"]]["title"]
        next_html = (
            f'<a class="pager-link pager-next" href="{href}"><span>{_esc(labels["next"])}</span>'
            f'<strong>{_esc(title)}</strong></a>'
        )
    return previous_html, next_html


def _rail(book: dict, locale: dict, url: str, current: str) -> str:
    """The book's whole tree: one paragraph per part, one link per chapter."""
    out = []
    for part in book["parts"]:
        out.append(f'<p class="rail-part">{_esc(_text(part["title"], locale))}</p>')
        out.append('<ul class="rail-list">')
        for chapter in book["chapters"]:
            if chapter["part"] != part["id"]:
                continue
            href = _rel(url, _guide_url(locale, chapter["slug"])) or "./"
            on = chapter["slug"] == current
            marker = ' class="rail-link is-current" aria-current="page"' if on else ' class="rail-link"'
            caption = chapter["locales"][locale["id"]]["nav"]
            out.append(f'<li><a{marker} href="{href}">{_esc(caption)}</a></li>')
        out.append("</ul>")
    return "\n".join(out)


def _edit_url(chapter: dict, locale: dict) -> str:
    return f"{REPO_URL}/blob/main/site/guide/{chapter['slug']}.{locale['id']}.html"


def _rel_file(url: str, target: str) -> str:
    return posixpath.relpath(target, url.rstrip("/") or ".")


def _relative_search(url: str, search_url: str) -> str:
    """Relative address of the search index, version query kept."""
    path, _, query = search_url.partition("?")
    return _rel_file(url, path) + (f"?{query}" if query else "")


def _search_index(book: dict, locale: dict, scans: dict) -> dict:
    """One locale's searchable text, written next to the guide.

    The browser searches this file instead of the DOM: the whole book is
    indexed once at build time, so a result can point at a section of a
    chapter the reader has not opened.
    """
    chapters = []
    for chapter in book["chapters"]:
        side = chapter["locales"][locale["id"]]
        sections = []
        for section in scans[chapter["slug"]][locale["id"]].sections:
            text = " ".join(" ".join(section["text"]).split())
            sections.append({"id": section["id"], "title": section["title"], "text": text[:600]})
        chapters.append(
            {
                "slug": chapter["slug"],
                "title": side["title"],
                "url": "" if chapter["slug"] == "overview" else chapter["slug"] + "/",
                "summary": side["summary"],
                "sections": sections,
            }
        )
    return {"locale": locale["id"], "chapters": chapters}


def _jsonld(book: dict, locale: dict, chapter: dict, canonical: str) -> dict:
    side = chapter["locales"][locale["id"]]
    author = {"@type": "Organization", "name": "option-berth", "url": REPO_URL}
    if chapter["slug"] == "overview":
        return {
            "@context": "https://schema.org",
            "@type": "Book",
            "name": side["title"],
            "description": side["lede"],
            "url": canonical,
            "inLanguage": locale["lang"],
            "author": author,
            "hasPart": [
                {
                    "@type": "Chapter",
                    "name": other["locales"][locale["id"]]["title"],
                    "url": SITE + _guide_url(locale, other["slug"]),
                }
                for other in book["chapters"]
                if other["slug"] != "overview"
            ],
        }
    guide_home = next(item for item in book["chapters"] if item["slug"] == "overview")
    return {
        "@context": "https://schema.org",
        "@type": "TechArticle",
        "headline": side["title"],
        "description": side["lede"],
        "url": canonical,
        "inLanguage": locale["lang"],
        "author": author,
        "isPartOf": {
            "@type": "Book",
            "name": guide_home["locales"][locale["id"]]["title"],
            "url": SITE + _guide_url(locale, "overview"),
        },
    }


def _asset_versions() -> dict[str, str]:
    """Short content hash per published asset.

    GitHub Pages caches /assets/* for four hours, so an unversioned stylesheet
    URL hands a returning reader yesterday's CSS with today's markup. The hash
    makes every change a new URL; the query is ignored by the server.
    """
    return {
        name: hashlib.sha256((ROOT / "assets" / name).read_bytes()).hexdigest()[:8]
        for name in ASSETS
    }


def _version_assets(page: str, root: str, versions: dict[str, str]) -> str:
    for name, version in versions.items():
        page = page.replace(f"{root}assets/{name}", f"{root}assets/{name}?v={version}")
    return page


def _script_hash(value: str) -> str:
    digest = hashlib.sha256(value.encode("utf-8")).digest()
    return "'sha256-" + base64.b64encode(digest).decode("ascii") + "'"


def _render_page(book: dict, locale: dict, chapter: dict, content: str, search_url: str) -> str:
    labels = book["labels"][locale["id"]]
    side = chapter["locales"][locale["id"]]
    url = _guide_url(locale, chapter["slug"])
    canonical = SITE + url
    home = chapter["slug"] == "overview"
    page_title = f"{side['title']} · {labels['footer_line']}" if home else f"{side['title']} · option-berth"
    jsonld = json.dumps(_jsonld(book, locale, chapter, canonical), ensure_ascii=False, separators=(",", ":"))
    jsonld = jsonld.replace("<", "\\u003c")
    root = _root_prefix(url)
    alternates = "".join(
        f'<link rel="alternate" hreflang="{_esc(item["lang"])}" href="{_esc(SITE + _guide_url(item, chapter["slug"]))}">'
        for item in book["locales"]
    )
    alternates += f'<link rel="alternate" hreflang="x-default" href="{_esc(SITE + _guide_url(book["locales"][0], chapter["slug"]))}">'
    language_menu = "".join(
        f'<li><a href="{_rel(url, _guide_url(item, chapter["slug"])) or "./"}" lang="{_esc(item["lang"])}"'
        f'{" aria-current=\"true\"" if item["id"] == locale["id"] else ""}>{_esc(item["label"])}</a></li>'
        for item in book["locales"]
    )
    values = {
        "lang": _esc(locale["lang"]),
        "page_title": _esc(page_title),
        "description": _esc(side["lede"]),
        "canonical": _esc(canonical),
        "alternates": alternates,
        "script_hash": _script_hash(jsonld),
        "jsonld": jsonld,
        "asset_path": root + "assets/",
        "home_path": root or "./",
        "guide_home": _rel(url, _guide_url(locale, "overview")) or "./",
        "language_menu": language_menu,
        "locale_label": _esc(locale["label"]),
        "search_path": _esc(_relative_search(url, search_url)),
        "nav_guide": _esc(labels["nav_guide"]),
        "nav_product": _esc(labels["nav_product"]),
        "nav_github": _esc(labels["nav_github"]),
        "skip": _esc(labels["skip"]),
        "navigation": _esc(labels["navigation"]),
        "footer_navigation": _esc(labels["footer_navigation"]),
        "language": _esc(labels["language"]),
        "menu": _esc(labels["menu"]),
        "close": _esc(labels["close"]),
        "search_open": _esc(labels["search_open"]),
        "search_placeholder": _esc(labels["search_placeholder"]),
        "search_hint": _esc(labels["search_hint"]),
        "search_empty": _esc(labels["search_empty"]),
        "search_results": _esc(labels["search_results"]),
        "theme_light": _esc(labels["theme_light"]),
        "theme_dark": _esc(labels["theme_dark"]),
        "copy": _esc(labels["copy"]),
        "copied": _esc(labels["copied"]),
        "og_type": "website" if home else "article",
        "og_locale": _esc(locale["og_locale"]),
        "root_prefix": root,
        "footer_line": _esc(labels["footer_line"]),
        "footer_note": _esc(labels["footer_note"]),
        "foot_guide": _esc(labels["foot_guide"]),
        "foot_contract": _esc(labels["foot_contract"]),
        "foot_products": _esc(labels["foot_products"]),
        "foot_machine": _esc(labels["foot_machine"]),
        "foot_overview": _esc(labels["foot_overview"]),
        "foot_install": _esc(labels["foot_install"]),
        "foot_status": _esc(labels["foot_status"]),
        "foot_cli": _esc(labels["foot_cli"]),
        "foot_product": _esc(labels["foot_product"]),
        "foot_integrations": _esc(labels["foot_integrations"]),
        "foot_work_records": _esc(labels["foot_work_records"]),
        "foot_engine": _esc(labels["foot_engine"]),
        "foot_client": _esc(labels["foot_client"]),
        "foot_skill": _esc(labels["foot_skill"]),
        "url_overview": _rel(url, _guide_url(locale, "overview")) or "./",
        "url_install": _rel(url, _guide_url(locale, "install")) or "./",
        "url_status": _rel(url, _guide_url(locale, "status")) or "./",
        "content": content,
    }
    page = GUIDE_TEMPLATE.substitute(values)
    if "{{" in page:
        raise ValueError(f"guide book: {url} still carries an unresolved placeholder")
    return _version_assets(page, root, _asset_versions())


def _llms_txt(book: dict) -> str:
    default = book["locales"][0]
    lines = [
        "# option-berth",
        "",
        "> option-berth (oberth) is the runtime facts layer for Git worktrees: what a checkout should run, whether it really is running, and why not.",
        "",
        "oberth keeps one chain, worktree → manifest → service → run → runtime facts, so a person and a coding agent read the same answer. The CLI is the only interface. Every command has a `--json` shape, and JSON stdout holds exactly one value.",
        "",
        "This guide is the long-form explanation. The authoritative contract for the CLI, JSON fields and the manifest is in the repository's docs/ directory (see Contract below).",
        "",
        f"## Guide ({default['label']})",
    ]
    for chapter in book["chapters"]:
        side = chapter["locales"][default["id"]]
        lines.append(f"- [{side['title']}]({SITE}{_guide_url(default, chapter['slug'])}): {side['summary']}")
    for locale in book["locales"][1:]:
        lines += ["", f"## Guide ({locale['label']})"]
        for chapter in book["chapters"]:
            side = chapter["locales"][locale["id"]]
            lines.append(f"- [{side['title']}]({SITE}{_guide_url(locale, chapter['slug'])}): {side['summary']}")
    lines += [
        "",
        "## Contract",
        f"- [CLI contract]({REPO_URL}/blob/main/docs/cli.md): the command surface, JSON fields, error codes and manifest references.",
        f"- [Product definition]({REPO_URL}/blob/main/docs/product.md): the need, the object model, write boundaries and what the product refuses to do.",
        f"- [Install and uninstall]({REPO_URL}/blob/main/docs/install.md): every installation channel, upgrade, removal and platform boundary.",
        f"- [Integrating orchestration tools]({REPO_URL}/blob/main/docs/integrations.md): recipes for Conductor, Cursor, Claude Code and Codex, plus the AGENTS.md section.",
        f"- [Work records]({REPO_URL}/blob/main/docs/workplace.md): the workplace commands, block fields and their boundaries.",
        f"- [Architecture]({REPO_URL}/blob/main/docs/agent/architecture.md): implementation boundaries and the chain of facts.",
        "",
        "## Project",
        f"- [Source]({REPO_URL}): the engine, examples and site in one repository.",
        f"- [Releases]({REPO_URL}/releases): signed-archive checksums and platform archives.",
        "- [macOS client](https://github.com/SheathedSharp/option-berth-macos): a separate product; it presents the same facts and grows no capability the CLI lacks.",
        "- [Coding-agent skill](https://github.com/SheathedSharp/option-berth-skill): a separate product; it teaches an agent the same CLI and is not distributed from this repository.",
        "",
        "## Optional",
        f"- [Full guide text]({SITE}llms-full.txt): every English chapter in one plain-text file.",
        f"- [Sitemap]({SITE}sitemap.xml): every page, in both languages.",
    ]
    return "\n".join(lines) + "\n"


def _llms_full(book: dict) -> str:
    default = book["locales"][0]
    parts = [
        "# option-berth guide",
        "",
        "> option-berth (oberth) is the runtime facts layer for Git worktrees: what a checkout should run, whether it really is running, and why not.",
        "",
        f"Canonical HTML: {SITE}{_guide_url(default, 'overview')}",
        *(
            f"{item['label']} edition: {SITE}{_guide_url(item, 'overview')}"
            for item in book["locales"][1:]
        ),
        "",
    ]
    for chapter in book["chapters"]:
        side = chapter["locales"][default["id"]]
        raw = (GUIDE / f"{chapter['slug']}.{default['id']}.html").read_text(encoding="utf-8")
        raw = raw.replace("{{root}}", SITE).replace("{{guide}}", SITE + _guide_url(default, "overview"))
        extractor = _TextExtract()
        extractor.feed(raw)
        extractor.close()
        parts += ["---", "", f"URL: {SITE}{_guide_url(default, chapter['slug'])}", ""]
        # The lede is the page's meta description; the chapter's own intro opens
        # the text, so printing both here only repeats the same sentences.
        parts += [f"# {side['title']}", ""]
        parts += [extractor.text(), ""]
    return "\n".join(parts).strip() + "\n"


def _sitemap(book: dict) -> str:
    urls = [SITE, SITE + "en/"]
    for chapter in book["chapters"]:
        for locale in book["locales"]:
            urls.append(SITE + _guide_url(locale, chapter["slug"]))
    body = "".join(f"<url><loc>{html.escape(url)}</loc></url>" for url in urls)
    return (
        '<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">'
        + body
        + "</urlset>\n"
    )


class _LinkScan(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.hrefs = []
        self.ids = set()

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if attrs.get("id"):
            self.ids.add(attrs["id"])
        if tag == "a" and attrs.get("href"):
            self.hrefs.append(attrs["href"])


def _check_site_links(dist: Path) -> None:
    parsed = {}

    def parse(path: Path) -> _LinkScan:
        if path not in parsed:
            scan = _LinkScan()
            scan.feed(path.read_text(encoding="utf-8"))
            scan.close()
            parsed[path] = scan
        return parsed[path]

    errors = []
    for page in sorted(dist.rglob("*.html")):
        scan = parse(page)
        for href in scan.hrefs:
            if href.startswith(FOREIGN_HREF):
                continue
            path_part, _, fragment = href.partition("#")
            path_part = path_part.partition("?")[0]
            if not path_part:
                if fragment and fragment not in scan.ids:
                    errors.append(f"{page.relative_to(dist)}: #{fragment} does not exist")
                continue
            target = (page.parent / path_part).resolve()
            if path_part.endswith("/") or target.is_dir():
                target = target / "index.html"
            if not target.is_file():
                errors.append(f"{page.relative_to(dist)}: {href} does not exist")
                continue
            if fragment and target.suffix == ".html" and fragment not in parse(target).ids:
                errors.append(f"{page.relative_to(dist)}: {href} names a missing id")
    if errors:
        raise ValueError("site links:\n" + "\n".join(errors))


def _write_legacy_redirects(dist: Path, book: dict) -> None:
    """Keep the guide's first English URLs alive.

    The guide used to live at /guide/en/ before English became the default
    edition at /guide/. Those addresses are published in llms.txt, the sitemap
    and other people's bookmarks, so each one forwards instead of 404ing.
    """
    if any(str(item["prefix"]).startswith("en") for item in book["locales"]):
        return
    default = book["locales"][0]
    for chapter in book["chapters"]:
        slug = chapter["slug"]
        legacy = "guide/en/" + ("" if slug == "overview" else slug + "/")
        url = _guide_url(default, slug)
        target = _rel(legacy, url)
        label = f"{chapter['locales'][default['id']]['title']} · option-berth"
        page = (
            "<!doctype html>\n"
            f'<html lang="{_esc(default["lang"])}">\n<head>\n<meta charset="utf-8">\n'
            '<meta name="robots" content="noindex">\n'
            f'<meta http-equiv="refresh" content="0; url={_esc(target)}">\n'
            f'<link rel="canonical" href="{_esc(SITE + url)}">\n'
            f"<title>{_esc(label)}</title>\n</head>\n<body>\n"
            f'<p><a href="{_esc(target)}">{_esc(label)}</a></p>\n</body>\n</html>\n'
        )
        path = dist / legacy / "index.html"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(page, encoding="utf-8")


def _render_guide(dist: Path) -> dict:
    book = load_book()
    locale_ids = [item["id"] for item in book["locales"]]
    scans = {}
    for chapter in book["chapters"]:
        scans[chapter["slug"]] = {}
        for locale_id in locale_ids:
            fragment = (GUIDE / f"{chapter['slug']}.{locale_id}.html").read_text(encoding="utf-8")
            scan = _scan(fragment)
            _check_fragment(scan, chapter["slug"], locale_id)
            scans[chapter["slug"]][locale_id] = scan
        first = _structure(scans[chapter["slug"]][locale_ids[0]])
        for locale_id in locale_ids[1:]:
            if _structure(scans[chapter["slug"]][locale_id]) != first:
                names = " and ".join(f"{chapter['slug']}.{item}.html" for item in (locale_ids[0], locale_id))
                raise ValueError(
                    f"guide book: {names} do not mirror each other (headings, block counts or code blocks)"
                )

    # The search index is written before the pages so each page can name its
    # own copy: a version in the URL keeps a browser from serving yesterday's
    # index against today's chapters.
    searches = {}
    (dist / "guide").mkdir(parents=True, exist_ok=True)
    for locale in book["locales"]:
        payload = json.dumps(_search_index(book, locale, scans), ensure_ascii=False, separators=(",", ":"))
        version = hashlib.sha256(payload.encode("utf-8")).hexdigest()[:8]
        name = f"search-{locale['id']}.json"
        (dist / "guide" / name).write_text(payload, encoding="utf-8")
        searches[locale["id"]] = f"guide/{name}?v={version}"

    for index, chapter in enumerate(book["chapters"]):
        for locale in book["locales"]:
            locale_id = locale["id"]
            labels = book["labels"][locale_id]
            side = chapter["locales"][locale_id]
            url = _guide_url(locale, chapter["slug"])
            fragment = (GUIDE / f"{chapter['slug']}.{locale_id}.html").read_text(encoding="utf-8")
            body = _resolve(fragment, locale, url)
            scan = _scan(body)
            previous_link, next_link = _chapter_nav(book, locale, chapter, index, labels)
            part = next(item for item in book["parts"] if item["id"] == chapter["part"])
            content = CHAPTER_TEMPLATE.substitute(
                {
                    "rail": _rail(book, locale, url, chapter["slug"]),
                    "toc_title": _esc(labels["toc_title"]),
                    "breadcrumb": _esc(labels["breadcrumb"]),
                    "nav_guide": _esc(labels["nav_guide"]),
                    "guide_home": _rel(url, _guide_url(locale, "overview")) or "./",
                    "part_title": _esc(_text(part["title"], locale)),
                    "title": _esc(side["title"]),
                    "body": body,
                    "page_toc": _page_toc(scan),
                    "on_this_page": _esc(labels["on_this_page"]),
                    "contract_title": _esc(labels["contract_title"]),
                    "refs": _refs(chapter, locale),
                    "foot_machine": _esc(labels["foot_machine"]),
                    "machine_note": _esc(labels["machine_note"]),
                    "root_prefix": _root_prefix(url),
                    "chapter_nav": _esc(labels["chapter_nav"]),
                    "prev_link": previous_link,
                    "next_link": next_link,
                    "edit_url": _esc(_edit_url(chapter, locale)),
                    "edit_page": _esc(labels["edit_page"]),
                }
            )
            page = _render_page(book, locale, chapter, content, searches[locale_id])
            target = dist / url / "index.html"
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(page, encoding="utf-8")

    _write_legacy_redirects(dist, book)
    (dist / "llms.txt").write_text(_llms_txt(book), encoding="utf-8")
    (dist / "llms-full.txt").write_text(_llms_full(book), encoding="utf-8")
    (dist / "sitemap.xml").write_text(_sitemap(book), encoding="utf-8")
    (dist / "robots.txt").write_text(
        "User-agent: *\nAllow: /\nSitemap: "
        + SITE
        + "sitemap.xml\n# Agent guide: "
        + SITE
        + "llms.txt\n"
    )
    _check_site_links(dist)
    return book


def build() -> Path:
    template = Template((ROOT / "templates/index.html").read_text())
    locales = json.loads((ROOT / "locales.json").read_text())
    if set(locales) != {"zh-CN", "en"} or set(locales["zh-CN"]) != set(locales["en"]):
        raise ValueError("Both locales must provide the same complete content")
    measured = snapshot_values(load_snapshot())
    versions = _asset_versions()
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
        # Locale strings may name snapshot fields ($proof_run_count). Resolve
        # them before escaping so the page never carries a literal placeholder.
        values = {
            key: html.escape(Template(value).safe_substitute(measured), quote=True)
            for key, value in translated.items()
        }
        values.update(lang=lang, site_url=SITE, canonical=SITE + ("en/" if english else ""),
                      asset_path="../assets/" if english else "assets/", home_path="./",
                      language_path="../" if english else "en/", other_lang="zh-CN" if english else "en",
                      language_label="中文" if english else "EN",
                      guide_path="../guide/" if english else "guide/zh/")
        values.update({key: html.escape(value, quote=True) for key, value in measured.items()})
        document = template.substitute(values)
        document = _version_assets(document, "../" if english else "", versions)
        target = dist / "en" / "index.html" if english else dist / "index.html"
        target.parent.mkdir(exist_ok=True)
        target.write_text(document)
    (dist / ".nojekyll").write_text("")
    book = _render_guide(dist)
    print(f"Built zh-CN and en engine pages, {len(book['chapters']) * 2} guide pages, llms.txt and llms-full.txt")
    return dist


if __name__ == "__main__":
    build()
