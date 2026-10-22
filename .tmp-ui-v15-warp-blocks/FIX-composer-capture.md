# v15 fix — composer height + capture isolation

**Date:** 2026-10-22 (Asia/Shanghai)
**Branch:** `ui/warp-blocks-v15` (local only — do not push)

## (1) Composer
- `SessionComposerBar.composerFieldHeight` — 18pt × line count (1…5)
- Input row: path + mode chip + draft share one centered row
- Hint row stays under input; does not inflate idle height
- `SessionBridgeField` inset (2,1); no invented scroll height

## (2) Capture isolation
- `TerminalSession.commandOutputAccumulators` — bytes between OSC C and D
- `ShellCommandCapture.stripOSC` / `finalizeAccumulated`
- Fallback: `absoluteCursorRow` + `getScrollInvariantLine` (not viewport `getLine`)
- Idle PTY keepalive: 720×360 hidden (was 1×1)

## Verify by hand
1. Open OptionBerth → berth-demo Shell
2. Idle: composer ≈ one text line; caret level with path
3. `ls` then `git --version` — second block shows only git version

## Follow-up 2026-10-22 13:47 — height / slash / ANSI

Zay rejected build 366 @ `fcfb10e`. Three fixes:

1. **Composer height for real** — hint row moved **outside** the Ink.surface; surface is path+chip+draft only (~17pt + 4+4 pad). `SessionBridgeField.sizeThatFits` forces exact height so NSScrollView cannot invent a tall intrinsic size. `.clipped()`.
2. **Slash menu** — palette rows: `/command` on top, detail under, left-aligned, no ConsoleToolbarAction stretch. Width 320.
3. **ANSI litter** — `stripTerminalControls` removes OSC + CSI + orphan `[1m…` crumbs; `isOrphanCSILitter` drops clear/percent lines; `finalizeAccumulated` used on display path too. `clear` → empty body (no junk card text).
