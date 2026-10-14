# 工作台 / Console

工作台是当前 worktree 的原生会话工作区，不是两个名为 Terminal / Agent session 的独立页面。

## 交互决定

- Shell 与 coding-agent 会话共用一个 terminal 主画布和同一 PTY registry。左侧窄 rail 只负责跳转；选择会话只是切换既有 PTY 的呈现与焦点，不重新创建进程。
- rail 按 Shell → Agent 排列并显示运行态；⌘⌥Tab 打开当前 worktree 的非模态会话切换器，⌘Tab / ⇧⌘Tab 在既有会话间循环。切换器按 Shell / Agent 分组，并支持名称、provider、状态和路径检索。
- 当焦点在 agent session 时，Esc 或「返回 Shell」切回进入该 agent 前记住的 Shell；还没有 Shell 时补一个，不向 Agent 发信号。Agent 留在会话栏和右侧状态列表，工具栏上的同一会话可以再次进入。快捷键只绑定当前工作区主窗口，不拦截独立窗口或 terminal 已选中的普通 Esc。投递只有一条：该窗口里实际挂载的视图在 `NSApp.sendEvent` 上安装本地监视器，用同一识别函数处理 ⌘⌥Tab、⌘Tab / ⇧⌘Tab、⌘↩、⇧⌘↩ 和 Esc。同一视图的 `performKeyEquivalent` 是唯一后备，只接监视器没有消费的事件。`windowNumber == 0` 不代表窗口，也不再单独放行。终端焦点上的 ⌘↩ 只把焦点交给会话输入条；⇧⌘↩ 在那里新建 Agent。输入条自己处理 Enter、⌘↩ 和 ⇧⌘↩。其它文本编辑器里的这些键仍交给那个编辑器。
- 打开 worktree 会话时，若该目录还没有 Shell，并且目录真实存在，界面启动一个交互式 Shell 并聚焦它。这是默认界面，不是 Agent 输入页。目录不存在或会话已满时不留下半启动进程。用户已经正在查看的 Agent 保持在前台，Shell 在其下准备好。
- 会话输入条是 Shell 和 Agent 之间的中间层，不另建运行事实。Enter 把当前文字写入当前 Shell 的 PTY（Shell 前台有命令或备用屏时键盘直接交给 PTY）。⌘↩ 把同一段文字写入续接目标的 Agent PTY 作为新一轮输入；尚无 Agent 会话但已选 provider 时用这段文字新建首个会话；已有可续接会话时不必再选 provider。⇧⌘↩ 始终通过既有 `agent plan` 新建 Agent PTY。右侧状态列表可单击设为续接目标并预览输出尾部，「进入」才切主画布。输入 `/` 列出命令（含 `/agent-select`、`/agent`、`/agent-new` 等），可用方向键与 Enter 选择。`settings.json` 里的 `defaultAgent` 对新建工作区算已选择 provider。
- 保留当前混合分屏，选中已有窗格只移动焦点；选择不在分屏中的会话替换当前焦点窗格，不广播输入。
- 去除长篇空态说明。命令块、历史、续接等选项就地提供，安全说明放到帮助/提示。
- 原生焦点由实际挂载的终端视图处理一次性请求，不能在每次状态刷新时夺走文本编辑焦点。
- 离开 worktree 后取消尚未完成的启动计划；已启动会话仍由唯一 PTY registry 持有。路径、draft、输出不跨 worktree 迁移。

## 参考与取舍

Warp 的 Terminal / Agent 两种模式（终端是打开标签后的默认界面，⌘↩ 从终端进入 agent 对话，Esc 回到终端且对话仍可再进入）是交互参照，不是要接进 Warp 的模型。这里的 Agent 仍是外部 CLI 的独立 PTY：离开只切换呈现，进程留在同一 registry，再次进入的是同一个会话。

阅读 Warp 在 b865631c9a0e46b548c7ec7dc32e228a148171d1 的
`app/src/terminal/input/universal.rs`、`terminal.rs`：借鉴上下文、输入和焦点集中在一个工作流的组织方式，
独立实现客户端 UI，不复制其 AI 服务、工具循环、自动语言判断或账户系统。

https://github.com/warpdotdev/warp/blob/b865631c9a0e46b548c7ec7dc32e228a148171d1/app/src/terminal/input/universal.rs
https://github.com/warpdotdev/warp/blob/b865631c9a0e46b548c7ec7dc32e228a148171d1/app/src/terminal/input/terminal.rs

现有原生模拟器、CLI agent plan、显式续接校验与会话 registry 继续复用，不造第二份运行事实。
恢复文件中的旧布局字段是兼容格式，不意味着界面继续保留两套模式页面。

## 待落地：会话输入条改造

本节跟踪尚未并入上方契约的缺口。Agent 通道保持原生 TUI 的 PTY 输入，不改 CLI/JSON 契约。

### 已知缺口（客户端）

- 真实 Codex/Claude Code 上的 ⌘↩ 提交与多行 paste 需账号手动验收。
- 结构化 stdio 通道仍待定（见下表「待定」行）。
- 冻结渲染与全鼠标/输入法专项验收按 `client/macos/AGENTS.md` 另行执行。

### 目标路由

Enter 始终写 Shell；Shell 前台有命令运行时键盘直接交给 PTY，不经输入条转写。

| 状态 | ⌘↩ | ⇧⌘↩ |
|---|---|---|
| 未选 provider | 打开选择，文字保留，选定后继续 | 同左 |
| 已选 provider、无会话 | 用这段文字新建首个会话 | 新建 |
| 选中会话空闲 | 作为新一轮输入写入 | 新建，并成为续接目标 |
| 选中会话正在输出 | 照常写入，提示由 Agent 自行排队 | 新建 |
| 选中会话已退出或为 task 模式 | 不写入，提示 ⇧⌘↩ 或从文件续接 | 新建 |

写入字节：目标开启 bracketed paste 时包裹后加 CR；未开启时单行加 CR，多行拒发并提示。用户文本中的 ESC 被过滤，不能伪造粘贴结束标记。

### 状态推断

只用 PTY 可观察信号：最近输出时间、BEL、OSC 777/9 通知、备用屏、退出码。状态标为推断；信号不足显示「未知」，静默不等于完成。不改 Agent 配置、不装 hooks。单击状态行设为 ⌘↩ 目标并展开最后几行只读预览；双击或「进入」才切主画布。

### 阶段

| 阶段 | 交付 |
|---|---|
| M0 | 已落地：真实 Shell 画布、`SessionPTYInput`、无会话时 ⌘↩ 新建首个会话 |
| M1 | 已落地：`/` 键盘导航与过滤、`/open` 等命令、续接目标标签、按住 ⌘ 高亮、↑ 历史、选 provider 后继续发送 |
| M2 | 已落地：活动观察与推断、预览、「进入」、BEL/OSC 关注点（推断，非 provider 协议） |
| M3 | 已落地：zsh 命令块/备用屏交 PTY、`/open` 聚焦终端；无 zsh 集成时依赖备用屏与 `/open`/点击 |
| 待定 | 结构化 stdio 通道（如 stream-json）需要引擎 `agent plan` 契约变更，等 M2 推断被证明不足再决定 |

验收：路由与写入字节有单测；合成假 Agent 夹具（开启 bracketed paste、备用屏、等待时发 BEL、回显每轮）在隔离 HOME/BERTH_HOME 下覆盖写入与状态。真实 Codex/Claude Code 需要用户已登录的账号，单列为手动验收项，未执行前不算覆盖。

## 验收

必须检查真实混合 PTY 的切换、进程身份、cwd、独立键入、焦点、分屏、独立窗口返回、草稿与取消归属。
冻结图只验证排版；原生窗口测试另外执行，不将截图冒充交互验收。进度见 issue #95 和对应 PR。旧布局/恢复字段继续兼容，session manager sheet 仍作为显式全局兜底入口。
