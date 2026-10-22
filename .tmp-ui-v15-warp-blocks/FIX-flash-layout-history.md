# v15 fix — flash / block layout / history row

**Date:** 2026-10-22 (Asia/Shanghai)
**Branch:** `ui/warp-blocks-v15` (local only — do not push)
**Base:** `5a43df4` / build 372 (design direction OK)

## 1) Flash after submit
- With shell integration, do **not** mount `SessionActiveBlockCard` (live PTY) for ordinary running commands.
- Show `SessionPendingBlockCard` (header only, no TerminalSurface) while running.
- Active PTY card remains for TUI (`alternateScreen`) and no-integration shells.
- Removes mount/unmount flash of 提示符/live strip after Enter.

## 2) Block output layout
- Prefer scroll-invariant **buffer capture** over raw-byte accumulator (CSI-stripped stream concatenates columnar glyphs).
- Accumulator kept as fallback when start row scrolled away.
- Display: mono `Metrics.meta`, `lineSpacing(3)`, `fixedSize(vertical:)`, truncate 48 lines; preserve interior blank rows.

## 3) Remove 「历史」 chrome row
- Live console no longer draws the toolbar row with 「历史」.
- History via `/history` (pinned slash) and **⌘Y**.
- Hidden `terminal.history` control retained for HistoryEntryChecks.
- Frozen fixtures keep a non-interactive 「历史」 label for snapshots.

## Verify
1. Submit `ls` — no flash of live PTY/prompt card; pending chip optional then completed card.
2. `uv` / `ls` — readable newlines + column spaces.
3. No 「历史」 row under the project header; `/history` and ⌘Y open the sheet.
