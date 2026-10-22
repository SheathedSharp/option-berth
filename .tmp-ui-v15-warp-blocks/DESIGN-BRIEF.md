# option-berth UI v15 — Warp 块流

**Branch:** `ui/warp-blocks-v15`  
**Base:** `ui/warp-session-v14` @ `e8ffc40`  
**Date:** 2026-10-22 (Asia/Shanghai)  
**Locked by Zay:** ASCII prototype **A · Warp 块流**  
**Follow-up:** Zay rejected “live PTY under blocks” leftover @ `3951662` — active/interactive must live in-stream.

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
- 进行中的命令 / 空闲提示符 / TUI 都在流末 **active block 卡**内嵌唯一 PTY，不是块流下方第二视口
- 输入只在底栏；`[Shell]` / `[Agent · provider]` chip；**⌘I** 切换；↩ 按模式路由；`/` slash 保留
- 无 Shell 标签大条、无永久「选择默认 Agent」chrome、无 hint 堆砌
- Worktrees 侧栏 + Paper `Ink.*` 不变；`/ob-server` `/git` 仍为能力 overlay

## IA vs v14

| Surface | v14 | v15 (A) |
|---|---|---|
| Session strip | ShellSessionBar 常驻（Shell 1 + 大 +Agent） | **折叠**：单 Shell 无条；多会话极细轨或仅 switcher；去掉永久「选择默认 Agent」 |
| Canvas | 全幅 PTY + 顶上 CommandBlockStrip | **块流**：完成块为卡片；**唯一 PTY 嵌在流末 active 卡** |
| Composer | 模式 chip + 多段 routeRow 噪声 | **一行输入**：chip + 字段 + 右侧轻提示（`↩ run · ⌘I Agent · /`） |
| Provider pick | 条上永久按钮 + Agent toolbelt | **仅** Agent 模式 toolbelt / slash `/agent-select` |
| Services / Git | overlay | 不变 |

## Block model (active-in-stream)

复用现有 `CommandBlock` / shell integration / `capturedOutput`；**新代码**把唯一 `HostedTerminalView` 嵌进流末 **active block 卡**，不再在块流下方另开 live PTY 视口。

- **完成块**：静态卡 — `$ command` + 截断 `capturedOutput`；可选中 → 附给 Agent
- **Active block（流的最后一张卡，同款 chrome）**：宿主唯一 PTY
  - 运行中命令：卡头显示 `$ cmd` +「运行中」，卡身嵌 PTY
  - 空闲提示符：卡头「提示符 / live」，卡身嵌 PTY（composer 仍是底栏输入 chrome）
  - Alternate-screen TUI（vim/less 等）：同一张 active 卡加高占满视口，仍是一块卡而非第二栏
- **无 Shell 集成**：流里只有这一张 active 卡（全高），视觉仍是 block，不是裸 PTY 条
- Agent 结构化流仍用 `AgentStreamDetail` 占满画布；原生 agent PTY（非 terminal kind）暂不走块流

### Limits (honest)

- 完成块是文本快照，不是可回放的终端 buffer；PTY scrollback 里历史仍在，但 UI 以块卡为准
- ScrollView 内嵌 NSView：滚动时可能有轻微重布局闪烁
- Alternate-screen 依赖 `activity.alternateScreen`；未识别的全屏程序可能仍用紧凑 active 高度

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
