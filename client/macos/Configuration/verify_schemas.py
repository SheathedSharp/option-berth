#!/usr/bin/env python3
"""Validate shipped schemas and compare a synthetic corpus with the Swift parser.

Run with a prebuilt ClientSchemaChecks executable. No real user files, remote
references or model processes are read/executed. jsonschema is test-only.
"""
from __future__ import annotations
import argparse
import json
from pathlib import Path
import subprocess
from jsonschema import Draft202012Validator

ROOT = Path(__file__).resolve().parent


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("swift_parser", type=Path)
    args = parser.parse_args()
    schemas = {kind: json.loads((ROOT / f"{kind}.schema.json").read_text()) for kind in ("theme", "settings", "keybindings")}
    for schema in schemas.values():
        Draft202012Validator.check_schema(schema)
    validators = {kind: Draft202012Validator(schema) for kind, schema in schemas.items()}
    cases = []

    def case(kind, value, valid, label, schema_valid=None):
        cases.append((kind, json.dumps(value, ensure_ascii=False), valid, valid if schema_valid is None else schema_valid, label))

    for kind in schemas:
        for value, valid in [({}, False), ([], False), ({"schemaVersion": 1}, True), ({"schemaVersion": 2}, False),
                             ({"schemaVersion": True}, False), ({"schemaVersion": "1"}, False), ({"schemaVersion": 1, "typo": 0}, False)]:
            case(kind, value, valid, "document shape/version")
        for metadata, valid in [("file:///fixture/local.schema.json", True), ("https://invalid.example/not-fetched", True),
                                (None, False), (7, False), ("", False), ("x\n", False)]:
            case(kind, {"schemaVersion": 1, "$schema": metadata}, valid, "inert metadata")
    for key in ["sidebarWidth", "servicesDetailWidth", "gitDetailWidth"]:
        low, high = (140, 320) if key == "sidebarWidth" else (340, 500)
        for value, valid in [(low, True), (high, True), (low + .5, True), (None, True),
                             (low - 1, False), (high + 1, False), (True, False), (str(low), False), ([], False)]:
            case("settings", {"schemaVersion": 1, key: value}, valid, key)
    for key in ("reduceMotion", "shellIntegration"):
        for value in (None, True, False, 0, 1, "true"):
            case("settings", {"schemaVersion": 1, key: value}, value is None or type(value) is bool, key)
    for value in (None, "codex", "claude", "opencode", "deepseek", "pi", "unknown", []):
        case("settings", {"schemaVersion": 1, "defaultAgent": value}, value is None or isinstance(value, str) and value in ("codex", "claude", "opencode", "deepseek", "pi"), "provider")
    for key in ("interfaceScale", "dataScale", "logScale"):
        for value in (None, .75, 1.2, 2, .74, 2.01, True, "1"):
            valid = value is None or type(value) in (int, float) and .75 <= value <= 2
            case("theme", {"schemaVersion": 1, key: value}, valid, key)
    for value, valid in [("#12abEF", True), ("#12345", False), ("123456", False), ("#123456\n", False), (None, False)]:
        case("theme", {"schemaVersion": 1, "colors": {"accent": value}}, valid, "color syntax")
    case("theme", {"schemaVersion": 1, "colors": {"unknown": "#123456"}}, False, "unknown color")
    case("theme", {"schemaVersion": 1, "colors": None}, True, "unset palette")
    for key in ("interfaceFont", "dataFont"):
        for value, valid in [(None, True), ("__system__", True), ("Monaspace Neon", True), ("字体", True),
                             ("", False), ("x\n", False), ("x\u0085", False), ("x" * 129, False)]:
            case("theme", {"schemaVersion": 1, key: value}, valid, "font")
        # Standard maxLength counts Unicode scalar values, not UTF-8 bytes.
        # Runtime resource limits are intentionally stricter and documented.
        case("theme", {"schemaVersion": 1, key: "字" * 64}, False, "font UTF-8 byte cap", schema_valid=True)
    for binding, valid in [({"key": "j", "option": True}, True), ({"key": "1"}, True),
                           ({"key": "j", "shift": None}, True), ({"key": "j", "shfit": True}, False),
                           ({"key": "j", "option": 1}, False), ({"key": "J"}, False), ({"key": "jj"}, False),
                           ({"key": ""}, False), ({}, False), (None, False)]:
        case("keybindings", {"schemaVersion": 1, "bindings": {"services": binding}}, valid, "binding shape")
    for count in (64, 65):
        case("keybindings", {"schemaVersion": 1, "bindings": {str(n): {"key": "j"} for n in range(count)}}, count == 64, "binding limit (structural, not effective-map validation)")
    for kind, schema in schemas.items():
        example = ROOT / f"{kind}.example.json"
        if example.exists():
            case(kind, json.loads(example.read_text()), True, "shipped example")
    case("theme", json.loads((ROOT.parent / "Fixtures/theme-dark.json").read_text()), True, "dark fixture")

    inputs = [{"kind": kind, "raw": raw} for kind, raw, *_ in cases]
    result = subprocess.run([str(args.swift_parser.resolve())], input=json.dumps(inputs), text=True,
                            capture_output=True, timeout=20, check=True)
    swift = json.loads(result.stdout)
    assert len(swift["accepted"]) == len(cases), "missing Swift results"
    for kind, schema in schemas.items():
        assert set(schema["properties"]) == set(swift["keys"][kind]), f"{kind}: runtime/schema key drift"
    assert set(schemas["theme"]["properties"]["colors"]["properties"]) == set(swift["colors"]), "theme token drift"
    for index, (kind, raw, valid, schema_valid, label) in enumerate(cases):
        assert validators[kind].is_valid(json.loads(raw)) == schema_valid, f"{kind}/{label}: schema mismatch at {index}"
        assert swift["accepted"][index] == valid, f"{kind}/{label}: Swift mismatch at {index}"
    print(f"Configuration schemas: 3 valid schemas, {len(cases)} reference/Swift cases and exact key/token coverage passed")


if __name__ == "__main__":
    main()
