#!/usr/bin/env python3
"""option-berth · 标志 —— 按 ⌥ 真字符的实测比例（终版）

在 font-size 420 下量到的实测值（不是估算）：
  可见外框 350 × 304 px；笔宽 31 px（= 可见宽的 8.86%）
  斜线斜率正好 2.0（63.4°）；左上横中心线 50.5→143.5；右上横 258.5→368.5；
  底横 277→368.5；两段顶横的断口 84 px（= 2.7 个笔宽）
换算：可见宽 350 px ↔ 21 单位 → 笔宽 1.86

三色分工：陆地（暖炭）／河与水（群青，一笔）／船（陶土，向下的梯形）；底为象牙白。
三块之间留一道气口（0.55），谁也不贴着谁 —— 拼接处就不会生硬。
"""
import os, math, subprocess
from PIL import Image

OUT = os.path.dirname(os.path.abspath(__file__))
K = 21.0 / 350.0
X = lambda px: round(1.5 + (px - 35) * K, 3)
Y = lambda py: round(2.88 + (py - 61) * K, 3)

W, WS, WI = 1.86, 2.15, 2.00
AIR = 0.55
LY, LYB = Y(76), Y(348.5)
L1X0, L1X1 = X(50.5), X(143.5)
L2X0, L2X1 = X(258.5), X(368.5)
EBX, EBY = X(277), LYB
B1X1 = X(368.5)
ANG = math.degrees(math.atan2(EBY - LY, EBX - L1X1))

PAL = {
    "light": dict(bg="#FAF7F1", land="#23262B", water="#2F5AA8", boat="#C0703F"),
    "dark":  dict(bg="#191817", land="#F7F3EA", water="#7C9BE0", boat="#DE8C60"),
}


def _u():
    dx, dy = EBX - L1X1, EBY - LY
    L = math.hypot(dx, dy)
    return dx / L, dy / L


def land(c, w=W):
    return (f'<path d="M {L1X0} {LY} L {L1X1} {LY}" stroke="{c}" stroke-width="{w}" '
            f'stroke-linecap="round" fill="none"/>')


def river(c, w=W):
    """一笔：斜线 → 肘（圆角转折）→ 底横。起笔整体沿斜线退开一个气口。"""
    ux, uy = _u()
    d = AIR + w
    kx, ky = L1X1 + ux * d, LY + uy * d
    return (f'<path d="M {kx:.3f} {ky:.3f} L {EBX} {EBY} L {B1X1} {LYB}" stroke="{c}" '
            f'stroke-width="{w}" stroke-linecap="round" stroke-linejoin="round" fill="none"/>')


def _rpoly(pts, r):
    """圆角多边形（做船体梯形）"""
    n = len(pts)
    out = []
    for i in range(n):
        p0, p1, p2 = pts[i - 1], pts[i], pts[(i + 1) % n]
        v1 = (p0[0] - p1[0], p0[1] - p1[1]); v2 = (p2[0] - p1[0], p2[1] - p1[1])
        l1, l2 = math.hypot(*v1), math.hypot(*v2)
        u1 = (v1[0] / l1, v1[1] / l1); u2 = (v2[0] / l2, v2[1] / l2)
        ca = max(-1, min(1, u1[0] * u2[0] + u1[1] * u2[1]))
        t = min(r / math.tan(math.acos(ca) / 2), l1 / 2, l2 / 2)
        a = (p1[0] + u1[0] * t, p1[1] + u1[1] * t)
        b = (p1[0] + u2[0] * t, p1[1] + u2[1] * t)
        out.append(("M " if i == 0 else "L ") + f"{a[0]:.3f} {a[1]:.3f}")
        out.append(f"Q {p1[0]:.3f} {p1[1]:.3f} {b[0]:.3f} {b[1]:.3f}")
    return " ".join(out) + " Z"


BOAT_IN, BOAT_D, BOAT_R = 1.05, 1.30, 0.50


def boat(c, w=W):
    """船：向下的梯形（宽顶窄底 = 船体剪影）。顶边与长度仍照 ⌥ 右上横的实测值。"""
    top, bot = LY - w / 2, LY - w / 2 + w * BOAT_D
    pts = [(L2X0, top), (L2X1, top), (L2X1 - BOAT_IN, bot), (L2X0 + BOAT_IN, bot)]
    return f'<path d="{_rpoly(pts, BOAT_R)}" fill="{c}"/>' 


def mark(th, w=W):
    return land(th["land"], w) + river(th["water"], w) + boat(th["boat"], w)


def svg(th, size=96, w=W):
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="{size}" '
            f'height="{size}" role="img" aria-label="option-berth">{mark(th, w)}</svg>')


def mark_file(th, w=W, note=""):
    return ('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" role="img" '
            'aria-label="option-berth">\n  <!-- option-berth · 标志' + note + '\n'
            f'       照 Option 字符的实测比例：笔宽 {w}（可见宽的 8.86%）、斜线斜率 2.0（{ANG:.1f}°）、\n'
            f'       左上横 {L1X0}->{L1X1}、右上横 {L2X0}->{L2X1}、底横 -> {B1X1}，'
            f'两段顶横断口 = 2.7 个笔宽。\n'
            f'       底 象牙白 #FAF7F1；陆地 #23262B / 河与水 #2F5AA8 / 船 #C0703F；气口 {AIR}。 -->\n  '
            + mark(th, w) + '\n</svg>\n')


ICON_RX, ICON_PCT = 228, 0.62


def icon(th, size=1024, plate=None, w=WI):
    plate = plate or th["bg"]
    vw = (B1X1 + w / 2) - (L1X0 - w / 2)
    vh = (LYB + w / 2) - (LY - w / 2)
    cx, cy = (L1X0 - w / 2 + B1X1 + w / 2) / 2, (LY - w / 2 + LYB + w / 2) / 2
    k = size * ICON_PCT / vw
    tx, ty = size / 2 - cx * k, size / 2 - cy * k
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {size} {size}" width="{size}" '
            f'height="{size}" role="img" aria-label="option-berth">'
            f'<rect width="{size}" height="{size}" rx="{ICON_RX / 1024 * size:.1f}" fill="{plate}"/>'
            f'<g transform="translate({tx:.2f} {ty:.2f}) scale({k:.4f})">{mark(th, w)}</g></svg>')


def mark_svg_variant(th, w=W, boat=None, name=""):
    """任意配色的裸标：给单色档、空态档用"""
    t = dict(th)
    if boat:
        t["boat"] = boat
    return ('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" role="img" '
            'aria-label="option-berth">' + mark(t, w) + '</svg>')


def write(name, content):
    with open(os.path.join(OUT, name), "w") as f:
        f.write(content)
    return name


INK_ICON = {**PAL["light"], "land": PAL["light"]["bg"]}
files = [write("option-berth-mark-light.svg", mark_file(PAL["light"])),
         write("option-berth-mark-dark.svg", mark_file(PAL["dark"])),
         write("option-berth-mark-small-light.svg", mark_file(PAL["light"], WS, "（小尺寸档）")),
         write("option-berth-mark-small-dark.svg", mark_file(PAL["dark"], WS, "（小尺寸档）")),
         write("option-berth-icon-light.svg", icon(PAL["light"])),
         write("option-berth-icon-dark.svg", icon(PAL["dark"])),
         write("option-berth-icon-ink.svg", icon(INK_ICON, plate=PAL["light"]["land"]))]

MONO = dict(land="#23262B", water="#23262B", boat="#23262B")
write("option-berth-mark-mono.svg", mark_svg_variant(MONO))
write("option-berth-mark-empty-light.svg",
      mark_svg_variant(PAL["light"], boat="#B9C1CE"))
write("option-berth-mark-empty-dark.svg",
      mark_svg_variant(PAL["dark"], boat="#4A5262"))


RSVG = "/opt/homebrew/bin/rsvg-convert"
for src, dst, px in [("option-berth-mark-light.svg", "option-berth-mark-light.png", 700),
                     ("option-berth-mark-dark.svg", "option-berth-mark-dark.png", 700),
                     ("option-berth-icon-light.svg", "option-berth-icon-light-1024.png", 1024),
                     ("option-berth-icon-dark.svg", "option-berth-icon-dark-1024.png", 1024),
                     ("option-berth-icon-ink.svg", "option-berth-icon-ink-1024.png", 1024)]:
    subprocess.run([RSVG, "-w", str(px), "-h", str(px), "-o", os.path.join(OUT, dst),
                    os.path.join(OUT, src)], check=True)

# ---- 提案页 -----------------------------------------------------------------
# 字体跟仓库走，不写死谁的家目录：原来那条指着 `~/code/sonar-client/…` ——
# 一个早于分叉的旧检出名，那目录早就不在了，提案页一直是拿默认字体渲的。
FONT = "../client/macos/Fonts/MonaspaceNeon-SemiBold.otf"


def ladder(th, sizes=(16, 20, 24, 32, 48, 64, 96, 128), w=W):
    return '<div class="ladder">' + "".join(
        f'<div class="lc">{svg(th, s, w)}<i>{s}</i></div>' for s in sizes) + '</div>'


def lockup(th, fs=46, k=1.3):
    hp = (LYB + WI / 2) - (LY - WI / 2)
    return (f'<div class="lock"><span>{svg(th, int(round(fs * 0.72 * k / hp * 24)), WI)}</span>'
            f'<span class="lt" style="color:{th["land"]};font-size:{fs}px">option-berth</span></div>')


def sec(th, label, body):
    return f'<section style="background:{th["bg"]}"><div class="lab">{label}</div>{body}</section>'


def anatomy(T):
    return ('<div class="anat">' + "".join(
        f'<figure><svg viewBox="0 0 24 24" width="132" height="132">{b}</svg>'
        f'<figcaption><b>{t}</b><span>{d}</span></figcaption></figure>' for t, b, d in (
            ("1 陆地", land(T["land"]), "深中性，左上一条横 —— 字符里的左上横原样"),
            ("2 河与水", river(T["water"]), "蓝，一笔：斜线（斜率 2.0）转角到底横"),
            ("3 船", boat(T["boat"]), "暖色，向下的梯形：宽顶窄底，长度与位置照右上横的实测值"),
            ("= 三块合起来", mark(T), "两处断口都是气口，谁也不贴着谁"),
        )) + '</div>')


parts = []
for T, tag in ((PAL["light"], "浅色系"), (PAL["dark"], "深色系")):
    parts.append(sec(T, f"{tag} · 标志", f'<div class="hero">'
                     f'<div style="width:270px">{svg(T, 270)}</div>{lockup(T)}</div>'))
    parts.append(sec(T, f"{tag} · 尺寸阶梯（笔宽 {W}）", ladder(T)))
    parts.append(sec(T, f"{tag} · 应用图标",
                     f'<div class="hero">'
                     f'<div class="plate"><div style="width:150px">{icon(T, 150)}</div></div>'
                     f'<div class="plate"><div style="width:150px">'
                     f'{icon(INK_ICON, 150, plate=PAL["light"]["land"])}</div></div>'
                     f'<div class="col">' + "".join(
                         f'<div style="width:{x}px">{icon(INK_ICON, x, plate=PAL["light"]["land"])}</div>'
                         for x in (128, 64, 32)) + '</div></div>'
                     f'<div class="fine">左：纸白板 · 中：墨板 · 右：墨板缩到 128 / 64 / 32'
                     f'（图标档笔宽 {WI}）</div>'))
parts.append(sec(PAL["light"], "三笔 · 读法", anatomy(PAL["light"])))
parts.append(sec(PAL["dark"], "三笔 · 深色", anatomy(PAL["dark"])))
parts.append(sec(PAL["light"], "配色",
                 '<div class="alt">'
                 f'<figure style="background:{PAL["light"]["bg"]}">'
                 f'<svg viewBox="0 0 24 24" width="180" height="180">{mark(PAL["light"])}</svg>'
                 f'<figcaption>象牙白 / 暖炭 / 群青 / 陶土</figcaption></figure>'
                 f'<figure style="background:{PAL["dark"]["bg"]}">'
                 f'<svg viewBox="0 0 24 24" width="180" height="180">{mark(PAL["dark"])}</svg>'
                 f'<figcaption>深底那一套</figcaption></figure></div>'))
parts.append(sec(PAL["light"], "色板",
                 '<div class="sw">' + "".join(
                     f'<div class="swatch"><div class="chip" style="background:{c}"></div>'
                     f'<div class="cl"><b>{n}</b><span>{c.upper()}</span></div></div>'
                     for n, c in (("陆地", PAL["light"]["land"]), ("河与水", PAL["light"]["water"]),
                                  ("船", PAL["light"]["boat"]),
                                  ("陆地 · 深底", PAL["dark"]["land"]),
                                  ("河与水 · 深底", PAL["dark"]["water"]),
                                  ("船 · 深底", PAL["dark"]["boat"]))) + '</div>'))
parts.append(sec(PAL["light"], "关键尺寸",
                 f'<div class="notes"><b>24 x 24 网格 · 比例全部照 ⌥ 字符实测</b><ul>'
                 f'<li>笔宽 {W} = 可见宽的 8.86%（真字符实测 31 / 350 px）</li>'
                 f'<li>陆地：中心线 y {LY}，{L1X0} → {L1X1}（= 3.0 个笔宽）</li>'
                 f'<li>河与水：({L1X1 + AIR:.2f}, …) → 肘 ({EBX}, {EBY}) → {B1X1}；'
                 f'斜线斜率 2.0（{ANG:.1f}°）；底横 2.95 个笔宽</li>'
                 f'<li>船：{L2X0} → {L2X1}（= 3.55 个笔宽）；与陆地之间的断口 = 2.7 个笔宽</li>'
                 f'<li>块与块之间留气口 {AIR}；端头一律同半径圆头，无直角</li>'
                 f'</ul></div>'))

html = f"""<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8">
<title>option-berth · 标志</title>
<style>
 @font-face {{ font-family:"Monaspace Neon"; src:url("{FONT}") format("opentype"); font-weight:600; }}
 * {{ box-sizing:border-box; }}
 body {{ margin:0; font-family:-apple-system,"PingFang SC",sans-serif;
        -webkit-font-smoothing:antialiased; color:#1B2942; }}
 .head {{ padding:60px 68px 46px; background:#1B2942; color:#E8EDF8; }}
 .head h1 {{ margin:0 0 12px; font-size:31px; font-weight:600; letter-spacing:-0.01em; }}
 .head p {{ margin:0; font-size:14.5px; line-height:1.85; color:#9DAAC7; max-width:820px; }}
 section {{ padding:46px 68px 50px; border-bottom:1px solid rgba(0,0,0,.07); }}
 .lab {{ font-size:11px; letter-spacing:.18em; text-transform:uppercase; color:#98A2B8;
         margin-bottom:26px; }}
 .hero {{ display:flex; align-items:center; gap:70px; flex-wrap:wrap; }}
 .hero > div svg {{ display:block; }}
 .ladder {{ display:flex; align-items:flex-end; gap:30px; flex-wrap:wrap; }}
 .lc {{ display:flex; flex-direction:column; align-items:center; gap:10px; }}
 .lc svg {{ display:block; }}
 .lc i {{ font-style:normal; font-size:10.5px; color:#A8B1C4; }}
 .anat {{ display:flex; gap:44px; flex-wrap:wrap; }}
 .anat figure {{ margin:0; display:flex; flex-direction:column; align-items:center; gap:13px; }}
 .anat figcaption {{ text-align:center; max-width:200px; }}
 .anat figcaption b {{ display:block; font-size:12.5px; margin-bottom:3px; }}
 .anat figcaption span {{ font-size:11px; line-height:1.7; color:#8B95AB; }}
 .alt {{ display:flex; gap:44px; flex-wrap:wrap; }}
 .alt figure {{ margin:0; padding:26px 22px 18px; border-radius:12px;
                display:flex; flex-direction:column; align-items:center; gap:14px; }}
 .alt figcaption {{ font-size:11.5px; color:#8B95AB; }}
 .plate {{ width:190px; height:190px; border-radius:10px; background:#C9CFDC;
           display:flex; align-items:center; justify-content:center; }}
 .col {{ display:flex; flex-direction:column; gap:18px; align-items:flex-start; }}
 .lock {{ display:flex; align-items:center; gap:16px; }}
 .lock .lt {{ font-family:"Monaspace Neon",ui-monospace,monospace; font-weight:600;
              letter-spacing:-0.035em; }}
 .lock > span svg {{ display:block; }}
 .fine {{ margin-top:24px; font-size:12.5px; color:#8B95AB; }}
 .notes {{ max-width:820px; }}
 .notes b {{ font-size:13.5px; }}
 .notes ul {{ margin:10px 0 0; padding-left:18px; }}
 .notes li {{ font-size:13px; line-height:2; color:#5A6880; }}
 .sw {{ display:flex; gap:24px; flex-wrap:wrap; }}
 .swatch {{ width:150px; }}
 .chip {{ height:74px; border-radius:10px; border:1px solid rgba(0,0,0,.07); }}
 .cl {{ margin-top:10px; font-size:11.5px; color:#5A6880; }}
 .cl b {{ display:block; color:#1B2942; font-size:12.5px; font-weight:600; margin-bottom:2px; }}
 .cl span {{ font-family:ui-monospace,monospace; }}
</style></head><body>
<div class="head">
  <h1>option-berth · 标志</h1>
  <p>照 <b>Option 字符本身的实测比例</b>重画：笔宽是可见宽的 8.86%，斜线斜率正好 2.0，左上横 3.0 个笔宽、右上横 3.55 个、底横 2.95 个，两段顶横的断口 2.7 个笔宽 —— 这几组数不是我编的，是那个字符自带的。配色：底是<b>象牙白</b> <code>#FAF7F1</code>，陆地 <code>#23262B</code>、河与水 <code>#2F5AA8</code>、船 <code>#C0703F</code>。陆地跟着象牙白改成暖炭之后，陆地与船同属暖调，群青就成了唯一的冷色重音 —— 三个颜色各站一位，不再互相抢。三块之间仍留 {AIR} 的气口。</p>
</div>
{"".join(parts)}
</body></html>"""
write("preview-option-berth.html", html)
print("files:", files)
print(f"W={W} LY={LY} LYB={LYB} 陆地 {L1X0}->{L1X1} 船 {L2X0}->{L2X1} 肘({EBX},{EBY}) 角{ANG:.1f}")

# ---- 字标锁排（用 HTML 渲染，@font-face 才拿得到随包字体）----------------------
lock_html = f"""<!DOCTYPE html><html><head><meta charset="utf-8"><style>
 @font-face {{ font-family:"Monaspace Neon"; src:url("{FONT}") format("opentype"); font-weight:600; }}
 body {{ margin:0; }}
 .row {{ display:flex; align-items:center; gap:60px; padding:26px 34px; }}
 .l {{ display:flex; align-items:center; gap:14px; }}
 .t {{ font-family:"Monaspace Neon",ui-monospace,monospace; font-weight:600;
       font-size:52px; letter-spacing:-1.6px; }}
</style></head><body>
 <div class="row" style="background:#FAF7F1">
   <div class="l">{svg(dict(land='#23262B', water='#2F5AA8', boat='#C0703F'), 62)}<span class="t" style="color:#0B1220">option-berth</span></div>
 </div>
 <div class="row" style="background:#191817">
   <div class="l">{svg(dict(land='#F7F3EA', water='#7C9BE0', boat='#DE8C60'), 62)}<span class="t" style="color:#F7F3EA">option-berth</span></div>
 </div>
</body></html>"""
open(os.path.join(OUT, "_lock.html"), "w").write(lock_html)
import subprocess as _sp
_HS = os.path.expanduser("~/Library/Caches/ms-playwright/chromium_headless_shell-1208/"
                         "chrome-headless-shell-mac-arm64/chrome-headless-shell")
_env = {k: v for k, v in os.environ.items()
        if k not in ("HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy")}
_sp.run([_HS, "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--hide-scrollbars",
         "--force-device-scale-factor=2", "--virtual-time-budget=3000",
         "--screenshot=" + os.path.join(OUT, "option-berth-lockup.png"),
         "--window-size=640,232", "file://" + os.path.join(OUT, "_lock.html")], check=True, env=_env)
os.remove(os.path.join(OUT, "_lock.html"))
print("lockup png written")
