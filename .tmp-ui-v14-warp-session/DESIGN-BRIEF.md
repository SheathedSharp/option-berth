# option-berth UI v14 — Warp-like unified session

**Branch:** `ui/warp-session-v14`  
**Base:** `ui/session-primary-v13` @ `4a6c578`  
**Date:** 2026-10-22 (Asia/Shanghai)

## Diagnosis

Zay rejected v13 session UX. On a live screenshot he boxed **two** regions:

1. Shell / PTY content (session strip + terminal)
2. Bottom composer titled `berth-demo · 会话`

That stack still reads as **two panes / two apps**, not one native surface. Causes:

- Composer caption strip (`{worktree} · 会话`) acts as a second title bar
- Composer sits on `Ink.surface` while canvas is `Ink.canvas`, separated by a hairline → bordered dual-card look
- Mode is taught by **layout** (stacked regions) instead of **keyboard state** on one input

v13 correctly made Session primary and removed peer 服务|Git|会话 faces; the remaining failure is chrome, not IA.

## Thesis

**One continuous session canvas.** Blocks / PTY / agent stream fill the content area. **One input** is chrome of that same surface — not a second titled panel. Shell vs Agent is an **explicit input mode** shown on the prompt (marker / quiet chip / hint), switched by keyboard (Warp feel: ⌘I), not by stacking a second region. Services / Git stay session capabilities via `/ob-server` and `/git`.

## IA vs v13

| Surface | v13 (session-primary) | v14 (warp-session) |
|---|---|---|
| Worktree home | Session console | Unchanged |
| Services / Git | Capability overlays | Unchanged |
| Canvas | Single focus Shell **or** Agent | Unchanged (single focus) |
| Composer chrome | Separate titled bar (`· 会话`) + `Ink.surface` card | **Merged**: no title strip; shared canvas plane; hairline optional |
| Shell ↔ Agent | Canvas selection + chord routing | + **explicit input mode** (Shell / Agent) on the same field |
| Mode switch | Tabs / ⌘↩ chords | Tabs + **⌘I** toggle; Esc leaves Agent input chrome toward Shell-clean |

## Keybinding table

| Shortcut | Shell input mode | Agent input mode |
|---|---|---|
| ↩ | Send to Shell (ComposerRouter `.enter`) | Send / resume Agent (same as ⌘↩ chord) |
| ⌘↩ | Resume / send Agent (unchanged) | Resume / send Agent (unchanged) |
| ⇧⌘↩ | New Agent (unchanged) | New Agent (unchanged) |
| ⌘I | Toggle → Agent mode | Toggle → Shell mode |
| Esc | Dismiss slash / provider / switcher; else focus PTY | Dismiss overlays; else return to Shell input mode |
| / | Slash menu (unchanged) | Slash menu (unchanged) |
| ⌃⌥Tab / ⌘⌥Tab | Session switcher (unchanged) | Unchanged |

ComposerRouter / SlashParser / approvals contracts stay; mode only remaps **plain Enter** in Agent input mode onto the existing `.command` chord path.

## Visual rules (Paper)

- Kill composer caption / second title bar
- Shared `Ink.canvas` behind canvas + input (no surface well under composer)
- Session strip remains light tab chrome of the same plane (not a second app)
- Agent input mode: quiet mode chip + provider affordance **on the same input row**; optional soft `Ink.accentSoft` wash when Agent conversation owns the canvas
- Hairlines over bordered cards; no BerthStrip; no cool Spotlight chrome

## Anti-goals

- Do not revive peer top tabs for 服务 / Git / 会话
- Do not revive BerthStrip or cool Spotlight chrome
- Do not invent CLI capabilities in the GUI
- Do not pixel-clone Warp; adopt the **feel** (one surface + mode shortcuts)
- Do not soft-pedal: composer must not be boxable as a second panel the way Zay boxed v13

## Product contracts preserved

- `docs/console.md` ComposerRouter, slash, agent stream, approvals
- Left Worktrees sidebar
- `/ob-server` (alias `/services`) and `/git` overlays; Esc dismisses
- Paper `Ink.*` tokens only

## Success gate

1. Hand-testable app from `client/macos/build/OptionBerth.app`
2. Visually one session surface — cannot “box” two separate panels as on v13
3. ⌘I toggles Shell ↔ Agent input mode; Enter / ⌘↩ still route correctly
4. Worktrees sidebar + `/ob-server` + `/git` still work
5. Local commit only — **no push**
