#!/usr/bin/env python3
"""从客户端的唯一几何源导出 SVG；只依赖 Python 标准库。

python3 brand/build-option-berth.py          更新 SVG
python3 brand/build-option-berth.py --check  只检查，不写文件
python3 brand/build-option-berth.py --output /tmp/oberth-brand

应用 PNG/ICNS 仍由 SwiftUI --write-icon / iconutil 生成，不由本脚本替代。
"""
from __future__ import annotations

import argparse
import math
from pathlib import Path
import re
import sys
import xml.etree.ElementTree as ET

HERE = Path(__file__).resolve().parent
SOURCE = HERE.parent / "client/macos/Sources/BerthGeometry.swift"
NAMES = ("standardRail", "standardToken", "compactRail", "compactToken")
ARITY = {"move": 2, "line": 2, "curve": 6, "close": 0}
SVG_COMMAND = {"move": "M", "line": "L", "curve": "C", "close": "Z"}
NS = "http://www.w3.org/2000/svg"
ET.register_namespace("", NS)


def read_geometry(source: Path = SOURCE) -> tuple[dict, dict]:
    text = source.read_text(encoding="utf-8")
    paths = {}
    for name in NAMES:
        match = re.search(rf"static let {name}: \[Command\] = \[(.*?)\n    \]", text, re.S)
        if match is None:
            raise ValueError(f"缺少几何数组 {name}")
        commands = []
        for line in match.group(1).splitlines():
            line = line.strip()
            if not line or line.startswith("//"):
                continue
            command = re.fullmatch(r"\.(move|line|curve|close)(?:\(([^()]*)\))?,", line)
            if command is None:
                raise ValueError(f"{name}: 无法解析 {line!r}")
            op, args = command.groups()
            values = tuple(float(value.strip()) for value in args.split(",")) if args else ()
            if len(values) != ARITY[op] or not all(math.isfinite(value) for value in values):
                raise ValueError(f"{name}: 非法命令 {line!r}")
            commands.append((op, values))
        if not commands or commands[0][0] != "move" or commands[-1][0] != "close":
            raise ValueError(f"{name}: 必须是封闭轮廓")
        if sum(op == "move" for op, _ in commands) != 1:
            raise ValueError(f"{name}: 每形必须只有一个封闭体")
        paths[name] = commands
    constants = {}
    for name in ("ink", "paper", "accent", "darkAccent"):
        match = re.search(rf"static let {name}: UInt32 = 0x([0-9A-Fa-f]{{6}})\b", text)
        if match is None:
            raise ValueError(f"缺少色值 {name}")
        constants[name] = "#" + match.group(1).upper()
    for name in ("iconInset", "iconRadius", "iconMarkScale"):
        match = re.search(rf"static let {name}: Double = ([0-9.]+)\b", text)
        if match is None:
            raise ValueError(f"缺少图标参数 {name}")
        value = float(match.group(1))
        if not 0 < value < 1:
            raise ValueError(f"非法图标参数 {name}")
        constants[name] = value
    return paths, constants


def number(value: float) -> str:
    return f"{value:.6f}".rstrip("0").rstrip(".") or "0"


def path_data(commands: list) -> str:
    return " ".join(SVG_COMMAND[op] + " ".join(number(value) for value in values)
                    for op, values in commands)


def mark(paths: dict, rail: str, token: str, compact: bool = False) -> str:
    prefix = "compact" if compact else "standard"
    return (f'<path fill="{rail}" d="{path_data(paths[prefix + "Rail"])}"/>'
            f'<path fill="{token}" d="{path_data(paths[prefix + "Token"])}"/>')


def svg(content: str, viewbox: str = "0 0 24 24") -> str:
    return (f'<svg xmlns="{NS}" viewBox="{viewbox}" role="img" aria-label="option-berth">\n'
            '<title>option-berth · 各自成泊</title>\n'
            '<!-- Generated from BerthGeometry.swift; do not edit the mark by hand. -->\n'
            + content + '\n</svg>\n')


def icon(paths: dict, palette: dict, dark: bool = False, mono: bool = False) -> str:
    rail = palette["paper"] if dark else palette["ink"]
    token = rail if mono else palette["darkAccent"] if dark else palette["accent"]
    inset = palette["iconInset"] * 1024
    radius = palette["iconRadius"] * 1024
    size = palette["iconMarkScale"] * 1024
    origin = (1024 - size) / 2
    tile = palette["ink"] if dark else palette["paper"]
    return svg(f'<rect x="{number(inset)}" y="{number(inset)}" '
               f'width="{number(1024 - 2 * inset)}" height="{number(1024 - 2 * inset)}" '
               f'rx="{number(radius)}" fill="{tile}"/>'
               f'<g transform="translate({number(origin)} {number(origin)}) scale({number(size / 24)})">'
               + mark(paths, rail, token) + '</g>', "0 0 1024 1024")


def wordmark(rail: str, token: str) -> str:
    root = ET.fromstring((HERE / "wordmark.svg").read_text(encoding="utf-8"))
    result = []
    for node in root:
        if node.tag != f"{{{NS}}}path":
            continue
        role = node.attrib.pop("data-role", "letters")
        if role not in ("letters", "token"):
            raise ValueError(f"未知字标角色 {role}")
        node.set("fill", token if role == "token" else rail)
        result.append(ET.tostring(node, encoding="unicode").replace(f' xmlns="{NS}"', ""))
    if len(result) != 3:
        raise ValueError("字标必须由 option / 胶囊 / berth 三段轮廓组成")
    return "".join(result)


def build_assets() -> dict[str, str]:
    paths, palette = read_geometry()
    ink, paper, accent, dark_accent = (palette[k] for k in ("ink", "paper", "accent", "darkAccent"))
    variants = {
        "light": (ink, accent, False),
        "dark": (paper, dark_accent, False),
        "mono": ("currentColor", "currentColor", False),
        "empty-light": (ink, "#B9C1CE", False),
        "empty-dark": (paper, "#4A5262", False),
        "small-light": (ink, accent, True),
        "small-dark": (paper, dark_accent, True),
    }
    assets = {f"option-berth-mark-{name}.svg": svg(mark(paths, *args))
              for name, args in variants.items()}
    assets["option-berth-icon-light.svg"] = icon(paths, palette)
    assets["option-berth-icon-dark.svg"] = icon(paths, palette, dark=True)
    assets["option-berth-icon-ink.svg"] = icon(paths, palette, dark=True, mono=True)
    for name, rail, token in (("light", ink, accent), ("dark", paper, dark_accent),
                               ("mono", "currentColor", "currentColor")):
        content = '<g transform="scale(6)">' + mark(paths, rail, token) + '</g>' + wordmark(rail, token)
        assets[f"option-berth-lockup-{name}.svg"] = svg(content, "0 0 573.79498 144")
    return assets


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="检查导出与代码一致，不写文件")
    parser.add_argument("--output", type=Path, default=HERE, help="SVG 输出目录")
    args = parser.parse_args()
    try:
        assets = build_assets()
        if args.check:
            stale = [name for name, text in assets.items()
                     if not (args.output / name).is_file()
                     or (args.output / name).read_text(encoding="utf-8") != text]
            if stale:
                print("导出缺失或过期：" + ", ".join(stale), file=sys.stderr)
                return 1
            print(f"OK: {len(assets)} 个 SVG 与客户端几何源一致")
        else:
            args.output.mkdir(parents=True, exist_ok=True)
            for name, text in assets.items():
                (args.output / name).write_text(text, encoding="utf-8")
            print(f"已写出 {len(assets)} 个 SVG 到 {args.output}")
        return 0
    except (OSError, ValueError, ET.ParseError) as error:
        print(f"品牌导出失败：{error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
