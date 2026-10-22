# option-berth UI v15 — Warp 块流

**Branch:** `ui/warp-blocks-v15`  
**Base:** `ui/warp-session-v14` @ `e8ffc40`  
**Date:** 2026-10-22 (Asia/Shanghai)  
**Locked by Zay:** ASCII prototype **A · Warp 块流**

## Diagnosis vs v14

v14 去掉了「· 会话」底栏与第二卡片感，一体画布成立。Zay 手测仍判 UI/UX 差。截图像素问题：

1. **Shell 1 标签条** + 永久大控件 `+ / 选择默认 Agent` —— 仍是第二层 chrome，不像一块画布
2. **草稿飘在画布中间**、与干净底栏输入脱节 —— 输入应只有底下一处
3. **底栏 hint 堆砌**（`⌘↩ 先选择 Agent` ×2 等）—— 噪声，不像 Warp 一行轻提示
4. 任何仍能被框成「第二张卡」的表面 / 标题 / 下拉

v14 的失败是 **chrome 与块模型**，不是「Session primary」IA。

## Thesis (A)

**一块滚动块流 + 底下一行输入。**

- 完成的 Shell 命令（及输出）与 Agent 轮次以 **可选中 block** 出现在同一滚动流里
- Live PTY / 进行中的命令是流底的 **active unfinished region**，不是第二个标签页或第二栏
- 输入只在底栏；`[Shell]` / `[Agent · provider]` chip；**⌘I** 切换；↩ 按模式路由；`/` slash 保留
- 无 Shell 标签大条、无永久「选择默认 Agent」chrome、无 hint 堆砌
- Worktrees 侧栏 + Paper `Ink.*` 不变；`/ob-server` `/git` 仍为能力 overlay

## IA vs v14

| Surface | v14 | v15 (A) |
|---|---|---|
| Session strip | ShellSessionBar 常驻（Shell 1 + 大 +Agent） | **折叠**：单 Shell 无条；多会话极细轨或仅 switcher；去掉永久「选择默认 Agent」 |
| Canvas | 全幅 PTY + 顶上 CommandBlockStrip | **块流**：完成块为卡片；live PTY 为流底 active 区 |
| Composer | 模式 chip + 多段 routeRow 噪声 | **一行输入**：chip + 字段 + 右侧轻提示（`↩ run · ⌘I Agent · /`） |
| Provider pick | 条上永久按钮 + Agent toolbelt | **仅** Agent 模式 toolbelt / slash `/agent-select` |
| Services / Git | overlay | 不变 |

## Block model (least-wrong)

复用现有 `CommandBlock` / shell integration / `capturedOutput`，不另造平行模型。

- **完成块**：`$ command` + 截断输出；可选中 → 附给 Agent（已有 attach 路径）
- **进行中 / TUI / 无集成**：仍走 live `HostedTerminalView` 作为流底 active region
- **诚实遗留**：完整「交互 TUI 包进独立 block」不做伪实现；有 alternate-screen / busy 时焦点回 PTY

Agent 结构化流仍用现有 `AgentStreamDetail` 占满画布（⌘I / 选中 stream）；不与 Shell 块流并列成两栏。

## Anti-goals

- 不恢复 服务|Git|会话 peer tabs / BerthStrip / cool Spotlight
- 不像素克隆 Warp；只取块流 + 单输入手感
- 不改 ComposerRouter / slash / approvals 合同（`docs/console.md`）
- 不 kill `oberth serve`；本地 commit，**不 push**

## Success gate

1. 手测 app：`client/macos/build/OptionBerth.app`
2. 截图无法再框出「标签条 + 第二输入卡 + hint 堆」
3. ⌘I / ↩ / `/` / `/ob-server` / `/git` 仍正确
4. Freezes 写入 `.tmp-ui-v15-warp-blocks/`
5. Local commit only
