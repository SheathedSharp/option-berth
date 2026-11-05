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
# Each station of the chain links to the chapter that explains it.
STATION_CHAPTER = {
    "worktree": "many",
    "manifest": "declare",
    "service": "up",
    "run": "logs",
    "facts": "status",
}
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


def _guide_url(lang: str, slug: str) -> str:
    base = "guide/" if lang == "zh-CN" else "guide/en/"
    return base if slug == "overview" else base + slug + "/"


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

    labels = book.get("labels") or {}
    if set(labels) != {"zh", "en"} or set(labels["zh"]) != set(labels["en"]):
        raise ValueError("guide book: labels must carry the same keys in both languages")
    for locale in ("zh", "en"):
        for key, value in labels[locale].items():
            if not str(value).strip():
                raise ValueError(f"guide book: labels.{locale}.{key} is empty")

    parts = book.get("parts") or []
    chapters = book.get("chapters") or []
    stations = book.get("stations") or []
    if not parts or not chapters:
        raise ValueError("guide book: parts and chapters may not be empty")
    part_ids = [part.get("id") for part in parts]
    if len(part_ids) != len(set(part_ids)) or None in part_ids:
        raise ValueError("guide book: part ids must be unique")
    slugs = [chapter.get("slug") for chapter in chapters]
    if len(slugs) != len(set(slugs)) or None in slugs:
        raise ValueError("guide book: chapter slugs must be unique")
    if "overview" not in slugs:
        raise ValueError("guide book: the overview chapter is required")
    station_ids = [station.get("id") for station in stations]
    if set(station_ids) != set(STATION_CHAPTER) or len(station_ids) != len(set(station_ids)):
        raise ValueError("guide book: stations must match the chain")
    for chapter in chapters:
        slug = chapter["slug"]
        if chapter.get("part") not in part_ids:
            raise ValueError(f"guide book: {slug} names an unknown part")
        if chapter.get("station") not in [None, *station_ids]:
            raise ValueError(f"guide book: {slug} names an unknown station")
        for locale in ("zh", "en"):
            side = chapter.get(locale) or {}
            for key in ("nav", "title", "lede", "summary"):
                if not str(side.get(key) or "").strip():
                    raise ValueError(f"guide book: {slug}.{locale}.{key} is empty")
            fragment = GUIDE / f"{slug}.{locale}.html"
            if not fragment.is_file():
                raise ValueError(f"guide book: missing chapter fragment {fragment.name}")
        for item in chapter.get("refs") or []:
            if not str(item.get("href") or "").startswith("https://"):
                raise ValueError(f"guide book: {slug} has a non-https contract link")
    for target in STATION_CHAPTER.values():
        if target not in slugs:
            raise ValueError(f"guide book: station target {target} does not exist")
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

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if attrs.get("id"):
            self.ids.append(attrs["id"])
        if tag in ("h1", "h2", "h3", "h4"):
            self._heading = int(tag[1])
            self._heading_id = attrs.get("id")
            self._heading_text = []
            self.heading_levels.append(self._heading)
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
            self._heading = None
        if tag == "pre" and self._pre:
            self._pre -= 1
            self.code_blocks.append("".join(self._pre_text))

    def handle_data(self, data):
        if self._heading is not None:
            self._heading_text.append(data)
        if self._pre:
            self._pre_text.append(data)


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
    for heading_id, text in h2s:
        if not heading_id:
            raise ValueError(f"guide book: {slug}.{locale}.html has an h2 without an id")
        if not text:
            raise ValueError(f"guide book: {slug}.{locale}.html has an empty h2")
    if len({heading_id for heading_id, _ in h2s}) != len(h2s):
        raise ValueError(f"guide book: {slug}.{locale}.html repeats an h2 id")
    if slug == "overview":
        h1s = [item for item in scan.headings if item[0] == 1]
        if len(h1s) != 1:
            raise ValueError("guide book: the overview needs exactly one h1")


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


def _resolve(fragment: str, lang: str, url: str) -> str:
    guide = _rel(url, _guide_url(lang, "overview"))
    return fragment.replace("{{guide}}", guide).replace("{{root}}", _root_prefix(url))


def _esc(value: object) -> str:
    return html.escape(str(value), quote=True)


def _toc_compact(book: dict, lang: str, url: str, current: str) -> str:
    locale = "zh" if lang == "zh-CN" else "en"
    out = ['<nav class="book-toc" aria-label="' + _esc(book["labels"][locale]["toc_title"]) + '">']
    for part in book["parts"]:
        out.append(f'<p class="toc-part">{_esc(part[locale])}</p><ol class="toc-list">')
        for chapter in book["chapters"]:
            if chapter["part"] != part["id"]:
                continue
            href = _rel(url, _guide_url(lang, chapter["slug"])) or "./"
            marker = ' class="is-current" aria-current="page"' if chapter["slug"] == current else ""
            out.append(f'<li><a href="{href}"{marker}>{_esc(chapter[locale]["nav"])}</a></li>')
        out.append("</ol>")
    out.append("</nav>")
    return "\n".join(out)


def _toc_full(book: dict, lang: str, url: str) -> str:
    locale = "zh" if lang == "zh-CN" else "en"
    out = ['<div class="contents-parts">']
    for part in book["parts"]:
        out.append(f'<section class="contents-part"><h3>{_esc(part[locale])}</h3><ol class="contents-list">')
        for chapter in book["chapters"]:
            if chapter["part"] != part["id"]:
                continue
            href = _rel(url, _guide_url(lang, chapter["slug"])) or "./"
            out.append(
                f'<li><a href="{href}"><span class="contents-title">{_esc(chapter[locale]["title"])}</span> '
                f'<span class="contents-summary">{_esc(chapter[locale]["summary"])}</span></a></li>'
            )
        out.append("</ol></section>")
    out.append("</div>")
    return "\n".join(out)


def _station_links(book: dict, lang: str, url: str, current: str | None) -> str:
    locale = "zh" if lang == "zh-CN" else "en"
    out = []
    for station in book["stations"]:
        target = _guide_url(lang, STATION_CHAPTER[station["id"]])
        href = _rel(url, target) or "./"
        on = station["id"] == current
        marker = ' class="rail-station is-current" aria-current="true"' if on else ' class="rail-station"'
        out.append(
            f'<li><a{marker} href="{href}"><span class="rail-capsule" aria-hidden="true"></span>'
            f'<span>{_esc(station[locale])}</span></a></li>'
        )
    return "\n".join(out)


def _page_toc(scan: _FragmentScan) -> str:
    return "\n".join(
        f'<li><a href="#{_esc(heading_id)}">{_esc(text)}</a></li>'
        for level, heading_id, text in scan.headings
        if level == 2
    )


def _refs(chapter: dict, locale: str) -> str:
    return "\n".join(
        f'<li><a href="{_esc(item["href"])}">{_esc(item[locale])}</a></li>'
        for item in chapter.get("refs") or []
    )


def _chapter_nav(book: dict, lang: str, chapter: dict, index: int, labels: dict) -> tuple[str, str]:
    locale = "zh" if lang == "zh-CN" else "en"
    url = _guide_url(lang, chapter["slug"])
    previous_html = ""
    next_html = ""
    if index > 0:
        previous = book["chapters"][index - 1]
        href = _rel(url, _guide_url(lang, previous["slug"])) or "./"
        previous_html = (
            f'<a class="chapter-link chapter-prev" href="{href}"><span>{_esc(labels["previous"])}</span>'
            f'<strong>{_esc(previous[locale]["title"])}</strong></a>'
        )
    if index + 1 < len(book["chapters"]):
        following = book["chapters"][index + 1]
        href = _rel(url, _guide_url(lang, following["slug"])) or "./"
        next_html = (
            f'<a class="chapter-link chapter-next" href="{href}"><span>{_esc(labels["next"])}</span>'
            f'<strong>{_esc(following[locale]["title"])}</strong></a>'
        )
    return previous_html, next_html


def _jsonld(book: dict, lang: str, chapter: dict, canonical: str) -> dict:
    locale = "zh" if lang == "zh-CN" else "en"
    author = {"@type": "Organization", "name": "option-berth", "url": REPO_URL}
    if chapter["slug"] == "overview":
        return {
            "@context": "https://schema.org",
            "@type": "Book",
            "name": chapter[locale]["title"],
            "description": chapter[locale]["lede"],
            "url": canonical,
            "inLanguage": lang,
            "author": author,
            "hasPart": [
                {
                    "@type": "Chapter",
                    "name": other[locale]["title"],
                    "url": SITE + _guide_url(lang, other["slug"]),
                }
                for other in book["chapters"]
                if other["slug"] != "overview"
            ],
        }
    guide_home = next(item for item in book["chapters"] if item["slug"] == "overview")
    return {
        "@context": "https://schema.org",
        "@type": "TechArticle",
        "headline": chapter[locale]["title"],
        "description": chapter[locale]["lede"],
        "url": canonical,
        "inLanguage": lang,
        "author": author,
        "isPartOf": {
            "@type": "Book",
            "name": guide_home[locale]["title"],
            "url": SITE + _guide_url(lang, "overview"),
        },
    }


def _script_hash(value: str) -> str:
    digest = hashlib.sha256(value.encode("utf-8")).digest()
    return "'sha256-" + base64.b64encode(digest).decode("ascii") + "'"


def _render_page(book: dict, lang: str, chapter: dict, content: str) -> str:
    locale = "zh" if lang == "zh-CN" else "en"
    labels = book["labels"][locale]
    url = _guide_url(lang, chapter["slug"])
    other_lang = "en" if lang == "zh-CN" else "zh-CN"
    other_url = _guide_url(other_lang, chapter["slug"])
    canonical = SITE + url
    home = chapter["slug"] == "overview"
    title = chapter[locale]["title"]
    page_title = f"{title} · {labels['footer_line']}" if home else f"{title} · option-berth"
    jsonld = json.dumps(_jsonld(book, lang, chapter, canonical), ensure_ascii=False, separators=(",", ":"))
    jsonld = jsonld.replace("<", "\\u003c")
    root = _root_prefix(url)
    values = {
        "lang": lang,
        "page_title": _esc(page_title),
        "description": _esc(chapter[locale]["lede"]),
        "canonical": _esc(canonical),
        "alternate_zh": _esc(SITE + _guide_url("zh-CN", chapter["slug"])),
        "alternate_en": _esc(SITE + _guide_url("en", chapter["slug"])),
        "script_hash": _script_hash(jsonld),
        "jsonld": jsonld,
        "asset_path": root + "assets/",
        "home_path": root or "./",
        "guide_home": _rel(url, _guide_url(lang, "overview")) or "./",
        "language_path": _rel(url, other_url) or "./",
        "other_lang": other_lang,
        "language_label": "中文" if lang == "en" else "EN",
        "nav_guide": _esc(labels["nav_guide"]),
        "nav_product": _esc(labels["nav_product"]),
        "nav_github": _esc(labels["nav_github"]),
        "skip": _esc(labels["skip"]),
        "navigation": _esc(labels["navigation"]),
        "footer_navigation": _esc(labels["footer_navigation"]),
        "og_type": "website" if home else "article",
        "og_locale": "zh_CN" if lang == "zh-CN" else "en",
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
        "url_overview": _rel(url, _guide_url(lang, "overview")) or "./",
        "url_install": _rel(url, _guide_url(lang, "install")) or "./",
        "url_status": _rel(url, _guide_url(lang, "status")) or "./",
        "content": content,
    }
    page = GUIDE_TEMPLATE.substitute(values)
    if "{{" in page:
        raise ValueError(f"guide book: {url} still carries an unresolved placeholder")
    return page


def _llms_txt(book: dict) -> str:
    lines = [
        "# option-berth",
        "",
        "> option-berth (oberth) is the runtime facts layer for Git worktrees: what a checkout should run, whether it really is running, and why not.",
        "",
        "oberth keeps one chain — worktree → manifest → service → run → runtime facts — so a person and a coding agent read the same answer. The CLI is the only interface; every command has a `--json` shape, and JSON stdout holds exactly one value.",
        "",
        "This guide is the long-form explanation. The authoritative contract for the CLI, JSON fields and the manifest is in the repository's docs/ directory (see Contract below).",
        "",
        "## Guide",
    ]
    for chapter in book["chapters"]:
        side = chapter["en"]
        lines.append(f"- [{side['title']}]({SITE}{_guide_url('en', chapter['slug'])}): {side['summary']}")
    lines += ["", "## 中文指南"]
    for chapter in book["chapters"]:
        side = chapter["zh"]
        lines.append(f"- [{side['title']}]({SITE}{_guide_url('zh-CN', chapter['slug'])}): {side['summary']}")
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
    parts = [
        "# option-berth guide",
        "",
        "> option-berth (oberth) is the runtime facts layer for Git worktrees: what a checkout should run, whether it really is running, and why not.",
        "",
        f"Canonical HTML: {SITE}guide/en/",
        f"Chinese edition: {SITE}guide/",
        "",
    ]
    for chapter in book["chapters"]:
        raw = (GUIDE / f"{chapter['slug']}.en.html").read_text(encoding="utf-8")
        raw = raw.replace("{{root}}", SITE).replace("{{guide}}", SITE + "guide/en/")
        if chapter["slug"] == "overview":
            raw = raw.replace("<div data-book-contents></div>", _toc_full(book, "en", _guide_url("en", "overview")))
        extractor = _TextExtract()
        extractor.feed(raw)
        extractor.close()
        parts += ["---", "", f"URL: {SITE}{_guide_url('en', chapter['slug'])}", ""]
        if chapter["slug"] != "overview":
            parts += [f"# {chapter['en']['title']}", "", chapter["en"]["lede"], ""]
        parts += [extractor.text(), ""]
    return "\n".join(parts).strip() + "\n"


def _sitemap(book: dict) -> str:
    urls = [SITE, SITE + "en/"]
    for chapter in book["chapters"]:
        for lang in ("zh-CN", "en"):
            urls.append(SITE + _guide_url(lang, chapter["slug"]))
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


def _render_guide(dist: Path) -> dict:
    book = load_book()
    scans = {}
    for chapter in book["chapters"]:
        scans[chapter["slug"]] = {}
        for locale in ("zh", "en"):
            fragment = (GUIDE / f"{chapter['slug']}.{locale}.html").read_text(encoding="utf-8")
            scan = _scan(fragment)
            _check_fragment(scan, chapter["slug"], locale)
            scans[chapter["slug"]][locale] = scan
        if _structure(scans[chapter["slug"]]["zh"]) != _structure(scans[chapter["slug"]]["en"]):
            raise ValueError(
                f"guide book: {chapter['slug']}.zh.html and {chapter['slug']}.en.html do not mirror "
                "each other (headings, block counts or code blocks)"
            )

    for index, chapter in enumerate(book["chapters"]):
        for lang in ("zh-CN", "en"):
            locale = "zh" if lang == "zh-CN" else "en"
            labels = book["labels"][locale]
            url = _guide_url(lang, chapter["slug"])
            fragment = (GUIDE / f"{chapter['slug']}.{locale}.html").read_text(encoding="utf-8")
            body = _resolve(fragment, lang, url)
            if chapter["slug"] == "overview":
                marker = "<div data-book-contents></div>"
                if body.count(marker) != 1:
                    raise ValueError("guide book: the overview needs exactly one contents placeholder")
                content = body.replace(marker, _toc_full(book, lang, url))
            else:
                scan = _scan(body)
                previous_link, next_link = _chapter_nav(book, lang, chapter, index, labels)
                content = CHAPTER_TEMPLATE.substitute(
                    {
                        "title": _esc(chapter[locale]["title"]),
                        "lede": _esc(chapter[locale]["lede"]),
                        "body": body,
                        "station_links": _station_links(book, lang, url, chapter.get("station")),
                        "toc": _toc_compact(book, lang, url, chapter["slug"]),
                        "toc_title": _esc(labels["toc_title"]),
                        "chain_title": _esc(labels["chain_title"]),
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
                    }
                )
            page = _render_page(book, lang, chapter, content)
            target = dist / url / "index.html"
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(page, encoding="utf-8")

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
                      guide_path="../guide/en/" if english else "guide/")
        values.update({key: html.escape(value, quote=True) for key, value in measured.items()})
        document = template.substitute(values)
        target = dist / "en" / "index.html" if english else dist / "index.html"
        target.parent.mkdir(exist_ok=True)
        target.write_text(document)
    (dist / ".nojekyll").write_text("")
    book = _render_guide(dist)
    print(f"Built zh-CN and en engine pages, {len(book['chapters']) * 2} guide pages, llms.txt and llms-full.txt")
    return dist


if __name__ == "__main__":
    build()
