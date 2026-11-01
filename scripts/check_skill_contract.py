#!/usr/bin/env python3
"""Check that the agent skill covers the engine's agent-facing contract.

The skill is an independent product, but it teaches the same CLI, error codes,
manifest fields, references, and agent-facing JSON fields. This check extracts
those anchors from the contract's canonical sources — docs/cli.md (the English
authoritative side of the pair), the generated manifest schema, and the
observation state constants — and requires each one to
appear in the skill checkout. It does not judge prose; it catches a contract
change that never reached the skill.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DEFAULT_CLI = ROOT / "docs" / "cli.md"
DEFAULT_SCHEMA = ROOT / "engine" / "docs" / "schema" / "manifest.schema.json"
DEFAULT_OBSERVATION = ROOT / "engine" / "internal" / "state" / "observation.go"
DEFAULT_SKILL = ROOT.parent / "option-berth-skill"

# Facts that live in prose, not in the schema or a constant. Each anchor is
# also checked against docs/cli.md so a rename cannot silently leave this list
# pointing at text the product no longer publishes.
QUALITATIVE_ANCHORS = (
    ("observation.state", "the published observation status"),
    ("health_status", "the service health fact"),
    ("manifest_runtime_mismatch", "a run that predates the current manifest"),
    ("oberth serve --detach", "starting a daemon outside an agent sandbox"),
    ("--only listener_observation", "the doctor self-probe"),
    ("BERTH_STRICT_MANIFEST", "strict unknown-key parsing"),
    ("worktree.identity", "the stable identity published by status"),
    ("oberth worktrees", "the read-only identity ledger"),
    ("--identity", "stopping a checkout that no longer exists"),
    ("oberth start", "the low-level entry that expands addresses"),
)


def manifest_key_pattern(key: str) -> re.Pattern[str]:
    """Match a manifest field where it is used as a field, not as a word.

    Bare substring matching let a `workspace:` field count as covered by the
    unrelated `--workspace` subscription flag. Require a field name, a YAML
    `key:`, or a quoted key.
    """
    return re.compile(
        rf"(?:`{re.escape(key)}`|(?<![\w-]){re.escape(key)}\s*:|\"{re.escape(key)}\")"
    )


def stable_error_codes(cli_text: str) -> list[str]:
    """Read the stable data.code strings from the CLI contract sentence."""
    match = re.search(r"The stable codes are (.+?)\.(?:\s|$)", cli_text, re.S)
    if match is None:
        raise ValueError("docs/cli.md: no sentence listing the stable error codes")
    # The list is comma-separated with an English `and` before the last item.
    # Splitting on both keeps `and` out of the codes themselves; the Chinese
    # side separates with `、` and needs no such cut.
    codes = [
        token.strip()
        for token in re.split(r",|\band\b", match.group(1))
        if re.fullmatch(r"[a-z][a-z0-9_]+", token.strip())
    ]
    if not codes:
        raise ValueError("docs/cli.md: the stable error-code sentence names no codes")
    return codes


def _section(text: str, heading: str) -> str:
    start = text.find(heading)
    if start < 0:
        raise ValueError(f"docs/cli.md: no section {heading}")
    rest = text[start + len(heading):]
    end = re.search(r"\n#{2,3} ", rest)
    return rest[: end.start()] if end else rest


def reference_tokens(cli_text: str) -> list[str]:
    """Read the manifest reference table, not examples in surrounding prose."""
    tokens: list[str] = []
    for line in _section(cli_text, "### Manifest references").splitlines():
        if not line.startswith("|") or "${" not in line:
            continue
        tokens.extend(re.findall(r"`(\$\{[^`]+\})`", line))
    if not tokens:
        raise ValueError("docs/cli.md: the manifest reference table names no references")
    return tokens


def json_field_anchors(cli_text: str) -> list[str]:
    """Read the agent-facing JSON field table, not fields named in prose."""
    fields: list[str] = []
    for line in _section(cli_text, "### Agent-facing JSON fields").splitlines():
        if not line.startswith("|"):
            continue
        cells = [cell.strip() for cell in line.strip().strip("|").split("|")]
        match = re.fullmatch(r"`([^`]+)`", cells[0]) if cells else None
        if match is not None:
            fields.append(match.group(1))
    fields = list(dict.fromkeys(fields))
    if not fields:
        raise ValueError("docs/cli.md: the agent JSON field table names no fields")
    return fields


def manifest_anchors(schema: dict) -> list[str]:
    """Keys an agent must know to draft or diagnose a manifest."""
    keys = set(schema.get("properties", {}))
    defs = schema.get("$defs", {})
    for name in ("Service", "MachineRef"):
        properties = defs.get(name, {}).get("properties", {})
        keys |= set(properties)
        for variant in properties.get("health", {}).get("anyOf", []):
            keys |= set(variant.get("properties", {}))
    return sorted(keys)


def observation_states(go_text: str) -> list[str]:
    """The observation.state values published by status and daemon.hello."""
    return sorted(set(re.findall(r'Observation\w+\s*=\s*"([a-z_]+)"', go_text)))


def build_anchors(cli_text: str, schema: dict, observation_go: str) -> list[tuple[str, str]]:
    anchors: list[tuple[str, str]] = []
    anchors.extend(("error code", code) for code in stable_error_codes(cli_text))
    anchors.extend(("manifest key", key) for key in manifest_anchors(schema))
    anchors.extend(("reference", token) for token in reference_tokens(cli_text))
    anchors.extend(("observation state", state) for state in observation_states(observation_go))
    anchors.extend(("json field", field) for field in json_field_anchors(cli_text))
    anchors.extend(("qualitative anchor", anchor) for anchor, _ in QUALITATIVE_ANCHORS)
    return list(dict.fromkeys(anchors))


def validate_qualitative_sources(cli_text: str) -> None:
    missing = [anchor for anchor, _ in QUALITATIVE_ANCHORS if anchor not in cli_text]
    if missing:
        raise ValueError(
            "docs/cli.md no longer contains qualitative anchor(s): " + ", ".join(missing)
        )


def read_skill(skill_dir: Path) -> tuple[str, list[Path]]:
    skill = skill_dir / "SKILL.md"
    references = sorted((skill_dir / "references").glob("*.md"))
    if not skill.is_file():
        raise ValueError(f"{skill_dir}: no SKILL.md (pass --skill-dir or set OPTION_BERTH_SKILL_DIR)")
    if not references:
        raise ValueError(f"{skill_dir}: no references/*.md files")
    files = [skill, *references]
    return "\n".join(path.read_text(encoding="utf-8") for path in files), files


def anchor_present(kind: str, anchor: str, skill_text: str) -> bool:
    if kind == "manifest key":
        return manifest_key_pattern(anchor).search(skill_text) is not None
    # A `.*` anchor names a field group ("services[].stats.*"), not a literal
    # key: the skill cannot spell every leaf, and the contract table already
    # fixes the group name. Accept the group name on its own or the full path,
    # so the skill can write `stats` without repeating the parent path.
    if anchor.endswith(".*"):
        group = anchor[:-2].rsplit(".", 1)[-1].replace("[]", "")
        return group in skill_text or anchor in skill_text
    return anchor in skill_text


def missing_anchors(anchors: list[tuple[str, str]], skill_text: str) -> list[tuple[str, str]]:
    return [
        (kind, anchor)
        for kind, anchor in anchors
        if not anchor_present(kind, anchor, skill_text)
    ]


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--skill-dir", type=Path)
    parser.add_argument("--cli", type=Path, default=DEFAULT_CLI)
    parser.add_argument("--schema", type=Path, default=DEFAULT_SCHEMA)
    parser.add_argument("--observation", type=Path, default=DEFAULT_OBSERVATION)
    args = parser.parse_args(argv)

    skill_dir = args.skill_dir
    if skill_dir is None and os.environ.get("OPTION_BERTH_SKILL_DIR"):
        skill_dir = Path(os.environ["OPTION_BERTH_SKILL_DIR"])
    if skill_dir is None:
        skill_dir = DEFAULT_SKILL

    try:
        cli_text = args.cli.read_text(encoding="utf-8")
        schema = json.loads(args.schema.read_text(encoding="utf-8"))
        observation_go = args.observation.read_text(encoding="utf-8")
        validate_qualitative_sources(cli_text)
        anchors = build_anchors(cli_text, schema, observation_go)
        skill_text, files = read_skill(skill_dir)
    except (OSError, ValueError, json.JSONDecodeError) as error:
        print(f"skill contract check failed: {error}", file=sys.stderr)
        return 1

    missing = missing_anchors(anchors, skill_text)
    if missing:
        print(
            f"skill contract check failed: {len(missing)} of {len(anchors)} anchors "
            f"are not covered by {skill_dir}",
            file=sys.stderr,
        )
        for kind, anchor in missing:
            print(f"  {kind}: {anchor}", file=sys.stderr)
        print(
            "Add these contract items to SKILL.md or references/ in the skill repository.",
            file=sys.stderr,
        )
        return 1

    print(f"skill contract: {len(anchors)} anchors covered by {len(files)} files in {skill_dir}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
