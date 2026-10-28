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
TERMINAL_GO = HERE.parent / "engine/internal/cmd/mark_gen.go"
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


# 终端首屏的标记：和 SVG 共用同一份轮廓，不另画一份。每格 4×4 采样后按覆盖率
# 取实心，上下两个半格合成一个字符行，字符格的宽高比因此和 24×24 网格一致。
TERMINAL_COLUMNS = 14
TERMINAL_SAMPLES = 4
TERMINAL_COVERAGE = 0.5
CURVE_STEPS = 24
TERMINAL_BLOCKS = {(True, True): "█", (True, False): "▀", (False, True): "▄", (False, False): " "}
RAIL, TOKEN = 1, 2


def outline(commands: list) -> list[tuple[float, float]]:
    """把一条封闭轮廓展平成折线，供栅格采样使用。"""
    points: list[tuple[float, float]] = []
    current = start = (0.0, 0.0)
    for op, values in commands:
        if op == "move":
            current = start = (values[0], values[1])
            points.append(current)
        elif op == "line":
            current = (values[0], values[1])
            points.append(current)
        elif op == "curve":
            x1, y1, x2, y2, x3, y3 = values
            x0, y0 = current
            for step in range(1, CURVE_STEPS + 1):
                t = step / CURVE_STEPS
                u = 1 - t
                points.append((u**3 * x0 + 3 * u * u * t * x1 + 3 * u * t * t * x2 + t**3 * x3,
                               u**3 * y0 + 3 * u * u * t * y1 + 3 * u * t * t * y2 + t**3 * y3))
            current = (x3, y3)
    points.append(start)
    return points


def covered(shapes: list, x: float, y: float) -> bool:
    """偶奇规则：点是否落在标记的任一封闭体内。"""
    inside = False
    for points in shapes:
        for (ax, ay), (bx, by) in zip(points, points[1:]):
            if (ay > y) != (by > y) and ax + (y - ay) * (bx - ax) / (by - ay) > x:
                inside = not inside
    return inside


def coverage(shape: list, left: float, top: float, width: float, height: float) -> float:
    hits = 0
    for row in range(TERMINAL_SAMPLES):
        for column in range(TERMINAL_SAMPLES):
            if covered([shape], left + (column + 0.5) * width / TERMINAL_SAMPLES,
                       top + (row + 0.5) * height / TERMINAL_SAMPLES):
                hits += 1
    return hits / TERMINAL_SAMPLES**2


def terminal_mark(paths: dict, columns: int = TERMINAL_COLUMNS) -> tuple[str, str]:
    """把标准标记栅格成终端方块字符，返回 (轨, 胶囊) 两层。

    两层形状相同——每格一个字符，另一层在该格留空格——调用方据此给两个形体上不同
    颜色。品牌本来就是这个分工：轨是墨色/纸色，胶囊是陶土色；涂成一色时胶囊会和
    两个轨帽并成"三个一样的块"，看起来像一张脸。

    栅格贴合标记自身的外接矩形，并让行高除得尽，使顶边和底边正好落在字符格边界上；
    否则圆角底边会被切成一条悬空的半高横条，像影子一样挂在本体下面。
    """
    if columns % 2:
        raise ValueError("终端标记的列数必须是偶数")
    rail, token = outline(paths["standardRail"]), outline(paths["standardToken"])
    xs = [x for shape in (rail, token) for x, _ in shape]
    ys = [y for shape in (rail, token) for _, y in shape]
    left, top = min(xs), min(ys)
    width, height = max(xs) - left, max(ys) - top
    cell = width / columns
    rows = math.ceil(height / cell)
    if rows % 2:
        rows += 1
    step = height / rows  # 行高单独取，行数取偶数才能让上下边界对齐
    owner = []
    for row in range(rows):
        line = []
        for column in range(columns):
            x0, y0 = left + column * cell, top + row * step
            rail_cov = coverage(rail, x0, y0, cell, step)
            token_cov = coverage(token, x0, y0, cell, step)
            if max(rail_cov, token_cov) < TERMINAL_COVERAGE:
                line.append(0)
            else:
                line.append(TOKEN if token_cov >= rail_cov else RAIL)
        owner.append(line)
    layers = {RAIL: [], TOKEN: []}
    for row in range(0, rows, 2):
        for layer in (RAIL, TOKEN):
            chars = []
            for column in range(columns):
                pair = (owner[row][column], owner[row + 1][column])
                filled = pair[0] != 0 or pair[1] != 0
                mine = layer in pair
                chars.append(TERMINAL_BLOCKS[(pair[0] != 0, pair[1] != 0)] if filled and mine else " ")
            layers[layer].append("".join(chars))
    # 两层一起裁掉上下空行和公共缩进。这里不能对单层做 rstrip：两层必须逐格对齐，
    # 谁短一格都会让调用方把颜色涂到错的格子上。
    blank = [not (layers[RAIL][i].strip() or layers[TOKEN][i].strip())
             for i in range(len(layers[RAIL]))]
    first = next((i for i, empty in enumerate(blank) if not empty), None)
    if first is None:
        raise ValueError("终端标记为空")
    last = max(i for i, empty in enumerate(blank) if not empty)
    indent = min(len(layers[layer][i]) - len(layers[layer][i].lstrip())
                 for layer in (RAIL, TOKEN) for i in range(first, last + 1)
                 if layers[layer][i].strip())
    return tuple("\n".join(line[indent:] for line in layers[layer][first:last + 1]) + "\n"
                 for layer in (RAIL, TOKEN))


def build_terminal_go(paths: dict | None = None) -> str:
    """生成 CLI 首屏用的 Go 常量；由同一份几何源派生。"""
    if paths is None:
        paths, _ = read_geometry()
    rail, token = terminal_mark(paths)
    return ("// Code generated by brand/build-option-berth.py from\n"
            "// client/macos/Sources/BerthGeometry.swift; DO NOT EDIT.\n"
            "\n"
            "package cmd\n"
            "\n"
            "// The brand mark on the bare-`oberth` screen: the 24x24 outline the client's\n"
            "// BerthMark and the brand SVGs share, rasterised into blocks. The two shapes\n"
            "// are separate layers with the same shape — one rune per cell, spaces where\n"
            "// the other shape owns the cell — so each can be drawn in its own colour.\n"
            "const (\n"
            "	markRail = `" + rail + "`\n"
            "	markToken = `" + token + "`\n"
            ")\n")


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
    parser.add_argument("--go-output", type=Path, default=TERMINAL_GO,
                        help="CLI 首屏标记的 Go 常量输出路径")
    args = parser.parse_args()
    try:
        paths, _ = read_geometry()
        targets = {args.output / name: text for name, text in build_assets().items()}
        targets[args.go_output] = build_terminal_go(paths)
        if args.check:
            stale = [str(path) for path, text in targets.items()
                     if not path.is_file()
                     or path.read_text(encoding="utf-8") != text]
            if stale:
                print("导出缺失或过期：" + ", ".join(stale), file=sys.stderr)
                return 1
            print(f"OK: {len(targets)} 个导出与客户端几何源一致")
        else:
            args.output.mkdir(parents=True, exist_ok=True)
            for path, text in targets.items():
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(text, encoding="utf-8")
            print(f"已写出 {len(targets)} 个导出")
        return 0
    except (OSError, ValueError, ET.ParseError) as error:
        print(f"品牌导出失败：{error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
