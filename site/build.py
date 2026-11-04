#!/usr/bin/env python3
"""Build two static pages with standard-library tooling and allowlisted assets."""
from pathlib import Path
from string import Template
from datetime import datetime
import html
import json
import os
import shutil

ROOT = Path(__file__).resolve().parent
# The canonical host is a build input: GitHub Pages remains the default preview,
# while the public site is served from Cloudflare Pages on its own domain.
SITE = os.environ.get("SITE_URL", "https://oberth.zayju.de/").strip()
if not SITE.startswith(("https://", "http://")):
    raise ValueError("SITE_URL must be an absolute http(s) URL")
if not SITE.endswith("/"):
    SITE += "/"
ASSETS = ("icon.svg", "mark.svg", "mark-dark.svg", "boot.js", "site.css", "site.js")
SNAPSHOT = ROOT / "data" / "eval.json"
SNAPSHOT_SCHEMA = "oberth.site-eval/v1"
ARMS = ("oberth", "native")


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
                      language_label="中文" if english else "EN")
        values.update({key: html.escape(value, quote=True) for key, value in measured.items()})
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
