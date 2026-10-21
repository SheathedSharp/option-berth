# option-berth v12 — ops-workbench (IA redesign)

## Decision
Zay chose **ops-workbench** (widget t68s280). Services + evidence is the primary stage;
Git / Sessions demote to **modes of the same workbench** (shared worktree focus), not three
equal home tabs. Keep left vertical Worktrees sidebar. Paper palette. **No** horizontal
BerthStrip (v7 rejected).

Base: `ui/stage-craft-v11` @ `912f336` → branch `ui/ops-workbench-v12`. Local only. NO push.

## Diagnosis (v8–v11)
v8–v11 kept the **same information architecture**:
left Worktrees · peer face strip (服务 | Git | 会话) · services table + log inspector.

They changed craft and Ink tokens (chips, card lift, Paper palette). Squint silhouette of
the Operate face stayed an **admin services table**. That is why token-only v12 would fail
the same gate.

Product core loop (`docs/product.md`): `status → up/restart/down → logs`. GUI = same facts as CLI.

## Thesis
**Operate workbench**: the selected berth’s job is to answer “what should run / is it running /
why not / start·stop·restart / show evidence.” Left rail = berth topology (worktrees with live
fraction + attention). Main stage = **attention-first services bound to run evidence**.
Git and Sessions are modes of that same berth focus — not peer apps competing for the home.

## Concrete IA change vs v11

| Surface | v11 | v12 ops-workbench |
|---------|-----|-------------------|
| Services list | Declaration order in one card | **Attention** section first (需重启 / 就绪超时 / 失败 / 健康失败…), then **就绪·在跑** calm rows |
| Undeclared listeners | Quiet inset under table | Still secondary, but **visually under Attention** when present (not buried below calm ready) |
| Log inspector | Focused service name; empty caption | Header = **selected service + status chip + exit/run fact**; taught empty when none selected |
| Face strip | Equal segmented 服务\|Git\|会话 | Kept switcher, but labeled as **工作面 modes**; 服务 is default home; quieter chrome |
| Document header | `live · N 服务` | Live **facts strip**: ready / attention counts tied to status |
| Shared focus | ServicesStore.focused only; Git jump via Notification | ViewState also holds `selectedServiceKey` + `selectedGitPath`; agent/log Git jump writes both and switches face |
| Sidebar row | `live/N` | Same + **attention mark** when any service needs attention |

## Shared selection bus
- `ViewState.selectedServiceKey` — mirrored when a service row is focused (`group/service`).
- `ViewState.selectedGitPath` — set when `berth.openGitPath` fires or when Operate offers “在 Git 中查看”.
- Cross-face: existing agent “在 Git 中查看” path stays; Operate adds an explicit jump to Git mode
  for the current berth (path when known; otherwise face switch + honest empty selection).

## Anti-goals
- No horizontal BerthStrip / replacing left sidebar
- No whole-machine port radar
- No inventing CLI capabilities
- No palette-only “v12” token redo as the main diff
- No remote push

## Success gate
Squint `02-services-live` vs v11: must show **Attention above calm Ready**, log header bound to
selection, face strip reading as modes — not the same admin table silhouette with new paint.
