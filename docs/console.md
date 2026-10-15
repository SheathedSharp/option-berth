# 工作台 / Console

工作台是当前 worktree 的原生会话工作区，不是两个名为 Terminal / Agent session 的独立页面。契约与纯路由模型在 `BerthAgent`；子进程与桥在 `BerthClient`。分阶段实施记录与剩余范围见 [console-plan.md](console-plan.md)。

## 布局与导航

- 左侧主画布为真实 Shell PTY（可分屏）。顶部 **Shell 栏** 切换本 worktree 的 Shell；**＋** 可新建 Shell 或 Agent。右侧 **Agent 栏**（宽度 `consoleAgentWidth`，默认 300pt）列出终端 Agent 与结构化流会话；栏头显示「需要你 / 工作中」计数，**＋** 菜单含新建（默认/其他 provider）、在原生终端中打开、从会话文件续接。
- Shell 与 Agent 共用 `TerminalSessions` 与结构化 `oberth agent stream` 子进程，合计最多 16 个会话名额。⇧⌘O / 退出应用会检查两类会话。
- ⌃⌥Tab 打开非模态**会话切换器**（Shell / Agent 分组、可搜索）。⌃Tab / ⇧⌃Tab 在会话间循环（系统保留 ⌘Tab）。独立终端窗口的快捷键不进入工作区投递链。
- 原生 Agent 全屏 TUI 时输入条收起为提示行，⌘↩ 返回 Shell 视图；结构化 Agent 详情在右栏，不抢 Shell 画布。

## 输入条与路由

- 底部**唯一输入条**（Shell 视图下）：上下文行、编辑区、路由行。↩ / ⌘↩ / ⇧⌘↩ 语义由 `ComposerRouter` 生成，与路由行一致。
- ↩ 送当前 Shell（CR 行尾；多行用 bracketed paste）。交互式 **zsh/bash/fish** 且启用 **Shell 集成**（`shellIntegration`，支持的 Shell 上新会话默认开）时：提示符状态键盘在输入条、终端光标透明，终端表面打字桥接到草稿；**Tab** 把单行草稿加 HT 送入 Shell PTY 做补全并从提示符行回读草稿；OSC `133;C`/`133;D` 驱动前台命令焦点与**命令块**条。
- ⌘↩ 送当前 **Agent 目标**（终端 Agent 为粘贴进 TUI、不回车）。`agent list` 中 `stream: true` 的 provider 默认走 **`oberth agent stream`**（NDJSON 桥）：轮次、审批、排队、中断、结束、续接、`/native` 原生续接；`unsupported` 时提示并可用 `/native` 或原生 PTY。已结束的流会话 ⌘↩ **续接**（新桥进程，同 provider 会话文件）。
- ⇧⌘↩ 新建 Agent 会话（结构化或终端，视 provider）。未选默认 Agent 时 ⌘↩ / ⇧⌘↩ 就地展开 provider 列表，草稿保留。`defaultAgent`（`settings.json`）使新工作区视为已选 provider。
- 行首 `/` 为斜杠菜单（`SlashParser`）：`/agent-select`、`/agent-new`、`/agent`、`/shell-new`、`/shell`、`/focus`、`/rename`、`/close`、`/interrupt`、`/native`、`/resume`、`/history` 等；未知 `/token` 不拦截，↩/⌘↩ 按原文送出。`/resume` 与 Agent 栏续接项均经 `agent plan --resume-file` 打开原生 PTY（须当前 worktree 的 provider 会话文件）。
- **Shell → Agent**：已完成命令块可点「发给 Agent」，将命令、退出码与终端输出尾部（≤200 行）附在输入条；⌘↩ 时合并为 markdown `` ```shell `` 代码块。结构化详情中命令可「放到输入条」；文件改动可「在 Git 中查看」（切 Git 页并选中路径，只读）。
- **通知**（默认关）：`consoleAgentNotifications` 或菜单「Agent 后台通知与 Dock 角标」；应用非前台时，结构化流的审批与轮次结束可发系统通知，Dock 显示未读会话数，回前台清除角标。

## 焦点与不变量

- 前台命令或备用屏时键盘在 PTY（⌃C/⌃L 可达 Shell）。`/focus` 把键盘交给终端直至下一次提示符。Agent 详情打开不强制抢焦点；审批请求**不能**用输入条 ↩/⌘↩ 回答，须在详情按钮或卡片操作。
- 焦点请求（`TerminalFocusIntent`）只消费一次；状态刷新不抢输入条焦点。输入法组合中 ↩/⌘↩ 不提交（须手测，见验收）。
- 离开 worktree 不迁移 draft/附件；取消中的启动计划丢弃。对话与流状态不落盘（除兼容的布局/会话元数据）。
- 同一 provider 会话文件：结构化桥交接（`/native`、结束后再开）须先关闭旧桥；从文件续接（`/resume`、栏内续接、`oberth agent stream --resume-file`）前检查本应用**运行中**结构化会话是否已绑定该文件，已占用则拒绝。原生 PTY Agent 暂不追踪会话文件，无法做同等冲突检测。

## 配置

| 键 | 含义 |
|---|---|
| `defaultAgent` | 新工作区默认 provider（`codex` / `claude` / …） |
| `shellIntegration` | 新开 zsh/bash/fish Shell 是否注入 OSC 命令块钩子 |
| `consoleAgentWidth` | 右栏 Agent 宽度 240–520 |
| `consoleAgentNotifications` | 结构化 Agent 后台通知与 Dock 角标 |

本地 UserDefaults 可在未写入 `settings.json` 时覆盖 `shellIntegration`、`consoleAgentWidth`、`consoleAgentNotifications`。

## 参考与取舍

Warp 的「单输入、Shell 常显、Agent 在侧栏」是交互参照，不接入其 AI 栈。外部 Agent 仍是用户环境中的 CLI；引擎只提供 `agent plan` / `agent stream` 桥，不写 provider 配置或自动批准。

## 发版前仍须手测

自动化与合成替身不能代替真实 provider 与输入法。见 [validation.md](validation.md) **控制台手测**表：**真机 Codex/Claude**（两轮、审批、中断、续接、`/native`）、**中文 IME**、powerlevel10k/starship 下 Shell 集成；可选 `consoleAgentNotifications`。实施历史与风险见 [console-plan.md](console-plan.md) §8–9。

## 验收

| 范围 | 证据 |
|---|---|
| 阶段 0 路由/斜杠/流归约/行投影 | `swift run AgentChecks`（含 `ComposerRouterChecks`、`AgentStreamReducerChecks`、`agentSessionFileGuardChecks` 等） |
| 焦点、键位、结构化流合成场景（含「放到输入条」、运行中占用会话文件时 `/resume` 拒绝） | `ConsoleChecks`（隔离 `HOME`/`BERTH_HOME`/`CFFIXED_USER_HOME`/`ZDOTDIR`，`BERTH_CONSOLE_TEST=1`） |
| 默认 Agent、Shell 集成、命令块「发给 Agent」 | `ConfigurationChecks`（同上） |
| 桥协议 | `go test ./internal/agentstream/... -race`（`engine/`） |
| 布局 | `mage states` 冻结图；主窗口菜单/几何见 `mage window` → `.cache/window.png` |
| 真实 provider / IME / 复杂提示符 | **须手测**；未做则在发版说明中写「未执行」 |

必须检查真实混合 PTY 的切换、进程身份、cwd、独立键入、分屏、独立窗口返回。冻结图不代替交互验收。
