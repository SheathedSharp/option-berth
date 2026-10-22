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
