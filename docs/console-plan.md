# 会话工作台改造计划

> 状态：发版前（2026-10-15）。阶段 0–5 **实现**已并入 `main`（契约见 [console.md](console.md)；合成回归 `bash scripts/run_console_validation.sh`）。**剩余**：真机/IME/复杂提示符手测（见 [validation.md](validation.md)「建议步骤」与记录表）、`mage states` 全量 PNG 人工目检、本文滚动删减。
> 已确认的决定：Agent 默认走 provider 自己的结构化 stdio 协议，审批请求在会话页由人决定；
> 支持的 Shell（zsh/bash/fish）上新会话默认开启集成（`settings.json` 可关）。
> 每完成一个阶段，把对应内容并入 [console.md](console.md)、[cli.md](cli.md)、[product.md](product.md)，
> 再从本文删去已落地部分。提交说明用产品语言，不写阶段编号。

## 1. 目标体验

- 「会话」页只有**一条输入**。↩ 交给 Shell，⌘↩ 把同一段文字交给选中的 Agent 作为新一轮，⇧⌘↩ 新建 Agent 会话；行首输入 `/` 列出命令。
- 左边始终是真实的 Shell。右边是本 worktree 的 Agent 列表；点一行，在右栏里看它的轮次、正在跑的命令、改了哪些文件、在等你确认什么。
- Agent 在后台通过 stdio 工作，不需要进入它的 TUI 才知道它在干什么。状态来自 provider 的事件，不靠猜测。
- 没选默认 Agent 时，输入条直接提示去选；⌘↩ 会就地打开选择，文字保留，选完继续发送。
- 输入条下方始终写明三个键此刻会把文字送到哪里，并且这行提示由路由函数本身生成，不会和实际行为不一致。

## 2. 现状为什么不行

前两轮基本按需求实现了路由表，问题出在结构上。以下均对应当前代码：

| 现象 | 根因 |
|---|---|
| 仍有两个输入框 | Shell 提示符在终端里照样能输入，输入条只是放在它下面。进入 Agent 后，TUI 自己的输入框和输入条同时存在，且这时 ↩ 会写进看不见的 Shell（`WorkspaceConsole.shellTarget()` 回退到 `terminalReturnID`）。 |
| 必须「进入」TUI 才看得清 Agent | 右侧预览是 PTY 原始字节的尾部（`TerminalSession.onBytes` → `AgentActivityInference.appendTail`），没有经过终端仿真，TUI 的光标移动和颜色控制码会原样出现在预览里。 |
| 状态不可信 | 只看输出时间、BEL 和备用屏。`infer()` 把备用屏一律判为「工作中」，使用备用屏的 TUI 会一直显示工作中；静默被判为「等待输入」。 |
| ⌘↩ 有误操作风险 | ⌘↩ 是把文字加回车写进 TUI（`deliverAgent`），不看 TUI 当前状态。TUI 正在显示审批选项时，这个回车可能被当作确认（推断，未在真实账号上验证）。 |
| 入口多且语义冲突 | 同一个 Agent 可以从左侧 rail、右侧状态栏、工具栏 chips、⌘⌥Tab 切换器进入；「＋新会话」弹层里的「开始新会话 ⌘↩」与输入条的 ⌘↩ 含义不同。 |
| 输入条不知道 Shell 在跑命令 | zsh 集成默认关闭。没有集成时只能看备用屏，`npm test` 运行期间焦点仍在输入条，⌃C 送不到 Shell。 |
| 宣传的快捷键按不出来 | ⌘Tab / ⇧⌘Tab 被系统应用切换器占用（`SessionShortcutDelivery.recognize` 的注释写明只有测试能合成），实际可用的是 ⌃Tab。 |

本计划改三件事：Agent 通道从模拟键盘换成结构化协议；Shell 只保留一个输入面；同一件事只留一个入口。

## 3. 决定

| 编号 | 决定 | 排除的做法 |
|---|---|---|
| D1 | 支持结构化协议的 provider（先做 Codex、Claude Code）默认以结构化会话运行。原生 TUI 只用于没有机器协议的 provider（DeepSeek），以及用户显式选择的「在原生终端中继续」。 | 把文字粘贴进 TUI 作为主通道 |
| D2 | 引擎新增 `oberth agent stream`，作为唯一的结构化桥。客户端只说一种规范化 NDJSON 协议，不直接实现 provider 协议，CLI 与 GUI 能力一致。 | 在 Swift 里分别实现各 provider 协议 |
| D3 | 审批请求由人在会话页决定，provider 执行。桥只转发 provider 发出的请求与人的回答；不传任何放宽权限的参数，不自动批准，不写 provider 的配置或权限规则。 | 自动批准；`--dangerously-*`、`bypassPermissions`、`approvalPolicy` 覆盖；安装 hooks/插件/MCP |
| D4 | 键位含义在所有视图里不变：↩ 永远是 Shell，⌘↩ 永远是 Agent。Agent 详情在右栏打开，Shell 始终可见。 | 进入 Agent 视图后把 ↩ 改成发给 Agent |
| D5 | Shell 在提示符状态时，键盘只属于输入条；有前台程序或备用屏时，键盘属于终端。zsh 下集成默认开启（仍是临时 ZDOTDIR，不改用户文件，settings.json 可关）。没有集成时保留今天的行为并在输入条提示。 | 隐藏或改写用户的 PS1 |
| D6 | 输入条的路由提示由 `ComposerRouter` 对三个键分别求值后生成。 | 单独维护提示文案 |

## 4. 数据模型

### 4.1 对象与归属

| 对象 | 拥有者 | 生命周期 | 写入恢复文件 |
|---|---|---|---|
| Shell | `TerminalSessions`（唯一 PTY 所有者） | 用户新建、结束 | 标题、类型、布局（现状） |
| 结构化 Agent 会话 | 新 `AgentStreams`；每个会话一个 `oberth agent stream` 子进程 | ⌘↩ / ⇧⌘↩ 新建；结束需等子进程真实退出 | 否 |
| 终端 Agent 会话 | `TerminalSessions`，kind `agent:<provider>:native` 或 `:task` | 现状 | 现状 |
| 轮次、条目、审批请求、待发队列 | 所属结构化会话，仅内存，有上限 | 随会话 | 否 |
| 工作台状态 | 每个 worktree 一个 `ConsoleWorkspace` | 应用运行期 | 只写兼容字段 |

两类进程共享 16 个会话名额。退出应用的检查（`MenuBarDelegate`）和全局会话管理（⇧⌘O）同时覆盖二者。
这里没有新的服务事实，daemon 不参与；对话内容只由 provider 自己保存。

### 4.2 客户端类型

值类型与纯函数（`AgentStreamReducer`、`ComposerRouter`、`SlashParser`、状态推导）放在 `AgentSupport/`（BerthAgent 目标），新文件只依赖 Foundation，AgentChecks 可以直接测。
拥有子进程的 `AgentStreams`、`AgentStreamProcess` 与下面的 `AgentStreamSession` 放在 `Sources/`；会话的可见字段只由归约器根据桥事件更新。

```swift
/// 每个 worktree 的默认 provider。fallback 的 "codex" 不算选择。
enum ProviderSelection: Equatable { case unset, chosen(String) }

/// 结构化会话：可见状态全部由桥事件归约得出（AgentStreamReducer）。
@MainActor final class AgentStreamSession: ObservableObject, Identifiable {
    let id: UUID
    let worktree: String                         // 规范化路径，创建后不变
    let provider: String
    @Published private(set) var title: String    // 首轮输入首行，≤ 40 字；可重命名；兜底「Codex 2」
    @Published private(set) var phase: StreamPhase          // starting | ready | ending | ended(EndReason)
    @Published private(set) var turns: [AgentTurn]          // ≤ 50 轮，超出丢弃最早的并留标记
    @Published private(set) var requests: [AgentRequest]    // 待人回答
    @Published private(set) var queued: [QueuedInput]       // 客户端拥有的待发队列，≤ 8 条，可撤回
    @Published private(set) var reference: ProviderReference? // provider 会话 ID 与会话文件路径，仅内存
    @Published var unread: Bool
    let createdAt: Date
}

struct AgentTurn: Identifiable {
    let id: String                 // 桥分配
    let input: String              // ≤ 64 KiB
    let sentAt: Date
    var outcome: TurnOutcome       // running | completed | failed(code, message) | interrupted | lost
    var items: [AgentItem]         // ≤ 500
    var diff: TurnDiff?            // 文件增删统计 + 截断后的 unified diff
    var endedAt: Date?
}

enum AgentItem: Identifiable {
    case message(id: String, text: String)                // 流式累积
    case reasoning(id: String, summary: String)           // 默认折叠
    case command(id: String, command: String, cwd: String?, status: ItemStatus,
                 exitCode: Int?, durationMs: Int?, outputTail: String)
    case fileChange(id: String, files: [FileStat], status: ItemStatus)
    case tool(id: String, name: String, inputPreview: String, status: ItemStatus)
    case plan(id: String, steps: [PlanStep])
    case notice(id: String, level: NoticeLevel, code: String, text: String)  // 权限被拒、重试、限流等
}
enum ItemStatus { case running, completed, failed, declined }

struct AgentRequest: Identifiable {
    let id: String                 // provider 请求 ID，原样回传
    let turn: String
    let kind: Kind                 // command | fileChange | permission | tool
    let command: String?, cwd: String?, paths: [String], reason: String?, preview: String?
    let decisions: [Decision]      // provider 给出的子集：allow / allowSession / deny
    var answering: Decision?       // 已发出回答，等 request.closed
}

/// 右栏的一行：只读投影。两类会话都映射到它，冻结图也用它。
struct AgentRow: Identifiable, Equatable {
    let id: UUID, provider: String, transport: Transport     // .stream | .terminal
    let title: String, status: AgentDisplayStatus
    let activity: String?          // 「运行 npm test」「等你确认 · rm -rf build」「12 秒前有输出」
    let since: Date?, unread: Bool, isTarget: Bool, queuedCount: Int
}
```

### 4.3 显示状态

结构化会话只按事件推导：

| 条件 | 显示 | 第二行 | 颜色令牌 |
|---|---|---|---|
| phase = starting | 启动中 | — | `Ink.inkMuted` |
| ready，没有轮次 | 空闲 | 等第一条消息 | `Ink.inkMuted` |
| 最新轮 running 且有待回答请求 | 等你确认 | 请求摘要，如 `rm -rf build` | `Change.changed` |
| 最新轮 running | 工作中 | 当前条目摘要 + 用时 | `Ink.live` |
| 最新轮 completed | 已完成 | 最后一条消息首行 + 相对时间 | `Ink.ink`，未读带点 |
| 最新轮 failed | 出错 | provider 错误类别（鉴权、限流…） | `Change.removed` |
| 最新轮 interrupted | 已中断 | — | `Ink.inkMuted` |
| 最新轮 lost | 结果未知 | 进程在本轮中退出 | `Change.removed` |
| phase = ended | 已结束 | 可续接 / 不可续接 | `Ink.inkFaint` |

当前条目摘要：命令为「运行 `npm test`」，文件为「修改 src/a.ts 等 3 个文件」，工具为「调用 Grep」，消息为「回复中」，推理为「思考中」。

终端会话只写事实，不推断「在工作」或「在等待」：
- task 模式：「运行中」或「已退出 exit N」，这是精确的。
- native 模式：「终端 · N 秒前有输出」；收到 BEL 或 OSC 9/777 时加关注点；退出后显示退出码。
- 删除 `AgentActivityInference` 中「备用屏 = 工作中」和「静默 = 等待输入」两条规则。
- 预览改为读取 SwiftTerm 屏幕：`getTerminal().getLine(row:)` + `translateToString(trimRight:)`，取最后 8 个非空行。控制码由仿真器处理，不再出现在预览里。

### 4.4 输入条路由模型

```swift
enum ComposerChord { case enter, command, commandShift }

struct ComposerContext: Equatable {
    var text: String
    var slash: SlashParse        // none | composing(filter) | command(SlashCommand, argument)
    var shell: ShellTarget       // none | atPrompt(id, title) | busy(id, title, command?) | untracked(id, title)
    var provider: ProviderSelection
    var target: AgentTarget      // none | stream(id, title, StreamTargetState) | terminal(id, title, alive)
}
// StreamTargetState: starting | idle | working | needsYou | ended(resumable: Bool)

enum ComposerAction: Equatable {
    case none
    case runSlash(SlashCommand, String)
    case shell(UUID, String)
    case shellBusy(String)                  // ↩ 不可用的原因
    case openShellThenSend(String)          // 还没有 Shell 或 Shell 已退出
    case agentSend(UUID, String)            // 立即作为新一轮
    case agentQueue(UUID, String)           // 目标启动中、工作中或等你确认：入队
    case agentResumeThenSend(UUID, String)  // 目标已结束但有会话文件
    case agentPaste(UUID, String)           // 终端会话：切到它的终端并粘贴，不回车
    case agentNew(String, firstTurn: String?)
    case pickProvider(then: ComposerChord)  // 先选 provider，文字保留，选完重放原键
    case openTarget(UUID)                   // ⌘↩ 且没有文字
    case refuse(String)
}

enum ComposerRouter {
    static func route(_ chord: ComposerChord, _ context: ComposerContext) -> ComposerAction
    /// 对三个键各调用一次 route 再描述结果。提示与行为同源。
    static func hints(_ context: ComposerContext) -> RouteHints
}
```

`ShellTarget.untracked` 表示没有集成、无法知道 Shell 是否在跑命令。`SlashParse` 由 `SlashParser` 生成（规则见 5.3）。
`ComposerAction` 只描述意图，执行由 `WorkspaceConsole` 完成；执行失败时在输入条上沿显示原因，文字保留。

写入字节（`SessionPTYInput`）：
- Shell：以 CR 结尾（与真实回车一致）。多行文字在 Shell 开启 bracketed paste 时包裹后加 CR；没开启时拒发并提示。
- 终端 Agent：开启 bracketed paste 时包裹发送，不加 CR；没开启时只接受单行，同样不加 CR。
- 两者都过滤 ESC 与 NUL，上限 64 KiB（现状）。

### 4.5 工作台状态与恢复文件

| 字段 | 含义 | 与旧字段 / 恢复文件的关系 |
|---|---|---|
| `provider: ProviderSelection` | 默认 Agent | 读写 `providerID`；恢复的工作区视为 `.chosen` |
| `targetAgentID: UUID?` | ⌘↩ 目标 | 取代 `resumeAgentID`；不归档 |
| `currentShellID: UUID?` | ↩ 目标 | 归档为 `terminalLayout.focused` |
| `canvas: .shell / .agentTerminal(UUID)` | 主画布 | 写出 `agentMode = canvas 是终端 Agent`；读取时一律回到 `.shell` |
| `panel: .list / .detail(UUID) / .collapsed` | 右栏 | 不归档 |
| `draft`、`history`（≤ 200 条，记录去向） | 输入 | 不归档（现状） |
| `terminalLayout` | 仅用于 Shell 分屏 | 不变 |
| `agentLayout`、`nativeExpanded` | 弃用 | 只为兼容读写旧文件保留，写出空布局与 `false` |

结构化会话不写入 `WorkspaceArchive`，续接仍须显式选择会话文件。以后若要在恢复页显示历史结构化会话，再升级 `schemaVersion` 并保留 v1 读取。
右栏宽度按现有模块宽度的文件化方式新增 `consoleAgentWidth`（240–520，默认 300；`settings.json` + `console.agentWidth.v1` 本地覆盖），同步 `ClientConfiguration.keys`、settings schema 与 schema 校验脚本。

### 4.6 引擎契约：`oberth agent stream`

```text
oberth agent stream <provider> --worktree <path> [--resume-file <path>]
```

- 属于交接层 `agent`，不经 daemon，不登记服务事实。调用方拥有这个进程。
- stdin 每行一条命令，stdout 每行一个事件，均带 `"v":1`。stderr 只写诊断，不含输入或输出原文。
- 启动阶段失败时，stderr 输出标准 JSON 错误：参数错误或未安装退出 2，provider 启动失败退出 1。
  新增稳定错误码 `unsupported`，表示已安装的 provider 版本没有可用的机器协议，客户端据此提供「用原生终端打开」。
- 正常关闭退出 0；provider 意外退出退出 1；SIGINT 退出 130。
- `agent list --json` 每项新增 `"stream": true|false`，表示本构建有该 provider 的结构化适配器，不代表已安装的版本一定支持。

命令（stdin）：

```json
{"v":1,"type":"turn.start","id":"c1","text":"修复登录页的单元测试"}
{"v":1,"type":"turn.interrupt","id":"c2"}
{"v":1,"type":"request.respond","id":"c3","request":"r7","decision":"allow"}
{"v":1,"type":"request.respond","id":"c4","request":"r8","decision":"deny","message":"先别删缓存，换个办法"}
{"v":1,"type":"session.close","id":"c5"}
```

每条命令先得到 `{"v":1,"type":"ack","id":"c1"}`，或 `{"v":1,"type":"error","id":"c1","code":"busy","message":"..."}`。

事件（stdout）：

```json
{"v":1,"type":"session.started","provider":"codex","protocol":"codex-app-server","worktree":"/workspace/demo","provider_session_id":"thr_123","session_file":"/Users/me/.codex/sessions/2026/10/15/rollout-thr_123.jsonl","capabilities":["interrupt","approvals","turn_diff"]}
{"v":1,"type":"turn.started","turn":"t1","command":"c1"}
{"v":1,"type":"item.started","turn":"t1","item":{"id":"i1","kind":"command","command":"npm test","cwd":"/workspace/demo","status":"running"}}
{"v":1,"type":"item.updated","turn":"t1","item":"i2","kind":"message","text_delta":"先运行测试确认失败原因。"}
{"v":1,"type":"item.completed","turn":"t1","item":{"id":"i1","kind":"command","status":"failed","exit_code":1,"duration_ms":12034,"output_tail":"..."}}
{"v":1,"type":"request.opened","turn":"t1","request":{"id":"r7","kind":"command","command":"rm -rf .cache","cwd":"/workspace/demo","reason":"...","decisions":["allow","allow_session","deny"]}}
{"v":1,"type":"request.closed","request":"r7","decision":"allow","by":"client"}
{"v":1,"type":"turn.diff","turn":"t1","files":[{"path":"src/login.ts","added":12,"removed":3}],"patch":"...","truncated":false}
{"v":1,"type":"notice","turn":"t1","level":"warn","code":"permission_denied","message":"..."}
{"v":1,"type":"turn.completed","turn":"t1","outcome":"completed","usage":{"input_tokens":1200,"output_tokens":800}}
{"v":1,"type":"session.ended","reason":"closed","exit_code":0}
```

条目 `kind`：`message`、`reasoning`、`command`、`file_change`、`tool`、`plan`。人读文案由客户端生成，桥只给字段。

不变量：
1. 同一会话同一时刻最多一轮。运行中收到 `turn.start` 返回 `busy`；排队由客户端负责，队列可见、可撤回。
2. `outcome` 只来自 provider 的结束信号。中断请求发出后、provider 确认前仍是 running；provider 在本轮中退出记为 `lost`，不推断成功或失败。
3. 不自动回答任何请求。不认识的 provider 请求（提问、MCP elicitation、attestation 等）以「客户端不支持」回绝并发 `notice`，从不默认允许。
4. `allow_session` 只在 provider 自己提供且作用域仅限本会话时出现：Codex 的 `acceptForSession`；Claude 中 `destination` 为 `session` 的 permission_suggestions。会持久化规则的选项（Codex execpolicy/network amendment、Claude 写入 settings 的建议）一律不提供。
5. 输入只走 stdin，不进 argv；桥不写日志文件。输出有上限：消息累计 1 MiB、命令尾部 16 KiB、diff 512 KiB、单行 2 MiB，超出截断并标记 `truncated`。
6. 关闭顺序：有运行中的轮次先请求中断并等待（≤ 10 秒）→ 关闭 provider stdin，等待退出（≤ 5 秒）→ 对 provider 进程组发 SIGTERM，等待（≤ 5 秒）→ SIGKILL → 回收后才发 `session.ended` 并退出。stdin EOF 等同 `session.close`。信号发出不等于已退出。
7. `--resume-file` 复用 `agentlaunch.inspectResume` 的校验，记录的 worktree 必须与 `--worktree` 一致。

provider 适配：

| provider | 启动与握手 | 轮次与事件 | 审批 | 中断 | 续接 | 阶段 |
|---|---|---|---|---|---|---|
| Codex | `codex app-server`；`initialize` → `initialized` → `thread/start {cwd}`，不覆盖审批与沙箱策略 | `turn/start`；`item/started`、`item/agentMessage/delta`、`item/commandExecution/outputDelta`、`item/completed`、`turn/diff/updated`、`turn/completed` | 服务端请求 `item/commandExecution/requestApproval`、`item/fileChange/requestApproval`、`item/permissions/requestApproval`，按 `availableDecisions` 映射 `accept` / `acceptForSession` / `decline` | `turn/interrupt`，以 `turn/completed` 的 `interrupted` 为准 | `thread/resume {threadId, excludeTurns: true}`；会话文件取 `thread.path` | 2 |
| Claude Code | `claude -p --input-format stream-json --output-format stream-json --verbose --include-partial-messages --permission-prompt-tool stdio`，按 Agent SDK 控制协议握手 | stdin 写 `user` 消息；读 `system/init`、`assistant`、`user`（tool_result）、`stream_event`、`result`、`system/api_retry`、`permission_denied` | `control_request` `can_use_tool` → `control_response` allow/deny；`control_cancel_request` → `request.closed` | `control_request` `interrupt`；不用 SIGTERM 结束进行中的轮 | `--resume <会话文件绝对路径>` | 2 |
| OpenCode | `opencode acp`（ACP） | `session/prompt`、`session/update` | `session/request_permission` | `session/cancel` | `session/load` | 5 |
| Pi | `pi --mode rpc`；`get_state` 后 `prompt` / `abort` | `message_update`、`tool_execution_*`、`agent_settled` | `extension_ui_request`（`select` / `confirm`） | `abort` | `--session <会话文件>` | 5 |
| DeepSeek | 无机器协议 | 终端方式 | — | — | — | — |

Claude 条目映射：`Bash` 为 command；`Edit`、`Write`、`MultiEdit`、`NotebookEdit` 为 file_change；`TodoWrite` 为 plan；文本块为 message；thinking 为 reasoning；其他工具为 tool。
Codex 的 `agentMessage`、`reasoning`、`commandExecution`、`fileChange`、计划与 MCP/搜索类条目分别对应同名类别。

Claude 的 stdio 控制通道是 Agent SDK 使用的协议，不是 CLI 文档里承诺的稳定参数。适配器用 `system/init` 的 `capabilities`
和握手结果探测，任一失败返回 `unsupported`，客户端回退终端方式。所有参数以实施时的 provider 文档和合成夹具为准，夹具不取自真实账号输出。

schema 放在 `engine/docs/schema/agent-stream.schema.json`，测试用它校验全部夹具。

## 5. 交互

### 5.1 键位

| 键 | 作用 | 备注 |
|---|---|---|
| ↩ | 交给当前 Shell | 输入法组合中不提交（现状） |
| ⌘↩ | 交给 ⌘↩ 目标，作为新一轮 | 没有目标时按默认 Agent 新建 |
| ⇧⌘↩ | 新建 Agent 会话，文字作为第一轮 | 没有文字时新建空闲会话 |
| ⇧↩、⌥↩ | 换行 | 编辑区最多显示 8 行，之后滚动 |
| ↑ / ↓ | 菜单打开时移动选择；否则在光标位于首行 / 末行时翻历史 | 历史含两种去向，回填不发送，连续重复去重 |
| 行首 `/` | 命令菜单 | |
| Esc | 依次：关闭菜单 → 关闭 Agent 详情 | 从不清空文字，从不写入 PTY |
| ⌃C | 有文字时清空（可 ⌘Z 撤销）；没有文字时向当前 Shell 发 ETX | |
| ⌃L | 转发给当前 Shell | |
| ⌃Tab / ⌃⇧Tab | 切换 Shell | 识别器已接受 ⌃；界面与文档不再写 ⌘Tab |
| ⌃⌥Tab | 打开会话切换器 | 同上 |
| 原生终端视图中的 ⌘↩ | 回到 Shell 视图，聚焦输入条，目标设为该终端 Agent | Esc 留给 TUI |

所有工作台快捷键都带 ⌘ 或 ⌃ 组合，不占用 TUI 需要的裸键。

### 5.2 路由表

文字非空且不是命令时：

| 目标状态 | ⌘↩ | ⇧⌘↩ |
|---|---|---|
| 未选默认 Agent，没有目标 | 就地打开 provider 选择；选完用这段文字新建 | 同左 |
| 已选默认 Agent，没有目标 | 用默认 Agent 新建，这段文字是第一轮 | 新建 |
| 目标空闲、已完成、出错或已中断 | 作为新一轮发送 | 新建，并成为新目标 |
| 目标启动中或工作中 | 入队，本轮结束后自动发送 | 新建 |
| 目标等你确认 | 入队，并提示先处理请求；绝不用这段文字回答请求 | 新建 |
| 目标已结束，有会话文件 | 续接（新桥进程 + `--resume-file`）后发送 | 新建 |
| 目标已结束，没有会话文件 | 不发送，提示 ⇧⌘↩ | 新建 |
| 目标是运行中的终端会话 | 切到它的终端并粘贴，不回车 | 新建（默认 Agent） |
| 目标是已退出的终端会话 | 不发送，提示 ⇧⌘↩ | 新建 |

⇧⌘↩ 新建的会话总是成为新目标；原目标继续运行，不受影响。

↩ 与 Shell 状态：
- 提示符状态：发送。
- 前台有命令或备用屏：不发送，提示「Shell 1 正在运行 npm test，键盘在终端里」；⌘↩、⇧⌘↩ 仍然可用。
- 没有 Shell 或 Shell 已退出：先开一个再发送；目录不存在时报错，不留下半启动进程（现状）。
- 没有集成：照常发送（今天的行为）。

没有文字时：↩ 不做事；⌘↩ 打开目标详情，没有目标且未选默认 Agent 时打开选择；⇧⌘↩ 新建空闲会话，未选时先选。
已有目标时，⌘↩ 一直发给目标自己的 provider，改默认 Agent 只影响新建。会话结束时队列里的文字合并放回输入条并提示，不静默丢弃。

提示示例（由 `ComposerRouter.hints` 生成）：

| 情况 | 提示 |
|---|---|
| 提示符状态，目标空闲 | `↩ Shell 1` · `⌘↩ 续接 Codex · 修复登录测试` · `⇧⌘↩ 新建 Codex 会话` |
| Shell 正在运行命令 | `↩ 不可用：Shell 1 正在运行 npm test` |
| 未选默认 Agent | `⌘↩ 先选择 Agent` · `⇧⌘↩ 先选择 Agent` |
| 目标工作中 | `⌘↩ 排在 Codex 当前这一轮之后` |
| 目标等你确认 | `⌘↩ 排队（Claude 在等你确认）` |
| 目标是终端会话 | `⌘↩ 放进 DeepSeek 1 的终端（不回车）` |

按住 ⌘ 时高亮 ⌘↩ 这一段，按住 ⇧⌘ 时高亮 ⇧⌘↩ 这一段。

### 5.3 `/` 命令

| 命令 | 作用 | 何时出现 |
|---|---|---|
| `/agent-select [provider]` | 选择默认 Agent；二级列表标出「结构化 / 仅终端 / 未安装」，未安装的不可选 | 总是 |
| `/agent-new [任务]` | 新建 Agent 会话，同 ⇧⌘↩ | 总是 |
| `/agent [名称]` | 切换 ⌘↩ 目标；二级列表是本 worktree 的 Agent 会话及状态 | 有 Agent 会话 |
| `/interrupt` | 中断目标当前这一轮 | 目标工作中 |
| `/close` | 结束目标会话（确认后执行） | 有目标 |
| `/native` | 在原生终端中继续目标会话 | 目标有会话文件且 provider 支持续接 |
| `/resume` | 从会话文件续接…（显式选文件，现有校验） | provider 支持续接 |
| `/shell-new` | 新建 Shell | 总是 |
| `/shell [名称]` | 切换当前 Shell | 多于一个 Shell |
| `/focus` | 把键盘交给当前终端（vim、交互程序） | 有 Shell |
| `/rename 名称` | 重命名打开的 Agent 详情；没有打开时重命名当前 Shell | 总是 |
| `/history` | 搜索命令历史 | 总是 |

规则：
- 只在行首识别。命令 token 里出现第二个 `/` 时视为路径，例如 `/usr/bin/env`（与 Warp 的 `slash_command_composition_filter` 相同）。
- token 与命令完全一致时，命令部分显示为强调色片段；之后的文字是参数。
- 菜单锚定在输入条上沿，宽度与编辑区一致，最多 8 行；去掉现在的 `offset(y: -52)`。↑↓ 移动，↩ 或 Tab 选择，Esc 关闭。二级列表在同一个浮层里展开，Esc 返回上一级。
- 不可用的命令不显示。
- 不认识的 `/xxx` 不拦截：⌘↩ 原样发给 Agent（provider 自己的命令，例如 `/compact`），↩ 交给 Shell。
- 删除 `/open`（改为 `/focus`）与 `/stop`（拆为 `/interrupt` 和 `/close`）。

### 5.4 焦点

1. 有集成、Shell 在提示符状态：输入条持有键盘。输入条聚焦时终端 `caretColor` 设为透明（需验证 Metal 渲染路径同样生效）。点击终端可以选择、复制；此时按下不带 ⌘、⌃ 的键，先让输入条成为第一响应者，再把该按键事件重新派发给输入条，保证输入法组合从输入条开始。
2. 前台命令开始（OSC 133;C）或进入备用屏：键盘交给终端，程序可以读到输入和 ⌃C。
3. 命令结束（133;D / 133;A）：只有焦点仍在该终端时才回到输入条，不抢其他编辑器。
4. `/focus` 把键盘交给终端，直到下一次提示符出现。
5. 没有集成：终端和输入条都可输入（今天的行为）；输入条上下文行提示「未启用 Shell 集成」，并提供开启入口（对新开的 Shell 生效）。
6. 原生终端视图：键盘全部属于 TUI，包括 Esc；输入条收起为一行提示，⌘↩ 返回。
7. 打开 Agent 详情不移动键盘；输入条保持焦点，⌘↩ 的目标随之切换。
8. 焦点请求只消费一次，状态刷新不抢焦点（现状，`TerminalFocusIntent`）。

### 5.5 关键流程

**第一次使用**
1. 打开「会话」，Shell 1 启动，输入条获得焦点；提示「⌘↩ 先选择 Agent」。
2. 输入「修复登录测试」后按 ⌘↩，输入条上方展开 provider 列表，文字保留。
3. 选 Codex：新建结构化会话「Codex · 修复登录测试」，右栏出现一行，从「启动中」到「工作中」；输入条清空，它成为目标。

**续接一轮**
1. 目标显示「已完成」。输入「再补一个回归测试」，按 ⌘↩。
2. 同一个桥进程、同一个 provider 会话开始第二轮，详情里追加一轮。

**并行会话**
1. ⇧⌘↩「顺便更新 README」新建第二个会话并成为目标，第一个继续工作。
2. 点第一个会话的行：它成为目标，并在右栏打开详情。

**审批**
1. Claude 请求运行 `rm -rf build`：行变成「等你确认」，琥珀色，行内显示命令与「允许 / 拒绝 / 查看」。
2. 点「允许」：按钮进入等待态，收到 `request.closed` 后回到「工作中」。
3. 这期间 ⌘↩ 的文字只会入队，不会回答请求。

**在原生终端中继续**
1. 详情菜单选「在原生终端中继续」。正在工作时先确认中断。
2. 桥关闭并被回收后，用会话文件经 `agent plan --resume-file` 启动原生 TUI；主画布切到它，输入条收起。
3. ⌘↩ 回到 Shell 视图。原结构化会话显示「已在原生终端中继续」并链接到该终端会话。同一个 provider 会话任何时候只有一个进程。

**Shell 运行交互命令**
1. ↩ 执行 `npm test`，集成报告命令开始，键盘交给终端（可以 ⌃C）；输入条显示「Shell 1 正在运行 npm test」，⌘↩ 仍然可用。
2. 命令结束，焦点回到输入条（前提是焦点还在该终端）。

### 5.6 未读与提醒

- 一轮结束（完成、出错、中断）或出现新请求时标为未读，除非该会话的详情正显示在前台窗口里。打开详情即已读。
- 栏头的「需要你 N」不因查看清除，只在请求被回答或被 provider 撤销时减少。
- 窗口外的系统通知和 Dock 角标放在阶段 4，默认关闭。

## 6. 界面

### 6.1 总布局

```text
服务  Git  [会话]
─────────────────────────────────────────────────────────────────────────────
[›_ Shell 1 ●] [›_ Shell 2] [＋]                          分屏 · 查找 · 历史 · ⋯
────────────────────────────────────────────┬────────────────────────────────
                                            │ Agent   需要你 1 · 工作中 1
  Shell 1（真实 PTY，可选中复制）             │ 默认 Codex ▾                    ＋
  user@host demo % npm test                 │────────────────────────────────
  FAIL src/login.test.ts                    │▍Codex · 修复登录测试          ⌘↩
  ...                                       │   ● 工作中 · 运行 npm test    0:42
  user@host demo %        （无光标）         │────────────────────────────────
                                            │  Claude · 重构 auth             !
                                            │   等你确认 · rm -rf build
                                            │   [允许] [拒绝] [查看]
                                            │────────────────────────────────
                                            │  Codex · 补文档                 •
                                            │   ✓ 已完成 · 已更新安装说明 · 3 分钟前
────────────────────────────────────────────┴────────────────────────────────
 demo · main ±3 · 上条 exit 1
 ❯ 修复登录页的单元测试，先跑 npm test 看看哪里挂了▏
 ↩ Shell 1    ⌘↩ 续接 Codex · 修复登录测试    ⇧⌘↩ 新建 Codex 会话    / 命令
```

- 输入条横跨整个页面底部，是唯一的输入。
- 右栏默认 300pt，可拖到 240pt 至窗口宽度的 60%；双击分隔线在默认宽度和宽视图之间切换。
- 窗口不足以同时容纳 Shell 360pt 与右栏 240pt 时，右栏收起；路由行右端显示目标的状态胶囊，点击展开。

### 6.2 输入条

三行：
1. 上下文行（caption）：worktree 名 · 分支 · 未提交文件数 · 上一条命令的退出码。数据来自已有的 Git 摘要与命令块，不读取 shell 的 cwd 上报（客户端不信任 OSC 7）。没有集成时，这一行改为「未启用 Shell 集成 · 开启」。
2. 编辑区：等宽字体，1–8 行。占位文字「↩ 交给 Shell，⌘↩ 交给 Agent，/ 查看命令」。已识别的命令 token 用强调色片段显示。
3. 路由行：三段提示加「/ 命令」。不可用的一段用 `Ink.inkFaint` 并写明原因；未选默认 Agent 时 ⌘↩、⇧⌘↩ 两段用 `Change.changed`。

阶段 4 起，编辑区上方可以有附件片段，例如「Shell 1 · npm test · exit 1 ×」。

### 6.3 Agent 栏

- 栏头：「Agent」、计数（需要你 N · 工作中 N）、默认 Agent 下拉（等同 `/agent-select`）、「＋」。「＋」的菜单包含：新建默认 Agent 会话、新建其他 Agent 会话 ▸、在原生终端中打开…、从会话文件续接…。
- 行：两行，等你确认时为三行。第一行是 provider 图标、标题，右侧依次为目标标记 `⌘↩`、未读点、用时。第二行是状态与当前活动（caption，`Ink.inkMuted`）。第三行是请求摘要与「允许 / 拒绝 / 查看」。
- 目标行：左侧 3pt `Ink.accent` 竖条，底色 `Ink.accentSoft`。
- 排序：新的在上，之后顺序不变，不按状态重排，刷新不加动画。
- 单击：设为目标并打开详情。终端会话没有结构化详情，单击后主画布切到它的终端。
- 右键菜单：设为目标、打开、重命名、在原生终端中继续、结束。
- 空状态只有两行：「还没有 Agent 会话。」和「在下方写下任务，⌘↩ 交给 Codex。」未选默认 Agent 时第二行改为「先选择默认 Agent」并带「选择」按钮。

### 6.4 Agent 详情（右栏）

```text
‹ Agent    Codex · 修复登录测试                                  ⋯
● 工作中 · 运行 npm test · 0:42                              [中断]
───────────────────────────────────────────────────────────────────
你 · 10:12
修复登录页的单元测试，先跑 npm test 看看哪里挂了

Codex
先运行测试确认失败原因。
▸ $ npm test                                       exit 1 · 12s
✎ src/auth/login.ts  +12 −3
┌ 等你确认 · 运行命令 ──────────────────────────────────────────
│ rm -rf node_modules/.cache
│ 目录 demo · 原因：清理缓存后重跑测试
│ [允许一次]  [本会话允许]  [拒绝]
└──────────────────────────────────────────────────────────────
排队 · 再补一个回归测试                                    [撤回]
───────────────────────────────────────────────────────────────────
本轮改动 3 个文件 +40 −12                              在 Git 中查看
```

- 状态条固定在顶部，工作中时提供「中断」，已结束时提供「续接」和「在原生终端中继续」。
- 用户输入显示为普通段落。Agent 消息只渲染段落、列表、行内代码和代码块，不渲染 HTML。
- 条目默认折叠；运行中的命令展开显示最后 3 行输出；待回答的请求总是展开。推理放在「思考摘要」折叠项里。
- 请求卡片：琥珀色描边，命令或路径用等宽字体并可选中。按钮只显示 provider 给出的选项，任何按钮都不设默认焦点；只有点击或 Tab 聚焦后按空格才会触发。输入条的按键永远不会回答请求。
- 桥以「客户端不支持」回绝的请求（例如 Claude 的提问）显示为一条说明，并提供「在原生终端中继续」。交接要先中断本轮，所以待回答的请求卡片上不放这个入口。
- 底栏的本轮改动来自 `turn.diff`。「在 Git 中查看」在阶段 4 实现，此前只显示统计。diff 正文复用 Git 页的补丁视图；输入模型不同时加一层解析，不新写一套渲染。
- 停在底部时新内容自动滚入；用户上翻后不打断，显示「↓ 新内容」。
- 「⋯」菜单：重命名、在原生终端中继续、结束会话。

### 6.5 原生终端视图

```text
[›_ Shell 1] [›_ Shell 2] [＋]   │   DeepSeek 1 · 原生终端               结束 · ⋯
────────────────────────────────────────────────────────────────────────────────
  （TUI 本身；键盘全部交给它，包括 Esc）
────────────────────────────────────────────────────────────────────────────────
 键盘在 DeepSeek 1 的终端里 · ⌘↩ 回到输入条
```

只有终端会话会进入这个视图。分屏只用于 Shell；Agent 不放进分屏。

### 6.6 删除与合并

| 现有元素 | 去向 |
|---|---|
| 左侧 42pt 会话 rail | Shell 栏（顶部标签）+ 右侧 Agent 栏 |
| 工具栏「＋新会话」弹层与 `SessionLaunchPanel`（**已从源码删除**） | ⇧⌘↩、Shell 栏「＋」、Agent 栏「＋」菜单、`/agent-new`、`/resume` |
| 工具栏「输入」按钮、「⌘↩」字样、后台 Agent chips、「返回 Shell」 | 路由行、Agent 栏、原生终端视图的提示行 |
| `AgentStatusColumn`（原始字节预览） | Agent 栏两行状态 + 结构化详情；终端行不再展开 PTY 预览 |
| 输入条右侧的目标标签与「选择 Agent」按钮 | 路由行与栏头的默认 Agent 下拉 |
| `ConsoleWorkspace.agentMode`、`nativeExpanded`、`agentLayout` | `canvas` / `panel`；旧字段只在归档读写时映射 |
| `AgentLaunchController` | 保留给终端方式；结构化会话走 `AgentStreams` |
| `SessionSwitcherPalette` | 保留，快捷键标注改为 ⌃⌥Tab |

### 6.7 视觉与动效

- 只用既有令牌：`Ink.live` 表示工作中，`Change.changed` 表示需要你，`Change.removed` 表示出错，`Ink.accent` / `Ink.accentSoft` 表示目标，`Ink.inkFaint` 表示已结束。字号用 `Metrics`，命令与路径用 `Face.mono`。
- 遵守减弱动态效果：不使用旋转指示器，改用静态图标；列表刷新不加动画。
- 用时每秒更新，只针对可见的工作中行；相对时间每 30 秒更新。
- 冻结视图与真实视图共用 `AgentRow`、`RouteHints` 等值类型，只把终端与编辑器替换为静态文字，避免两边分叉。

## 7. 分阶段交付（状态）

阶段 0（`BerthAgent` 纯模型与契约）与阶段 1–5 **实现**已并入 [console.md](console.md)、[cli.md](cli.md) 与代码；原各阶段 checklist 已从本文删去，细节以仓库与 git 历史为准。

| 阶段 | 范围（摘要） | 自动化证据 |
|---|---|---|
| 0 | `ComposerRouter`、`AgentStreamReducer`、`SlashParser`、状态投影 | `AgentChecks` |
| 1 | 单输入、Shell 栏、焦点与 zsh/bash/fish 集成默认 | `ConsoleChecks`、`ConfigurationChecks`、`TerminalChecks` |
| 2 | `oberth agent stream`、Codex/Claude/OpenCode/Pi | `go test ./internal/agentstream/... -race`、`internal/cmd` Agent 测试 |
| 3 | 结构化 UI、排队、续接、`StreamConsoleChecks` | `ConsoleChecks` + 合成 `BERTH_BIN` |
| 4 | 命令块→Agent、Git 跳转、后台通知（默认关） | `ComposerAttachmentChecks`、`streamShellAttachmentAgentSendCheck` 等 |
| 5 | Tab 回读、`provider_question` 卡片、bash/fish 集成 | `WorkspaceChecks`、`ShellPromptLineCapture` |

**仍未关闭（发版前，不能由合成测试代替）**

- [validation.md](validation.md) **控制台手测**表：真机 Codex/Claude、IME、复杂提示符；可选系统通知。
- `mage states`：2026-10-15 已生成/刷新 `client/macos/.cache/states/`（含 `10-console-*`、`11-console-split`）；**全量 PNG 须人工逐张翻看**（见 [validation.md](validation.md) 手测表）。`mage window` 同日已生成 `client/macos/.cache/window.png`。

## 8. 验证与未覆盖范围

| 要求 | 证据 |
|---|---|
| 路由、提示、斜杠解析、状态推导、写入字节 | AgentChecks 纯函数测试 |
| 桥协议与进程回收 | `go test`（含 `-race`），合成 provider 替身，schema 校验 |
| 焦点、键位、单一输入 | ConsoleChecks 原生窗口用例（隔离 HOME / BERTH_HOME，`BERTH_CONSOLE_TEST=1`，需要时加 `BERTH_CONSOLE_TEST_QUIET=1`） |
| 布局与样式 | `mage states` 的冻结图逐张查看；菜单与窗口用 `mage window` |
| 真实 provider | 手动：Codex、Claude Code 各两轮对话、一次允许、一次拒绝、一次中断、一次结束后续接、一次在原生终端中继续 |
| 输入法与复杂提示 | 手动：中文输入法组合中 ↩ / ⌘↩ 不提交；在终端提示符处开始的组合转入输入条；powerlevel10k / starship 下集成默认开启后的表现 |

冻结图不等于交互验收；合成替身不等于真实 provider 或其所有版本；没有执行的项必须写明未执行。

## 9. 风险

| 风险 | 对策 |
|---|---|
| provider 协议变化 | 适配器各自隔离，有合成夹具；启动时探测能力，失败返回 `unsupported` 并回退终端方式 |
| Claude 控制通道不是 CLI 承诺的稳定参数 | 按 `system/init` 的 `capabilities` 和握手结果探测；文档写明依赖 |
| 审批误操作 | 按钮不设默认焦点；输入条按键从不回答请求；回答后等 provider 确认才更新 |
| 子进程泄漏 | 客户端回收桥，桥回收 provider 进程组；测试以进程状态为证据 |
| 内存增长 | 轮次、条目、文本、diff 都有上限，超出截断并标记 |
| 隐私 | 对话不落盘；stderr 不含内容；输入不进 argv；附件只在用户显式操作时生成 |
| 同一 provider 会话被两个进程同时打开 | 交接时先关闭再启动；续接前检查本应用没有使用同一会话文件的运行中会话，无法确认就拒绝 |
| 集成默认开启影响用户的提示插件 | settings.json 可关；手动验收覆盖常见提示框架 |
| 焦点回归 | 保留既有原生检查，并新增 5.4 各条的用例 |

## 10. 不做

- 自动判断输入是命令还是自然语言。
- 自建模型、工具循环或上下文管理。
- 安装或修改 agent 的 hooks、插件、MCP、配置；安装 ACP 适配器。
- 自动批准、添加放宽权限的参数、写入 provider 的权限规则。
- 把对话、提示、输出或 provider 会话 ID 写入磁盘。
- 扫描 provider 的历史目录列出会话；隐式「最近一次」续接；跨 worktree 续接。
- 把 Agent 放进 Shell 分屏。
- 默认开启窗口外通知。

## 附：参照与依据

Warp（`b865631c9a0e46b548c7ec7dc32e228a148171d1`），只借鉴交互，不引入实现：

| Warp 的做法 | 本计划 |
|---|---|
| 输入分 Shell / AI 两种类型，可自动判断或锁定（`ai/blocklist/agent_view/gui_input_mode_policy.rs`） | 不自动判断，用 ↩ / ⌘↩ 明确去向 |
| 进入 Agent 视图后输入锁定为 AI，退出回到 Shell | 键位含义不变，Agent 详情放右栏，Shell 始终可见 |
| `/` 只在行首识别，token 含第二个 `/` 视为路径，识别后高亮（`terminal/input/slash_command_model.rs`） | 采用 |
| 命令按上下文决定是否可用（`search/slash_command_menu/static_commands/commands.rs`） | 采用，不可用即隐藏 |
| CLI agent 会话状态：进行中、成功、失败、阻塞、已取消（`terminal/cli_agent_sessions/mod.rs`） | 采用为显示状态，来源换成 provider 机器协议 |
| 细粒度状态依赖装进 agent 的插件与 hooks，经 OSC 777 上报（`terminal/cli_agent_sessions/event/mod.rs`） | 不采用，不改 agent 配置 |
| Ctrl-C 后 2 秒内没有活动就推断为已取消 | 不采用，中断以 provider 确认为准 |
| 通知分完成、请求、错误，聚焦后标为已读（`ai/agent_management/notifications/item.rs`） | 采用为未读规则 |

链接前缀：`https://github.com/warpdotdev/warp/blob/b865631c9a0e46b548c7ec7dc32e228a148171d1/app/src/`

provider 协议：
- Claude Code headless 与 stream-json：https://code.claude.com/docs/en/headless
- Codex app-server：https://learn.chatgpt.com/docs/app-server （源码中的 `codex-rs/app-server/README.md`）
- OpenCode ACP：https://opencode.ai/docs/acp/
